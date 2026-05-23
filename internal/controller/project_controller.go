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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-controller/api/v1alpha1"
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

	// BuildRunDockerSecretName is copied from BuildRunDockerSecretNamespace into every managed Project namespace.
	// When empty, docker secret sync is disabled.
	BuildRunDockerSecretName      string
	BuildRunDockerSecretNamespace string
}

// +kubebuilder:rbac:groups=kudeploy.com,resources=projects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kudeploy.com,resources=projects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kudeploy.com,resources=projects/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

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
	} else if err != nil {
		return ctrl.Result{}, err
	}

	if !isManagedNamespace(namespace, project.Name) {
		log.Info("Same-name Namespace is not managed by this Project", "namespace", namespace.Name)
		return ctrl.Result{}, r.updateProjectStatus(ctx, project, metav1.Condition{
			Type:    projectReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "NamespaceConflict",
			Message: "A same-name Namespace already exists and is not managed by Kudeploy.",
		})
	}

	if condition, err := r.syncBuildRunDockerSecret(ctx, project); err != nil {
		return ctrl.Result{}, err
	} else if condition != nil {
		log.Info("BuildRun docker Secret sync blocked", "reason", condition.Reason, "message", condition.Message)
		return ctrl.Result{}, r.updateProjectStatus(ctx, project, *condition)
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

func (r *ProjectReconciler) syncBuildRunDockerSecret(ctx context.Context, project *kudeployv1alpha1.Project) (*metav1.Condition, error) {
	if !r.hasBuildRunDockerSecretSync() {
		return nil, nil
	}

	source := &corev1.Secret{}
	sourceKey := client.ObjectKey{Name: r.BuildRunDockerSecretName, Namespace: r.BuildRunDockerSecretNamespace}
	err := r.Get(ctx, sourceKey, source)
	if apierrors.IsNotFound(err) {
		return &metav1.Condition{
			Type:    projectReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "BuildRunDockerSecretNotFound",
			Message: fmt.Sprintf("BuildRun docker Secret %q does not exist in namespace %q.", r.BuildRunDockerSecretName, r.BuildRunDockerSecretNamespace),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if source.Type != corev1.SecretTypeDockerConfigJson {
		return &metav1.Condition{
			Type:    projectReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  "BuildRunDockerSecretInvalidType",
			Message: fmt.Sprintf("BuildRun docker Secret %q in namespace %q has type %q, expected %q.", source.Name, source.Namespace, source.Type, corev1.SecretTypeDockerConfigJson),
		}, nil
	}

	desired := buildProjectBuildRunDockerSecret(source, project.Name)
	if err := controllerutil.SetControllerReference(project, desired, r.Scheme); err != nil {
		return nil, err
	}
	return nil, r.createOrUpdateBuildRunDockerSecret(ctx, desired)
}

func (r *ProjectReconciler) createOrUpdateBuildRunDockerSecret(ctx context.Context, desired *corev1.Secret) error {
	current := &corev1.Secret{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), current)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	if !buildRunDockerSecretNeedsUpdate(current, desired) {
		return nil
	}

	current.Labels = desired.Labels
	current.Annotations = desired.Annotations
	current.OwnerReferences = desired.OwnerReferences
	current.Type = desired.Type
	current.Data = desired.Data
	return r.Update(ctx, current)
}

func buildProjectBuildRunDockerSecret(source *corev1.Secret, namespace string) *corev1.Secret {
	labels := copyStringMap(source.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	labels[projectLabel] = namespace
	labels[managedByLabel] = managedByLabelValue

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        source.Name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: copyStringMap(source.Annotations),
		},
		Type: source.Type,
		Data: copySecretData(source.Data),
	}
}

func buildRunDockerSecretNeedsUpdate(current, desired *corev1.Secret) bool {
	return !apiequality.Semantic.DeepEqual(current.Labels, desired.Labels) ||
		!apiequality.Semantic.DeepEqual(current.Annotations, desired.Annotations) ||
		!apiequality.Semantic.DeepEqual(current.OwnerReferences, desired.OwnerReferences) ||
		current.Type != desired.Type ||
		!apiequality.Semantic.DeepEqual(current.Data, desired.Data)
}

func (r *ProjectReconciler) hasBuildRunDockerSecretSync() bool {
	return r.BuildRunDockerSecretName != "" && r.BuildRunDockerSecretNamespace != ""
}

func (r *ProjectReconciler) isBuildRunDockerSecret(object client.Object) bool {
	return r.hasBuildRunDockerSecretSync() &&
		object.GetName() == r.BuildRunDockerSecretName &&
		object.GetNamespace() == r.BuildRunDockerSecretNamespace
}

func (r *ProjectReconciler) projectsForBuildRunDockerSecret(ctx context.Context, object client.Object) []reconcile.Request {
	if !r.isBuildRunDockerSecret(object) {
		return nil
	}

	projects := &kudeployv1alpha1.ProjectList{}
	if err := r.List(ctx, projects); err != nil {
		logf.FromContext(ctx).Error(err, "Failed to list Projects for BuildRun docker Secret sync")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(projects.Items))
	for _, project := range projects.Items {
		requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: project.Name}})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *ProjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	controllerBuilder := ctrl.NewControllerManagedBy(mgr).
		For(&kudeployv1alpha1.Project{}).
		Owns(&corev1.Secret{})

	if r.hasBuildRunDockerSecretSync() {
		controllerBuilder = controllerBuilder.Watches(
			&corev1.Secret{},
			handler.EnqueueRequestsFromMapFunc(r.projectsForBuildRunDockerSecret),
			builder.WithPredicates(predicate.NewPredicateFuncs(r.isBuildRunDockerSecret)),
		)
	}

	return controllerBuilder.Named("project").Complete(r)
}
