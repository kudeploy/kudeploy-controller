/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	serviceReadyCondition = "Ready"
	serviceLabel          = "kudeploy.com/service"
	deploymentLabel       = "kudeploy.com/deployment"
)

// ServiceReconciler reconciles a Service object.
type ServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kudeploy.com,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kudeploy.com,resources=services/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kudeploy.com,resources=services/finalizers,verbs=update
// +kubebuilder:rbac:groups=kudeploy.com,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// Reconcile moves the Service toward its desired active Deployment version.
func (r *ServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	service := &kudeployv1alpha1.Service{}
	if err := r.Get(ctx, req.NamespacedName, service); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !service.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if ensureServiceMetadata(service) {
		if err := r.Update(ctx, service); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, service); err != nil {
			return ctrl.Result{}, err
		}
	}

	envSecret, err := r.createOrUpdateServiceEnvSecret(ctx, service)
	if err != nil {
		return ctrl.Result{}, err
	}
	envHash := envSecretHash(envSecret.Data)

	if service.Status.ObservedGeneration != service.Generation || service.Status.LatestEnvSecretHash != envHash {
		return r.reconcileNewServiceVersion(ctx, service, envHash)
	}

	return r.reconcileServiceTraffic(ctx, service)
}

func (r *ServiceReconciler) reconcileNewServiceVersion(ctx context.Context, service *kudeployv1alpha1.Service, envHash string) (ctrl.Result, error) {
	version := service.Status.LatestVersion + 1
	if version == 0 {
		version = 1
	}
	deploymentName := serviceVersionName(service.Name, version)
	if err := r.createOrUpdateRuntimeServiceAccount(ctx, service); err != nil {
		return ctrl.Result{}, err
	}

	kudeployDeployment := buildKudeployDeployment(service, version, deploymentName)
	if err := controllerutil.SetControllerReference(service, kudeployDeployment, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createKudeployDeployment(ctx, kudeployDeployment); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.createOrUpdateKubernetesService(ctx, service, activeDeploymentSelector(service)); err != nil {
		return ctrl.Result{}, err
	}

	originalService := service.DeepCopy()
	service.Status.ObservedGeneration = service.Generation
	service.Status.LatestVersion = version
	service.Status.LatestDeploymentName = deploymentName
	service.Status.LatestEnvSecretHash = envHash
	service.Status.ServiceAccountName = runtimeServiceAccountNameFor(service.Name)
	meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
		Type:    serviceReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  "DeploymentProgressing",
		Message: "Latest Deployment is not ready yet.",
	})
	return ctrl.Result{}, r.patchServiceStatus(ctx, service, originalService)
}

func (r *ServiceReconciler) createOrUpdateServiceEnvSecret(ctx context.Context, service *kudeployv1alpha1.Service) (*corev1.Secret, error) {
	envSecret := buildServiceEnvSecret(service)
	if err := controllerutil.SetControllerReference(service, envSecret, r.Scheme); err != nil {
		return nil, err
	}

	existingEnvSecret := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKeyFromObject(envSecret), existingEnvSecret)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, envSecret); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
			return nil, err
		}
		return envSecret, nil
	}
	if err != nil {
		if namespaceIsTerminatingError(err) {
			return envSecret, nil
		}
		return nil, err
	}
	originalEnvSecret := existingEnvSecret.DeepCopy()
	existingEnvSecret.Labels = mergeManagedLabels(envSecret.Labels, existingEnvSecret.Labels)
	existingEnvSecret.Annotations = mergeMetadata(envSecret.Annotations, existingEnvSecret.Annotations)
	existingEnvSecret.OwnerReferences = envSecret.OwnerReferences
	if existingEnvSecret.Type == "" {
		existingEnvSecret.Type = envSecret.Type
	}
	if err := r.Patch(ctx, existingEnvSecret, client.MergeFrom(originalEnvSecret)); err != nil && !namespaceIsTerminatingError(err) {
		return nil, err
	}
	return existingEnvSecret, nil
}

func (r *ServiceReconciler) reconcileServiceTraffic(ctx context.Context, service *kudeployv1alpha1.Service) (ctrl.Result, error) {
	latestKudeployDeployment := &kudeployv1alpha1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Name: service.Status.LatestDeploymentName, Namespace: service.Namespace}, latestKudeployDeployment)
	if apierrors.IsNotFound(err) {
		originalService := service.DeepCopy()
		meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
			Type:    serviceReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentNotFound",
			Message: "Latest Deployment does not exist.",
		})
		return ctrl.Result{}, r.patchServiceStatus(ctx, service, originalService)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !isKudeployDeploymentReady(latestKudeployDeployment) {
		if err := r.createOrUpdateKubernetesService(ctx, service, activeDeploymentSelector(service)); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.createOrUpdateRuntimeServiceAccount(ctx, service); err != nil {
			return ctrl.Result{}, err
		}
		originalService := service.DeepCopy()
		service.Status.ServiceAccountName = runtimeServiceAccountNameFor(service.Name)
		meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
			Type:    serviceReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentProgressing",
			Message: "Latest Deployment is not ready yet.",
		})
		return ctrl.Result{}, r.patchServiceStatus(ctx, service, originalService)
	}

	selector := map[string]string{deploymentLabel: latestKudeployDeployment.Name}
	if err := r.createOrUpdateKubernetesService(ctx, service, selector); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createOrUpdateRuntimeServiceAccount(ctx, service); err != nil {
		return ctrl.Result{}, err
	}

	originalService := service.DeepCopy()
	service.Status.ServiceAccountName = runtimeServiceAccountNameFor(service.Name)
	service.Status.ActiveVersion = latestKudeployDeployment.Spec.Version
	service.Status.ActiveDeploymentName = latestKudeployDeployment.Name
	meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
		Type:    serviceReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  "DeploymentReady",
		Message: "Latest Deployment is ready and receiving traffic.",
	})
	return ctrl.Result{}, r.patchServiceStatus(ctx, service, originalService)
}

func (r *ServiceReconciler) patchServiceStatus(ctx context.Context, service, originalService *kudeployv1alpha1.Service) error {
	return ignoreConflict(r.Status().Patch(ctx, service, client.MergeFrom(originalService)))
}

func (r *ServiceReconciler) createOrUpdateRuntimeServiceAccount(ctx context.Context, service *kudeployv1alpha1.Service) error {
	serviceAccount := buildRuntimeServiceAccount(service)
	if err := controllerutil.SetControllerReference(service, serviceAccount, r.Scheme); err != nil {
		return err
	}

	existingServiceAccount := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKeyFromObject(serviceAccount), existingServiceAccount)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, serviceAccount); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
			return err
		}
		return nil
	}
	if err != nil {
		if namespaceIsTerminatingError(err) {
			return nil
		}
		return err
	}
	originalServiceAccount := existingServiceAccount.DeepCopy()
	existingServiceAccount.Labels = mergeManagedLabels(serviceAccount.Labels, existingServiceAccount.Labels)
	existingServiceAccount.Annotations = mergeMetadata(serviceAccount.Annotations, existingServiceAccount.Annotations)
	existingServiceAccount.OwnerReferences = serviceAccount.OwnerReferences
	if err := r.Patch(ctx, existingServiceAccount, client.MergeFrom(originalServiceAccount)); err != nil && !namespaceIsTerminatingError(err) {
		return err
	}
	return nil
}

func (r *ServiceReconciler) createKudeployDeployment(ctx context.Context, kudeployDeployment *kudeployv1alpha1.Deployment) error {
	existingKudeployDeployment := &kudeployv1alpha1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(kudeployDeployment), existingKudeployDeployment)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, kudeployDeployment); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
			return err
		}
		return nil
	}
	if namespaceIsTerminatingError(err) {
		return nil
	}
	return err
}

func (r *ServiceReconciler) createOrUpdateKubernetesService(ctx context.Context, service *kudeployv1alpha1.Service, selector map[string]string) error {
	kubernetesService := buildKubernetesService(service, selector)
	if err := controllerutil.SetControllerReference(service, kubernetesService, r.Scheme); err != nil {
		return err
	}

	existingKubernetesService := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKeyFromObject(kubernetesService), existingKubernetesService)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, kubernetesService); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
			return err
		}
		return nil
	}
	if err != nil {
		if namespaceIsTerminatingError(err) {
			return nil
		}
		return err
	}
	originalKubernetesService := existingKubernetesService.DeepCopy()

	existingKubernetesService.Labels = mergeManagedLabels(kubernetesService.Labels, existingKubernetesService.Labels)
	existingKubernetesService.Annotations = mergeMetadata(kubernetesService.Annotations, existingKubernetesService.Annotations)
	existingKubernetesService.OwnerReferences = kubernetesService.OwnerReferences
	existingKubernetesService.Spec.Ports = kubernetesService.Spec.Ports
	existingKubernetesService.Spec.Selector = kubernetesService.Spec.Selector
	if err := r.Patch(ctx, existingKubernetesService, client.MergeFrom(originalKubernetesService)); err != nil && !namespaceIsTerminatingError(err) {
		return err
	}
	return nil
}

func ensureServiceMetadata(service *kudeployv1alpha1.Service) bool {
	changed := false
	if service.Labels == nil {
		service.Labels = map[string]string{}
		changed = true
	}
	if service.Labels[projectLabel] != service.Namespace {
		service.Labels[projectLabel] = service.Namespace
		changed = true
	}
	if service.Labels[managedByLabel] != managedByLabelValue {
		service.Labels[managedByLabel] = managedByLabelValue
		changed = true
	}
	return changed
}

func buildKudeployDeployment(kudeployService *kudeployv1alpha1.Service, version int64, name string) *kudeployv1alpha1.Deployment {
	return &kudeployv1alpha1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: kudeployService.Namespace,
			Labels:    deploymentManagedLabels(kudeployService.Namespace, kudeployService.Name, name),
		},
		Spec: kudeployv1alpha1.DeploymentSpec{
			ServiceName:        kudeployService.Name,
			Version:            version,
			ServiceAccountName: runtimeServiceAccountNameFor(kudeployService.Name),
			Replicas:           kudeployService.Spec.Replicas,
			Image:              kudeployService.Spec.Image,
			Command:            kudeployService.Spec.Command,
			Args:               kudeployService.Spec.Args,
			Resources:          kudeployService.Spec.Resources,
			Ports:              kudeployService.Spec.Ports,
			Env:                kudeployService.Spec.Env,
			EnvFrom:            kudeployService.Spec.EnvFrom,
			ReadinessProbe:     kudeployService.Spec.ReadinessProbe,
			LivenessProbe:      kudeployService.Spec.LivenessProbe,
			StartupProbe:       kudeployService.Spec.StartupProbe,
		},
	}
}

func buildServiceEnvSecret(kudeployService *kudeployv1alpha1.Service) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceEnvSecretNameFor(kudeployService.Name),
			Namespace: kudeployService.Namespace,
			Labels: map[string]string{
				projectLabel:   kudeployService.Namespace,
				serviceLabel:   kudeployService.Name,
				managedByLabel: managedByLabelValue,
			},
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func buildRuntimeServiceAccount(kudeployService *kudeployv1alpha1.Service) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runtimeServiceAccountNameFor(kudeployService.Name),
			Namespace: kudeployService.Namespace,
			Labels: map[string]string{
				projectLabel:   kudeployService.Namespace,
				serviceLabel:   kudeployService.Name,
				managedByLabel: managedByLabelValue,
			},
		},
	}
}

func buildKubernetesService(kudeployService *kudeployv1alpha1.Service, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kudeployService.Name,
			Namespace: kudeployService.Namespace,
			Labels: map[string]string{
				projectLabel:   kudeployService.Namespace,
				serviceLabel:   kudeployService.Name,
				managedByLabel: managedByLabelValue,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    servicePortsFor(kudeployService.Spec.Ports),
		},
	}
}

func servicePortsFor(ports []kudeployv1alpha1.ServicePort) []corev1.ServicePort {
	servicePorts := make([]corev1.ServicePort, 0, len(ports))
	for _, port := range ports {
		servicePorts = append(servicePorts, corev1.ServicePort{
			Name:       fmt.Sprintf("port-%d", port.Port),
			Port:       port.Port,
			TargetPort: intstr.FromInt32(targetPortFor(port)),
			Protocol:   corev1.ProtocolTCP,
		})
	}
	return servicePorts
}

func activeDeploymentSelector(service *kudeployv1alpha1.Service) map[string]string {
	if service.Status.ActiveDeploymentName == "" {
		return nil
	}
	return map[string]string{deploymentLabel: service.Status.ActiveDeploymentName}
}

func isKudeployDeploymentReady(kudeployDeployment *kudeployv1alpha1.Deployment) bool {
	condition := meta.FindStatusCondition(kudeployDeployment.Status.Conditions, serviceReadyCondition)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

func serviceVersionName(serviceName string, version int64) string {
	suffix := fmt.Sprintf("-%05d", version)
	return childName(serviceName, suffix)
}

func deploymentManagedLabels(namespace, serviceName, deploymentName string) map[string]string {
	return map[string]string{
		projectLabel:    namespace,
		serviceLabel:    serviceName,
		deploymentLabel: deploymentName,
		managedByLabel:  managedByLabelValue,
	}
}

func runtimeServiceAccountNameFor(serviceName string) string {
	return childName("service-"+serviceName, "")
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kudeployv1alpha1.Service{}).
		Owns(&kudeployv1alpha1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.Secret{}).
		Named("service").
		Complete(r)
}
