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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-operator/api/v1alpha1"
)

const (
	projectFinalizer = "kudeploy.com/project"

	projectLabel        = "kudeploy.com/project"
	managedByLabel      = "app.kubernetes.io/managed-by"
	managedByLabelValue = "kudeploy"

	projectReadyCondition = "Ready"
)

// ProjectReconciler reconciles a Project object
type ProjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kudeploy.com,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kudeploy.com,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kudeploy.com,resources=projects/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *ProjectReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	project := &kudeployv1alpha1.Project{}
	if err := r.Get(ctx, req.NamespacedName, project); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !project.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, project)
	}

	if controllerutil.AddFinalizer(project, projectFinalizer) {
		if err := r.Update(ctx, project); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, project); err != nil {
			return ctrl.Result{}, err
		}
	}

	namespace := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: project.Name}, namespace)
	if apierrors.IsNotFound(err) {
		namespace = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: project.Name,
				Labels: map[string]string{
					projectLabel:   project.Name,
					managedByLabel: managedByLabelValue,
				},
			},
		}
		if err := r.Create(ctx, namespace); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.updateProjectStatus(ctx, project, metav1.Condition{
			Type:    projectReadyCondition,
			Status:  metav1.ConditionTrue,
			Reason:  "NamespaceReady",
			Message: "Namespace is managed by Kudeploy.",
		})
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !isManagedNamespace(namespace, project.Name) {
		log.Info("same-name namespace is not managed by this project", "namespace", namespace.Name)
		return ctrl.Result{}, r.updateProjectStatus(ctx, project, metav1.Condition{
			Type:    projectReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "NamespaceConflict",
			Message: "A same-name Namespace already exists and is not managed by Kudeploy.",
		})
	}

	return ctrl.Result{}, r.updateProjectStatus(ctx, project, metav1.Condition{
		Type:    projectReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  "NamespaceReady",
		Message: "Namespace is managed by Kudeploy.",
	})
}

func (r *ProjectReconciler) reconcileDelete(ctx context.Context, project *kudeployv1alpha1.Project) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(project, projectFinalizer) {
		return ctrl.Result{}, nil
	}

	namespace := &corev1.Namespace{}
	err := r.Get(ctx, client.ObjectKey{Name: project.Name}, namespace)
	if apierrors.IsNotFound(err) {
		controllerutil.RemoveFinalizer(project, projectFinalizer)
		return ctrl.Result{}, r.Update(ctx, project)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	if !isManagedNamespace(namespace, project.Name) {
		controllerutil.RemoveFinalizer(project, projectFinalizer)
		return ctrl.Result{}, r.Update(ctx, project)
	}

	if namespace.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *ProjectReconciler) updateProjectStatus(ctx context.Context, project *kudeployv1alpha1.Project, condition metav1.Condition) error {
	original := project.DeepCopy()
	project.Status.NamespaceName = project.Name
	meta.SetStatusCondition(&project.Status.Conditions, condition)
	return ignoreConflict(r.Status().Patch(ctx, project, client.MergeFrom(original)))
}

func isManagedNamespace(namespace *corev1.Namespace, projectName string) bool {
	return namespace.Labels[projectLabel] == projectName &&
		namespace.Labels[managedByLabel] == managedByLabelValue
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kudeployv1alpha1.Project{}).
		Named("project").
		Complete(r)
}
