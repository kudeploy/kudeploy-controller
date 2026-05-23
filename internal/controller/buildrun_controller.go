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

	tektonpod "github.com/tektoncd/pipeline/pkg/apis/pipeline/pod"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"knative.dev/pkg/apis"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-controller/api/v1alpha1"
)

const (
	buildRunFinalizer = "kudeploy.com/buildrun"

	buildRunLabel       = "kudeploy.com/buildrun"
	buildRunReady       = "Ready"
	buildRunSAPrefix    = "buildrun-"
	sourceWorkspaceName = "source"
	sourceWorkspaceSize = "1Gi"
	buildRunPodFSGroup  = int64(65532)
	defaultBuildContext = "."
	defaultDockerfile   = "./Dockerfile"

	buildPipelineURL = "https://raw.githubusercontent.com/kudeploy/kudeploy-manifests/main/tekton/pipelines/build-and-push.yaml"
)

// BuildRunReconciler reconciles a BuildRun object
type BuildRunReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=kudeploy.com,resources=buildruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kudeploy.com,resources=buildruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kudeploy.com,resources=buildruns/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=tekton.dev,resources=pipelineruns,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *BuildRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	buildRun := &kudeployv1alpha1.BuildRun{}
	if err := r.Get(ctx, req.NamespacedName, buildRun); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !buildRun.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, buildRun)
	}

	if ensureBuildRunMetadata(buildRun) {
		if err := r.Update(ctx, buildRun); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
		if err := r.Get(ctx, req.NamespacedName, buildRun); err != nil {
			return ctrl.Result{}, err
		}
	}

	if missing, err := r.missingSecret(ctx, buildRun); err != nil {
		return ctrl.Result{}, err
	} else if missing != "" {
		log.Info("referenced Secret does not exist", "secret", missing)
		return ctrl.Result{}, r.updateBuildRunStatus(ctx, buildRun, metav1.Condition{
			Type:    buildRunReady,
			Status:  metav1.ConditionFalse,
			Reason:  "SecretNotFound",
			Message: fmt.Sprintf("Secret %q does not exist in namespace %q.", missing, buildRun.Namespace),
		})
	}

	serviceAccount := buildRunServiceAccount(buildRun)
	if err := controllerutil.SetControllerReference(buildRun, serviceAccount, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createOrUpdateBuildRunServiceAccount(ctx, serviceAccount); err != nil {
		return ctrl.Result{}, err
	}

	pipelineRun := buildPipelineRun(buildRun)
	if err := controllerutil.SetControllerReference(buildRun, pipelineRun, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.createPipelineRun(ctx, pipelineRun); err != nil {
		return ctrl.Result{}, err
	}

	currentPipelineRun := &tektonv1.PipelineRun{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(pipelineRun), currentPipelineRun); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.updateBuildRunStatusFromPipelineRun(ctx, buildRun, currentPipelineRun)
}

func (r *BuildRunReconciler) reconcileDelete(ctx context.Context, buildRun *kudeployv1alpha1.BuildRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(buildRun, buildRunFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.deleteIfExists(ctx, &tektonv1.PipelineRun{ObjectMeta: metav1.ObjectMeta{Name: buildRun.Name, Namespace: buildRun.Namespace}}); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.deleteIfExists(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: buildRunServiceAccountNameFor(buildRun.Name), Namespace: buildRun.Namespace}}); err != nil {
		return ctrl.Result{}, err
	}

	controllerutil.RemoveFinalizer(buildRun, buildRunFinalizer)
	return ctrl.Result{}, r.Update(ctx, buildRun)
}

func (r *BuildRunReconciler) missingSecret(ctx context.Context, buildRun *kudeployv1alpha1.BuildRun) (string, error) {
	for _, secretRef := range []*corev1.LocalObjectReference{buildRun.Spec.Git.SecretRef, buildRun.Spec.Image.SecretRef} {
		if secretRef == nil || secretRef.Name == "" {
			continue
		}
		referencedSecret := &corev1.Secret{}
		err := r.Get(ctx, client.ObjectKey{Name: secretRef.Name, Namespace: buildRun.Namespace}, referencedSecret)
		if apierrors.IsNotFound(err) {
			return secretRef.Name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", nil
}

func (r *BuildRunReconciler) createOrUpdateBuildRunServiceAccount(ctx context.Context, serviceAccount *corev1.ServiceAccount) error {
	existingServiceAccount := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKeyFromObject(serviceAccount), existingServiceAccount)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, serviceAccount)
	}
	if err != nil {
		return err
	}
	existingServiceAccount.Labels = serviceAccount.Labels
	existingServiceAccount.OwnerReferences = serviceAccount.OwnerReferences
	existingServiceAccount.Secrets = serviceAccount.Secrets
	existingServiceAccount.ImagePullSecrets = serviceAccount.ImagePullSecrets
	return r.Update(ctx, existingServiceAccount)
}

func (r *BuildRunReconciler) createPipelineRun(ctx context.Context, pipelineRun *tektonv1.PipelineRun) error {
	existingPipelineRun := &tektonv1.PipelineRun{}
	err := r.Get(ctx, client.ObjectKeyFromObject(pipelineRun), existingPipelineRun)
	if apierrors.IsNotFound(err) {
		if err := r.Create(ctx, pipelineRun); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	}
	return err
}

func (r *BuildRunReconciler) deleteIfExists(ctx context.Context, object client.Object) error {
	err := r.Delete(ctx, object)
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (r *BuildRunReconciler) updateBuildRunStatus(ctx context.Context, buildRun *kudeployv1alpha1.BuildRun, condition metav1.Condition) error {
	originalBuildRun := buildRun.DeepCopy()
	buildRun.Status.PipelineRunName = buildRun.Name
	buildRun.Status.ServiceAccountName = buildRunServiceAccountNameFor(buildRun.Name)
	meta.SetStatusCondition(&buildRun.Status.Conditions, condition)
	return ignoreConflict(r.Status().Patch(ctx, buildRun, client.MergeFrom(originalBuildRun)))
}

func (r *BuildRunReconciler) updateBuildRunStatusFromPipelineRun(ctx context.Context, buildRun *kudeployv1alpha1.BuildRun, pipelineRun *tektonv1.PipelineRun) error {
	originalBuildRun := buildRun.DeepCopy()
	buildRun.Status.PipelineRunName = pipelineRun.Name
	buildRun.Status.ServiceAccountName = buildRunServiceAccountNameFor(buildRun.Name)
	buildRun.Status.StartTime = pipelineRun.Status.StartTime
	buildRun.Status.CompletionTime = pipelineRun.Status.CompletionTime
	meta.SetStatusCondition(&buildRun.Status.Conditions, buildRunConditionFromPipelineRun(pipelineRun))
	return ignoreConflict(r.Status().Patch(ctx, buildRun, client.MergeFrom(originalBuildRun)))
}

func buildRunConditionFromPipelineRun(pipelineRun *tektonv1.PipelineRun) metav1.Condition {
	succeeded := pipelineRun.Status.GetCondition(apis.ConditionSucceeded)
	if succeeded == nil {
		return metav1.Condition{
			Type:    buildRunReady,
			Status:  metav1.ConditionUnknown,
			Reason:  "PipelineRunCreated",
			Message: "PipelineRun was created and is waiting for Tekton status.",
		}
	}

	message := succeeded.Message
	if message == "" {
		message = "PipelineRun status was reported by Tekton."
	}

	switch succeeded.Status {
	case corev1.ConditionTrue:
		return metav1.Condition{
			Type:    buildRunReady,
			Status:  metav1.ConditionTrue,
			Reason:  "PipelineRunSucceeded",
			Message: message,
		}
	case corev1.ConditionFalse:
		return metav1.Condition{
			Type:    buildRunReady,
			Status:  metav1.ConditionFalse,
			Reason:  "PipelineRunFailed",
			Message: message,
		}
	default:
		return metav1.Condition{
			Type:    buildRunReady,
			Status:  metav1.ConditionUnknown,
			Reason:  "PipelineRunRunning",
			Message: message,
		}
	}
}

func ensureBuildRunMetadata(buildRun *kudeployv1alpha1.BuildRun) bool {
	changed := false
	if buildRun.Labels == nil {
		buildRun.Labels = map[string]string{}
		changed = true
	}
	if buildRun.Labels[projectLabel] != buildRun.Namespace {
		buildRun.Labels[projectLabel] = buildRun.Namespace
		changed = true
	}
	if buildRun.Labels[managedByLabel] != managedByLabelValue {
		buildRun.Labels[managedByLabel] = managedByLabelValue
		changed = true
	}
	if controllerutil.AddFinalizer(buildRun, buildRunFinalizer) {
		changed = true
	}
	return changed
}

func buildRunServiceAccount(buildRun *kudeployv1alpha1.BuildRun) *corev1.ServiceAccount {
	secrets := make([]corev1.ObjectReference, 0, 2)
	imagePullSecrets := make([]corev1.LocalObjectReference, 0, 1)
	if buildRun.Spec.Git.SecretRef != nil && buildRun.Spec.Git.SecretRef.Name != "" {
		secrets = append(secrets, corev1.ObjectReference{Name: buildRun.Spec.Git.SecretRef.Name})
	}
	if buildRun.Spec.Image.SecretRef != nil && buildRun.Spec.Image.SecretRef.Name != "" {
		secrets = append(secrets, corev1.ObjectReference{Name: buildRun.Spec.Image.SecretRef.Name})
		imagePullSecrets = append(imagePullSecrets, corev1.LocalObjectReference{Name: buildRun.Spec.Image.SecretRef.Name})
	}

	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildRunServiceAccountNameFor(buildRun.Name),
			Namespace: buildRun.Namespace,
			Labels:    buildRunManagedLabels(buildRun),
		},
		Secrets:          secrets,
		ImagePullSecrets: imagePullSecrets,
	}
}

func buildPipelineRun(buildRun *kudeployv1alpha1.BuildRun) *tektonv1.PipelineRun {
	return &tektonv1.PipelineRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildRun.Name,
			Namespace: buildRun.Namespace,
			Labels:    buildRunManagedLabels(buildRun),
		},
		Spec: tektonv1.PipelineRunSpec{
			PipelineRef: &tektonv1.PipelineRef{
				ResolverRef: tektonv1.ResolverRef{
					Resolver: "http",
					Params: tektonv1.Params{
						tektonStringParam("url", buildPipelineURL),
					},
				},
			},
			TaskRunTemplate: tektonv1.PipelineTaskRunTemplate{
				ServiceAccountName: buildRunServiceAccountNameFor(buildRun.Name),
				PodTemplate: &tektonpod.PodTemplate{
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: ptrInt64(buildRunPodFSGroup),
					},
				},
			},
			Params: buildPipelineRunParams(buildRun),
			Workspaces: []tektonv1.WorkspaceBinding{
				{
					Name: sourceWorkspaceName,
					VolumeClaimTemplate: &corev1.PersistentVolumeClaim{
						Spec: corev1.PersistentVolumeClaimSpec{
							AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse(sourceWorkspaceSize),
								},
							},
						},
					},
				},
			},
		},
	}
}

func buildRunManagedLabels(buildRun *kudeployv1alpha1.BuildRun) map[string]string {
	return map[string]string{
		projectLabel:   buildRun.Namespace,
		buildRunLabel:  buildRun.Name,
		managedByLabel: managedByLabelValue,
	}
}

func buildPipelineRunParams(buildRun *kudeployv1alpha1.BuildRun) tektonv1.Params {
	params := tektonv1.Params{
		tektonStringParam("git-url", buildRun.Spec.Git.URL),
		tektonStringParam("image", fmt.Sprintf("%s:%s", buildRun.Spec.Image.Repository, buildRun.Spec.Image.Tag)),
		tektonStringParam("context", buildContextFor(buildRun)),
		tektonStringParam("dockerfile", dockerfileFor(buildRun)),
	}
	if buildRun.Spec.Git.Revision != "" {
		params = append(params, tektonStringParam("git-revision", buildRun.Spec.Git.Revision))
	}
	return params
}

func buildRunServiceAccountNameFor(buildRunName string) string {
	return childName(buildRunSAPrefix+buildRunName, "")
}

func tektonStringParam(name, value string) tektonv1.Param {
	return tektonv1.Param{
		Name:  name,
		Value: *tektonv1.NewStructuredValues(value),
	}
}

func buildContextFor(buildRun *kudeployv1alpha1.BuildRun) string {
	if buildRun.Spec.Context == "" {
		return defaultBuildContext
	}
	return buildRun.Spec.Context
}

func dockerfileFor(buildRun *kudeployv1alpha1.BuildRun) string {
	if buildRun.Spec.Dockerfile == "" {
		return defaultDockerfile
	}
	return buildRun.Spec.Dockerfile
}

func ptrInt64(value int64) *int64 {
	return &value
}

// SetupWithManager sets up the controller with the Manager.
func (r *BuildRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kudeployv1alpha1.BuildRun{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&tektonv1.PipelineRun{}).
		Named("buildrun").
		Complete(r)
}
