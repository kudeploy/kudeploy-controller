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

	deployment := buildKudeployDeployment(service, version, deploymentName)
	if err := controllerutil.SetControllerReference(service, deployment, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createKudeployDeployment(ctx, deployment); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.createOrUpdateStableKubernetesService(ctx, service, activeServiceSelector(service)); err != nil {
		return ctrl.Result{}, err
	}

	original := service.DeepCopy()
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
	return ctrl.Result{}, r.patchServiceStatus(ctx, service, original)
}

func (r *ServiceReconciler) createOrUpdateServiceEnvSecret(ctx context.Context, service *kudeployv1alpha1.Service) (*corev1.Secret, error) {
	desired := serviceEnvSecret(service)
	if err := controllerutil.SetControllerReference(service, desired, r.Scheme); err != nil {
		return nil, err
	}

	current := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
			return nil, err
		}
		return desired, nil
	}
	if err != nil {
		if namespaceIsTerminatingError(err) {
			return desired, nil
		}
		return nil, err
	}
	original := current.DeepCopy()
	current.Labels = mergeManagedLabels(desired.Labels, current.Labels)
	current.Annotations = mergeMetadata(desired.Annotations, current.Annotations)
	current.OwnerReferences = desired.OwnerReferences
	if current.Type == "" {
		current.Type = desired.Type
	}
	if err := r.Patch(ctx, current, client.MergeFrom(original)); err != nil && !namespaceIsTerminatingError(err) {
		return nil, err
	}
	return current, nil
}

func (r *ServiceReconciler) reconcileServiceTraffic(ctx context.Context, service *kudeployv1alpha1.Service) (ctrl.Result, error) {
	latestDeployment := &kudeployv1alpha1.Deployment{}
	err := r.Get(ctx, client.ObjectKey{Name: service.Status.LatestDeploymentName, Namespace: service.Namespace}, latestDeployment)
	if apierrors.IsNotFound(err) {
		original := service.DeepCopy()
		meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
			Type:    serviceReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentNotFound",
			Message: "Latest Deployment does not exist.",
		})
		return ctrl.Result{}, r.patchServiceStatus(ctx, service, original)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !isKudeployDeploymentReady(latestDeployment) {
		if err := r.createOrUpdateStableKubernetesService(ctx, service, activeServiceSelector(service)); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.createOrUpdateRuntimeServiceAccount(ctx, service); err != nil {
			return ctrl.Result{}, err
		}
		original := service.DeepCopy()
		service.Status.ServiceAccountName = runtimeServiceAccountNameFor(service.Name)
		meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
			Type:    serviceReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "DeploymentProgressing",
			Message: "Latest Deployment is not ready yet.",
		})
		return ctrl.Result{}, r.patchServiceStatus(ctx, service, original)
	}

	selector := map[string]string{deploymentLabel: latestDeployment.Name}
	if err := r.createOrUpdateStableKubernetesService(ctx, service, selector); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createOrUpdateRuntimeServiceAccount(ctx, service); err != nil {
		return ctrl.Result{}, err
	}

	original := service.DeepCopy()
	service.Status.ServiceAccountName = runtimeServiceAccountNameFor(service.Name)
	service.Status.ActiveVersion = latestDeployment.Spec.Version
	service.Status.ActiveDeploymentName = latestDeployment.Name
	meta.SetStatusCondition(&service.Status.Conditions, metav1.Condition{
		Type:    serviceReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  "DeploymentReady",
		Message: "Latest Deployment is ready and receiving traffic.",
	})
	return ctrl.Result{}, r.patchServiceStatus(ctx, service, original)
}

func (r *ServiceReconciler) patchServiceStatus(ctx context.Context, service, original *kudeployv1alpha1.Service) error {
	return ignoreConflict(r.Status().Patch(ctx, service, client.MergeFrom(original)))
}

func (r *ServiceReconciler) createOrUpdateRuntimeServiceAccount(ctx context.Context, service *kudeployv1alpha1.Service) error {
	desired := runtimeServiceAccount(service)
	if err := controllerutil.SetControllerReference(service, desired, r.Scheme); err != nil {
		return err
	}

	current := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
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
	original := current.DeepCopy()
	current.Labels = mergeManagedLabels(desired.Labels, current.Labels)
	current.Annotations = mergeMetadata(desired.Annotations, current.Annotations)
	current.OwnerReferences = desired.OwnerReferences
	if err := r.Patch(ctx, current, client.MergeFrom(original)); err != nil && !namespaceIsTerminatingError(err) {
		return err
	}
	return nil
}

func (r *ServiceReconciler) createKudeployDeployment(ctx context.Context, desired *kudeployv1alpha1.Deployment) error {
	current := &kudeployv1alpha1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
			return err
		}
		return nil
	}
	if namespaceIsTerminatingError(err) {
		return nil
	}
	return err
}

func (r *ServiceReconciler) createOrUpdateStableKubernetesService(ctx context.Context, service *kudeployv1alpha1.Service, selector map[string]string) error {
	desired := stableKubernetesService(service, selector)
	if err := controllerutil.SetControllerReference(service, desired, r.Scheme); err != nil {
		return err
	}

	current := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
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
	original := current.DeepCopy()

	current.Labels = mergeManagedLabels(desired.Labels, current.Labels)
	current.Annotations = mergeMetadata(desired.Annotations, current.Annotations)
	current.OwnerReferences = desired.OwnerReferences
	current.Spec.Ports = desired.Spec.Ports
	current.Spec.Selector = desired.Spec.Selector
	if err := r.Patch(ctx, current, client.MergeFrom(original)); err != nil && !namespaceIsTerminatingError(err) {
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

func buildKudeployDeployment(service *kudeployv1alpha1.Service, version int64, name string) *kudeployv1alpha1.Deployment {
	return &kudeployv1alpha1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: service.Namespace,
			Labels:    deploymentManagedLabels(service.Namespace, service.Name, name),
		},
		Spec: kudeployv1alpha1.DeploymentSpec{
			ServiceName:        service.Name,
			Version:            version,
			ServiceAccountName: runtimeServiceAccountNameFor(service.Name),
			Image:              service.Spec.Image,
			Ports:              service.Spec.Ports,
			Env:                service.Spec.Env,
			EnvFrom:            service.Spec.EnvFrom,
		},
	}
}

func serviceEnvSecret(service *kudeployv1alpha1.Service) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceEnvSecretNameFor(service.Name),
			Namespace: service.Namespace,
			Labels: map[string]string{
				projectLabel:   service.Namespace,
				serviceLabel:   service.Name,
				managedByLabel: managedByLabelValue,
			},
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func runtimeServiceAccount(service *kudeployv1alpha1.Service) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runtimeServiceAccountNameFor(service.Name),
			Namespace: service.Namespace,
			Labels: map[string]string{
				projectLabel:   service.Namespace,
				serviceLabel:   service.Name,
				managedByLabel: managedByLabelValue,
			},
		},
	}
}

func stableKubernetesService(service *kudeployv1alpha1.Service, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      service.Name,
			Namespace: service.Namespace,
			Labels: map[string]string{
				projectLabel:   service.Namespace,
				serviceLabel:   service.Name,
				managedByLabel: managedByLabelValue,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    servicePortsFor(service.Spec.Ports),
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

func activeServiceSelector(service *kudeployv1alpha1.Service) map[string]string {
	if service.Status.ActiveDeploymentName == "" {
		return nil
	}
	return map[string]string{deploymentLabel: service.Status.ActiveDeploymentName}
}

func isKudeployDeploymentReady(deployment *kudeployv1alpha1.Deployment) bool {
	condition := meta.FindStatusCondition(deployment.Status.Conditions, serviceReadyCondition)
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
