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
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-controller/api/v1alpha1"
)

const deploymentReadyCondition = "Ready"

// DeploymentReconciler reconciles a Deployment object.
type DeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kudeploy.com,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kudeploy.com,resources=deployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kudeploy.com,resources=deployments/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

// Reconcile moves a Kudeploy Deployment toward one matching Kubernetes Deployment.
func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	kudeployDeployment := &kudeployv1alpha1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, kudeployDeployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !kudeployDeployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if terminating, err := r.namespaceIsTerminating(ctx, kudeployDeployment.Namespace); err != nil || terminating {
		return ctrl.Result{}, err
	}

	if ensureDeploymentMetadata(kudeployDeployment) {
		if err := r.Update(ctx, kudeployDeployment); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, kudeployDeployment); err != nil {
			return ctrl.Result{}, err
		}
	}

	if err := r.createOrUpdateDeploymentEnvSecret(ctx, kudeployDeployment); err != nil {
		return ctrl.Result{}, err
	}

	kubernetesDeployment := buildKubernetesDeployment(kudeployDeployment)
	if err := controllerutil.SetControllerReference(kudeployDeployment, kubernetesDeployment, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createOrUpdateKubernetesDeployment(ctx, kubernetesDeployment); err != nil {
		return ctrl.Result{}, err
	}

	existingKubernetesDeployment := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(kubernetesDeployment), existingKubernetesDeployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		if namespaceIsTerminatingError(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.updateDeploymentStatus(ctx, kudeployDeployment, existingKubernetesDeployment)
}

func (r *DeploymentReconciler) createOrUpdateKubernetesDeployment(ctx context.Context, kubernetesDeployment *appsv1.Deployment) error {
	existingKubernetesDeployment := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(kubernetesDeployment), existingKubernetesDeployment)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, kubernetesDeployment); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
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
	originalKubernetesDeployment := existingKubernetesDeployment.DeepCopy()

	kubernetesDeployment.Spec.Selector = existingKubernetesDeployment.Spec.Selector
	kubernetesDeployment.Labels = mergeManagedLabels(kubernetesDeployment.Labels, existingKubernetesDeployment.Labels)
	kubernetesDeployment.Annotations = mergeMetadata(kubernetesDeployment.Annotations, existingKubernetesDeployment.Annotations)
	kubernetesDeployment.Spec.Template.Labels = mergeManagedLabels(kubernetesDeployment.Spec.Template.Labels, existingKubernetesDeployment.Spec.Template.Labels)
	kubernetesDeployment.Spec.Template.Annotations = mergeMetadata(kubernetesDeployment.Spec.Template.Annotations, existingKubernetesDeployment.Spec.Template.Annotations)

	existingKubernetesDeployment.Labels = kubernetesDeployment.Labels
	existingKubernetesDeployment.Annotations = kubernetesDeployment.Annotations
	existingKubernetesDeployment.OwnerReferences = kubernetesDeployment.OwnerReferences
	existingKubernetesDeployment.Spec = kubernetesDeployment.Spec
	if equality.Semantic.DeepEqual(existingKubernetesDeployment.Labels, originalKubernetesDeployment.Labels) &&
		equality.Semantic.DeepEqual(existingKubernetesDeployment.Annotations, originalKubernetesDeployment.Annotations) &&
		equality.Semantic.DeepEqual(existingKubernetesDeployment.OwnerReferences, originalKubernetesDeployment.OwnerReferences) &&
		equality.Semantic.DeepEqual(existingKubernetesDeployment.Spec, originalKubernetesDeployment.Spec) {
		return nil
	}
	if err := r.Patch(ctx, existingKubernetesDeployment, client.MergeFrom(originalKubernetesDeployment)); err != nil && !namespaceIsTerminatingError(err) {
		return err
	}
	return nil
}

func (r *DeploymentReconciler) createOrUpdateDeploymentEnvSecret(ctx context.Context, kudeployDeployment *kudeployv1alpha1.Deployment) error {
	deploymentEnvSecret, err := r.buildDeploymentEnvSecret(ctx, kudeployDeployment)
	if err != nil {
		if namespaceIsTerminatingError(err) {
			return nil
		}
		return err
	}
	if err := controllerutil.SetControllerReference(kudeployDeployment, deploymentEnvSecret, r.Scheme); err != nil {
		return err
	}

	existingDeploymentEnvSecret := &corev1.Secret{}
	err = r.Get(ctx, client.ObjectKeyFromObject(deploymentEnvSecret), existingDeploymentEnvSecret)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, deploymentEnvSecret); err != nil && !apierrors.IsAlreadyExists(err) && !namespaceIsTerminatingError(err) {
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

	originalDeploymentEnvSecret := existingDeploymentEnvSecret.DeepCopy()
	existingDeploymentEnvSecret.Labels = mergeManagedLabels(deploymentEnvSecret.Labels, existingDeploymentEnvSecret.Labels)
	existingDeploymentEnvSecret.Annotations = mergeMetadata(deploymentEnvSecret.Annotations, existingDeploymentEnvSecret.Annotations)
	existingDeploymentEnvSecret.OwnerReferences = deploymentEnvSecret.OwnerReferences
	if existingDeploymentEnvSecret.Type == "" {
		existingDeploymentEnvSecret.Type = deploymentEnvSecret.Type
	}
	if err := r.Patch(ctx, existingDeploymentEnvSecret, client.MergeFrom(originalDeploymentEnvSecret)); err != nil && !namespaceIsTerminatingError(err) {
		return err
	}
	return nil
}

func (r *DeploymentReconciler) buildDeploymentEnvSecret(ctx context.Context, kudeployDeployment *kudeployv1alpha1.Deployment) (*corev1.Secret, error) {
	serviceEnvSecret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Name: serviceEnvSecretNameFor(kudeployDeployment.Spec.ServiceName), Namespace: kudeployDeployment.Namespace}, serviceEnvSecret); err != nil {
		return nil, err
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        deploymentEnvSecretNameFor(kudeployDeployment.Name),
			Namespace:   kudeployDeployment.Namespace,
			Labels:      deploymentManagedLabels(kudeployDeployment.Namespace, kudeployDeployment.Spec.ServiceName, kudeployDeployment.Name),
			Annotations: copyStringMap(serviceEnvSecret.Annotations),
		},
		Type: corev1.SecretTypeOpaque,
		Data: copySecretData(serviceEnvSecret.Data),
	}, nil
}

func (r *DeploymentReconciler) updateDeploymentStatus(ctx context.Context, kudeployDeployment *kudeployv1alpha1.Deployment, kubernetesDeployment *appsv1.Deployment) error {
	originalKudeployDeployment := kudeployDeployment.DeepCopy()
	kudeployDeployment.Status.KubernetesDeploymentName = kubernetesDeployment.Name
	if isKubernetesDeploymentAvailable(kubernetesDeployment) {
		meta.SetStatusCondition(&kudeployDeployment.Status.Conditions, metav1.Condition{
			Type:    deploymentReadyCondition,
			Status:  metav1.ConditionTrue,
			Reason:  "KubernetesDeploymentAvailable",
			Message: "Kubernetes Deployment is available.",
		})
	} else {
		meta.SetStatusCondition(&kudeployDeployment.Status.Conditions, metav1.Condition{
			Type:    deploymentReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "KubernetesDeploymentProgressing",
			Message: "Kubernetes Deployment is not available yet.",
		})
	}
	return ignoreConflict(r.Status().Patch(ctx, kudeployDeployment, client.MergeFrom(originalKudeployDeployment)))
}

func ensureDeploymentMetadata(kudeployDeployment *kudeployv1alpha1.Deployment) bool {
	labels := deploymentManagedLabels(kudeployDeployment.Namespace, kudeployDeployment.Spec.ServiceName, kudeployDeployment.Name)
	changed := false
	if kudeployDeployment.Labels == nil {
		kudeployDeployment.Labels = map[string]string{}
		changed = true
	}
	for key, value := range labels {
		if kudeployDeployment.Labels[key] != value {
			kudeployDeployment.Labels[key] = value
			changed = true
		}
	}
	return changed
}

func buildKubernetesDeployment(kudeployDeployment *kudeployv1alpha1.Deployment) *appsv1.Deployment {
	labels := deploymentManagedLabels(kudeployDeployment.Namespace, kudeployDeployment.Spec.ServiceName, kudeployDeployment.Name)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kudeployDeployment.Name,
			Namespace: kudeployDeployment.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas:             replicasFor(kudeployDeployment),
			RevisionHistoryLimit: ptrInt32(0),
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: ptrIntOrString(intstr.FromInt32(0)),
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					deploymentLabel: kudeployDeployment.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: kudeployDeployment.Spec.ServiceAccountName,
					Containers: []corev1.Container{
						{
							Name:            kudeployDeployment.Spec.ServiceName,
							Image:           kudeployDeployment.Spec.Image,
							ImagePullPolicy: corev1.PullAlways,
							Command:         kudeployDeployment.Spec.Command,
							Args:            kudeployDeployment.Spec.Args,
							Resources:       kudeployDeployment.Spec.Resources,
							Env:             kudeployDeployment.Spec.Env,
							EnvFrom:         containerEnvFromFor(kudeployDeployment),
							Ports:           containerPortsFor(kudeployDeployment.Spec.Ports),
							ReadinessProbe:  kudeployDeployment.Spec.ReadinessProbe,
							LivenessProbe:   kudeployDeployment.Spec.LivenessProbe,
							StartupProbe:    kudeployDeployment.Spec.StartupProbe,
						},
					},
				},
			},
		},
	}
}

func replicasFor(kudeployDeployment *kudeployv1alpha1.Deployment) *int32 {
	if kudeployDeployment.Spec.Replicas == nil {
		return ptrInt32(1)
	}
	return kudeployDeployment.Spec.Replicas
}

func containerEnvFromFor(kudeployDeployment *kudeployv1alpha1.Deployment) []corev1.EnvFromSource {
	envFrom := make([]corev1.EnvFromSource, 0, len(kudeployDeployment.Spec.EnvFrom)+1)
	envFrom = append(envFrom, kudeployDeployment.Spec.EnvFrom...)
	envFrom = append(envFrom, corev1.EnvFromSource{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: deploymentEnvSecretNameFor(kudeployDeployment.Name),
			},
		},
	})
	return envFrom
}

func containerPortsFor(ports []kudeployv1alpha1.ServicePort) []corev1.ContainerPort {
	containerPorts := make([]corev1.ContainerPort, 0, len(ports))
	for _, port := range ports {
		containerPorts = append(containerPorts, corev1.ContainerPort{
			ContainerPort: targetPortFor(port),
		})
	}
	return containerPorts
}

func targetPortFor(port kudeployv1alpha1.ServicePort) int32 {
	if port.TargetPort == 0 {
		return port.Port
	}
	return port.TargetPort
}

func isKubernetesDeploymentAvailable(kubernetesDeployment *appsv1.Deployment) bool {
	for _, condition := range kubernetesDeployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentAvailable && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *DeploymentReconciler) namespaceIsTerminating(ctx context.Context, namespaceName string) (bool, error) {
	namespace := &corev1.Namespace{}
	if err := r.Get(ctx, client.ObjectKey{Name: namespaceName}, namespace); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	return !namespace.DeletionTimestamp.IsZero(), nil
}

func namespaceIsTerminatingError(err error) bool {
	return apierrors.IsForbidden(err) && strings.Contains(err.Error(), "because it is being terminated")
}

func ptrInt32(value int32) *int32 {
	return &value
}

func ptrIntOrString(value intstr.IntOrString) *intstr.IntOrString {
	return &value
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kudeployv1alpha1.Deployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Secret{}).
		Named("deployment").
		Complete(r)
}
