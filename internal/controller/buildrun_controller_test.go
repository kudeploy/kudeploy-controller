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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-operator/api/v1alpha1"
	tektonv1 "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
)

var _ = Describe("BuildRun Controller", func() {
	const (
		namespaceName = "whoami"
		buildRunName  = "whoami-latest"
	)

	ctx := context.Background()
	buildRunKey := types.NamespacedName{Name: buildRunName, Namespace: namespaceName}

	newScheme := func() *runtime.Scheme {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(kudeployv1alpha1.AddToScheme(scheme)).To(Succeed())
		Expect(tektonv1.AddToScheme(scheme)).To(Succeed())
		return scheme
	}

	newReconciler := func(objects ...client.Object) *BuildRunReconciler {
		scheme := newScheme()
		return &BuildRunReconciler{
			Client: fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kudeployv1alpha1.BuildRun{}).
				WithObjects(objects...).
				Build(),
			Scheme: scheme,
		}
	}

	newBuildRun := func() *kudeployv1alpha1.BuildRun {
		return &kudeployv1alpha1.BuildRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:      buildRunName,
				Namespace: namespaceName,
			},
			Spec: kudeployv1alpha1.BuildRunSpec{
				Context:    "services/whoami",
				Dockerfile: "Dockerfile.prod",
				Repo: kudeployv1alpha1.BuildRunRepoSpec{
					URL:      "https://github.com/kudeploy/whoami",
					Revision: "main",
					SecretRef: &corev1.LocalObjectReference{
						Name: "repo-credentials",
					},
				},
				Image: kudeployv1alpha1.BuildRunImageSpec{
					Repository: "ghcr.io/kudeploy/whoami",
					Tag:        "latest",
					SecretRef: &corev1.LocalObjectReference{
						Name: "image-credentials",
					},
				},
			},
		}
	}

	newSecret := func(name string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespaceName,
			},
		}
	}

	It("creates a dedicated ServiceAccount and deterministic PipelineRun", func() {
		buildRun := newBuildRun()
		reconciler := newReconciler(
			buildRun,
			newSecret("repo-credentials"),
			newSecret("image-credentials"),
		)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: buildRunKey})
		Expect(err).NotTo(HaveOccurred())

		serviceAccount := &corev1.ServiceAccount{}
		Expect(reconciler.Get(ctx, types.NamespacedName{Name: "buildrun-" + buildRunName, Namespace: namespaceName}, serviceAccount)).To(Succeed())
		Expect(serviceAccount.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(serviceAccount.Labels).To(HaveKeyWithValue("kudeploy.com/buildrun", buildRunName))
		Expect(serviceAccount.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(serviceAccount.Secrets).To(ConsistOf(
			corev1.ObjectReference{Name: "repo-credentials"},
			corev1.ObjectReference{Name: "image-credentials"},
		))
		Expect(serviceAccount.ImagePullSecrets).To(ConsistOf(
			corev1.LocalObjectReference{Name: "image-credentials"},
		))
		Expect(serviceAccount.OwnerReferences).To(HaveLen(1))
		Expect(serviceAccount.OwnerReferences[0].Name).To(Equal(buildRunName))

		pipelineRun := &tektonv1.PipelineRun{}
		Expect(reconciler.Get(ctx, buildRunKey, pipelineRun)).To(Succeed())
		Expect(pipelineRun.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(pipelineRun.Labels).To(HaveKeyWithValue("kudeploy.com/buildrun", buildRunName))
		Expect(pipelineRun.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(pipelineRun.Spec.PipelineRef.Resolver).To(Equal(tektonv1.ResolverName("http")))
		Expect(pipelineRun.Spec.PipelineRef.Params).To(ConsistOf(
			tektonStringParam("url", "https://raw.githubusercontent.com/kudeploy/kudeploy-manifests/main/tekton/pipelines/build-and-push.yaml"),
		))
		Expect(pipelineRun.Spec.TaskRunTemplate.ServiceAccountName).To(Equal("buildrun-" + buildRunName))
		Expect(pipelineRun.Spec.TaskRunTemplate.PodTemplate.SecurityContext.FSGroup).To(Equal(ptrInt64(65532)))
		Expect(pipelineRun.Spec.Params).To(ConsistOf(
			tektonStringParam("git-url", "https://github.com/kudeploy/whoami"),
			tektonStringParam("git-revision", "main"),
			tektonStringParam("image", "ghcr.io/kudeploy/whoami:latest"),
			tektonStringParam("context", "services/whoami"),
			tektonStringParam("dockerfile", "Dockerfile.prod"),
		))
		Expect(pipelineRun.Spec.Workspaces).To(HaveLen(1))
		Expect(pipelineRun.Spec.Workspaces[0].Name).To(Equal("source"))
		Expect(pipelineRun.Spec.Workspaces[0].VolumeClaimTemplate.Spec.AccessModes).To(ConsistOf(corev1.ReadWriteOnce))
		Expect(*pipelineRun.Spec.Workspaces[0].VolumeClaimTemplate.Spec.Resources.Requests.Storage()).To(Equal(resource.MustParse("1Gi")))
		Expect(pipelineRun.OwnerReferences).To(HaveLen(1))
		Expect(pipelineRun.OwnerReferences[0].Name).To(Equal(buildRunName))

		Expect(reconciler.Get(ctx, buildRunKey, buildRun)).To(Succeed())
		Expect(buildRun.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(buildRun.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(buildRun.Status.PipelineRunName).To(Equal(buildRunName))
		Expect(buildRun.Status.ServiceAccountName).To(Equal("buildrun-" + buildRunName))
		Expect(buildRun.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"),
			HaveField("Status", metav1.ConditionTrue),
			HaveField("Reason", "PipelineRunCreated"),
		)))
	})

	It("does not create runtime resources when a referenced Secret is missing", func() {
		buildRun := newBuildRun()
		reconciler := newReconciler(buildRun, newSecret("repo-credentials"))

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: buildRunKey})
		Expect(err).NotTo(HaveOccurred())

		serviceAccount := &corev1.ServiceAccount{}
		Expect(errors.IsNotFound(reconciler.Get(ctx, types.NamespacedName{Name: "buildrun-" + buildRunName, Namespace: namespaceName}, serviceAccount))).To(BeTrue())

		pipelineRun := &tektonv1.PipelineRun{}
		Expect(errors.IsNotFound(reconciler.Get(ctx, buildRunKey, pipelineRun))).To(BeTrue())

		Expect(reconciler.Get(ctx, buildRunKey, buildRun)).To(Succeed())
		Expect(buildRun.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", "SecretNotFound"),
		)))
	})

	It("deletes the generated ServiceAccount and PipelineRun during finalization", func() {
		now := metav1.Now()
		buildRun := newBuildRun()
		buildRun.Finalizers = []string{buildRunFinalizer}
		buildRun.DeletionTimestamp = &now

		serviceAccount := buildServiceAccount(buildRun)
		pipelineRun := buildPipelineRun(buildRun)
		reconciler := newReconciler(buildRun, serviceAccount, pipelineRun)

		_, err := reconciler.reconcileDelete(ctx, buildRun)
		Expect(err).NotTo(HaveOccurred())

		Expect(errors.IsNotFound(reconciler.Get(ctx, types.NamespacedName{Name: serviceAccount.Name, Namespace: namespaceName}, &corev1.ServiceAccount{}))).To(BeTrue())
		Expect(errors.IsNotFound(reconciler.Get(ctx, buildRunKey, &tektonv1.PipelineRun{}))).To(BeTrue())
	})
})
