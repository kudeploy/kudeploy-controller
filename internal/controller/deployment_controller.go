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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-operator/api/v1alpha1"
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

// Reconcile moves a Kudeploy Deployment toward one matching Kubernetes Deployment.
func (r *DeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	deployment := &kudeployv1alpha1.Deployment{}
	if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !deployment.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if terminating, err := r.namespaceIsTerminating(ctx, deployment.Namespace); err != nil || terminating {
		return ctrl.Result{}, err
	}

	if ensureDeploymentMetadata(deployment) {
		if err := r.Update(ctx, deployment); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, deployment); err != nil {
			return ctrl.Result{}, err
		}
	}

	kubernetesDeployment := buildKubernetesDeployment(deployment)
	if err := controllerutil.SetControllerReference(deployment, kubernetesDeployment, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createOrUpdateKubernetesDeployment(ctx, kubernetesDeployment); err != nil {
		return ctrl.Result{}, err
	}

	current := &appsv1.Deployment{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(kubernetesDeployment), current); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		if namespaceIsTerminatingError(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.updateDeploymentStatus(ctx, deployment, current)
}

func (r *DeploymentReconciler) createOrUpdateKubernetesDeployment(ctx context.Context, desired *appsv1.Deployment) error {
	current := &appsv1.Deployment{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		if namespaceIsTerminatingError(err) {
			return nil
		}
		return err
	}
	original := current.DeepCopy()
	current.Labels = desired.Labels
	current.OwnerReferences = desired.OwnerReferences
	current.Spec = desired.Spec
	if err := r.Patch(ctx, current, client.MergeFrom(original)); err != nil && !namespaceIsTerminatingError(err) {
		return err
	}
	return nil
}

func (r *DeploymentReconciler) updateDeploymentStatus(ctx context.Context, deployment *kudeployv1alpha1.Deployment, kubernetesDeployment *appsv1.Deployment) error {
	original := deployment.DeepCopy()
	deployment.Status.KubernetesDeploymentName = kubernetesDeployment.Name
	if isKubernetesDeploymentAvailable(kubernetesDeployment) {
		meta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    deploymentReadyCondition,
			Status:  metav1.ConditionTrue,
			Reason:  "KubernetesDeploymentAvailable",
			Message: "Kubernetes Deployment is available.",
		})
	} else {
		meta.SetStatusCondition(&deployment.Status.Conditions, metav1.Condition{
			Type:    deploymentReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "KubernetesDeploymentProgressing",
			Message: "Kubernetes Deployment is not available yet.",
		})
	}
	return ignoreConflict(r.Status().Patch(ctx, deployment, client.MergeFrom(original)))
}

func ensureDeploymentMetadata(deployment *kudeployv1alpha1.Deployment) bool {
	desiredLabels := deploymentManagedLabels(deployment.Namespace, deployment.Spec.ServiceName, deployment.Name)
	changed := false
	if deployment.Labels == nil {
		deployment.Labels = map[string]string{}
		changed = true
	}
	for key, value := range desiredLabels {
		if deployment.Labels[key] != value {
			deployment.Labels[key] = value
			changed = true
		}
	}
	return changed
}

func buildKubernetesDeployment(deployment *kudeployv1alpha1.Deployment) *appsv1.Deployment {
	labels := deploymentManagedLabels(deployment.Namespace, deployment.Spec.ServiceName, deployment.Name)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployment.Name,
			Namespace: deployment.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptrInt32(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					deploymentLabel: deployment.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: deployment.Spec.ServiceAccountName,
					Containers: []corev1.Container{
						{
							Name:            deployment.Spec.ServiceName,
							Image:           deployment.Spec.Image,
							ImagePullPolicy: corev1.PullAlways,
							Ports:           containerPortsFor(deployment.Spec.Ports),
						},
					},
				},
			},
		},
	}
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

func isKubernetesDeploymentAvailable(deployment *appsv1.Deployment) bool {
	for _, condition := range deployment.Status.Conditions {
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

// SetupWithManager sets up the controller with the Manager.
func (r *DeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kudeployv1alpha1.Deployment{}).
		Owns(&appsv1.Deployment{}).
		Named("deployment").
		Complete(r)
}
