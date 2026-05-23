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
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/rand"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-controller/api/v1alpha1"
)

var _ = Describe("Project Controller", func() {
	Context("When reconciling a Project", func() {
		ctx := context.Background()

		var projectName string
		var projectKey types.NamespacedName
		var sourceNamespace string

		BeforeEach(func() {
			projectName = "project-" + rand.String(8)
			projectKey = types.NamespacedName{Name: projectName}
			sourceNamespace = "controller-" + rand.String(8)
		})

		AfterEach(func() {
			resource := &kudeployv1alpha1.Project{}
			err := k8sClient.Get(ctx, projectKey, resource)
			if err == nil {
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			} else {
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}

			namespace := &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, namespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
			} else {
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}

			namespace = &corev1.Namespace{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: sourceNamespace}, namespace)
			if err == nil {
				Expect(k8sClient.Delete(ctx, namespace)).To(Succeed())
			} else {
				Expect(errors.IsNotFound(err)).To(BeTrue())
			}
		})

		It("creates a same-name managed namespace and marks the Project ready", func() {
			project := &kudeployv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			controllerReconciler := &ProjectReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: projectKey})
			Expect(err).NotTo(HaveOccurred())

			namespace := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, namespace)).To(Succeed())
			Expect(namespace.Labels).To(HaveKeyWithValue("kudeploy.com/project", projectName))
			Expect(namespace.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))

			Expect(k8sClient.Get(ctx, projectKey, project)).To(Succeed())
			Expect(project.Status.NamespaceName).To(Equal(projectName))
			Expect(project.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", "Ready"),
				HaveField("Status", metav1.ConditionTrue),
				HaveField("Reason", "NamespaceReady"),
			)))
		})

		It("reports a conflict without taking over an unmanaged same-name namespace", func() {
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: projectName,
					Labels: map[string]string{
						"owner": "someone-else",
					},
				},
			}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())

			project := &kudeployv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			controllerReconciler := &ProjectReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: projectKey})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: projectName}, namespace)).To(Succeed())
			Expect(namespace.Labels).To(HaveKeyWithValue("owner", "someone-else"))
			Expect(namespace.Labels).NotTo(HaveKey("kudeploy.com/project"))
			Expect(namespace.Labels).NotTo(HaveKey("app.kubernetes.io/managed-by"))

			Expect(k8sClient.Get(ctx, projectKey, project)).To(Succeed())
			Expect(project.Status.NamespaceName).To(Equal(projectName))
			Expect(project.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", "Ready"),
				HaveField("Status", metav1.ConditionFalse),
				HaveField("Reason", "NamespaceConflict"),
			)))
		})

		It("syncs the configured BuildRun docker Secret into the managed namespace", func() {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: sourceNamespace},
			})).To(Succeed())
			sourceSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "image-credentials",
					Namespace: sourceNamespace,
					Labels: map[string]string{
						"registry": "ghcr",
					},
					Annotations: map[string]string{
						"kudeploy.com/source": "controller",
					},
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(`{"auths":{"ghcr.io":{}}}`),
				},
			}
			Expect(k8sClient.Create(ctx, sourceSecret)).To(Succeed())

			project := &kudeployv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			controllerReconciler := &ProjectReconciler{
				Client:                        k8sClient,
				Scheme:                        k8sClient.Scheme(),
				BuildRunDockerSecretName:      "image-credentials",
				BuildRunDockerSecretNamespace: sourceNamespace,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: projectKey})
			Expect(err).NotTo(HaveOccurred())

			syncedSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "image-credentials", Namespace: projectName}, syncedSecret)).To(Succeed())
			Expect(syncedSecret.Type).To(Equal(corev1.SecretTypeDockerConfigJson))
			Expect(syncedSecret.Data).To(Equal(sourceSecret.Data))
			Expect(syncedSecret.Labels).To(HaveKeyWithValue("registry", "ghcr"))
			Expect(syncedSecret.Labels).To(HaveKeyWithValue("kudeploy.com/project", projectName))
			Expect(syncedSecret.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
			Expect(syncedSecret.Annotations).To(HaveKeyWithValue("kudeploy.com/source", "controller"))
			Expect(syncedSecret.OwnerReferences).To(HaveLen(1))
			Expect(syncedSecret.OwnerReferences[0].Name).To(Equal(projectName))

			Expect(k8sClient.Get(ctx, projectKey, project)).To(Succeed())
			Expect(project.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", "Ready"),
				HaveField("Status", metav1.ConditionTrue),
				HaveField("Reason", "NamespaceReady"),
			)))
		})

		It("updates an existing synced BuildRun docker Secret", func() {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: sourceNamespace},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "image-credentials",
					Namespace: sourceNamespace,
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(`{"auths":{"new.example.com":{}}}`),
				},
			})).To(Succeed())

			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: projectName,
					Labels: map[string]string{
						"kudeploy.com/project":         projectName,
						"app.kubernetes.io/managed-by": "kudeploy",
					},
				},
			}
			Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "image-credentials",
					Namespace: projectName,
				},
				Type: corev1.SecretTypeDockerConfigJson,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(`{"auths":{"old.example.com":{}}}`),
				},
			})).To(Succeed())

			project := &kudeployv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			controllerReconciler := &ProjectReconciler{
				Client:                        k8sClient,
				Scheme:                        k8sClient.Scheme(),
				BuildRunDockerSecretName:      "image-credentials",
				BuildRunDockerSecretNamespace: sourceNamespace,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: projectKey})
			Expect(err).NotTo(HaveOccurred())

			syncedSecret := &corev1.Secret{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "image-credentials", Namespace: projectName}, syncedSecret)).To(Succeed())
			Expect(syncedSecret.Data).To(Equal(map[string][]byte{
				corev1.DockerConfigJsonKey: []byte(`{"auths":{"new.example.com":{}}}`),
			}))
			Expect(syncedSecret.OwnerReferences).To(HaveLen(1))
			Expect(syncedSecret.OwnerReferences[0].Name).To(Equal(projectName))
		})

		It("marks the Project not ready when the configured BuildRun docker Secret is missing", func() {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: sourceNamespace},
			})).To(Succeed())
			project := &kudeployv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			controllerReconciler := &ProjectReconciler{
				Client:                        k8sClient,
				Scheme:                        k8sClient.Scheme(),
				BuildRunDockerSecretName:      "image-credentials",
				BuildRunDockerSecretNamespace: sourceNamespace,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: projectKey})
			Expect(err).NotTo(HaveOccurred())

			syncedSecret := &corev1.Secret{}
			Expect(errors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "image-credentials", Namespace: projectName}, syncedSecret))).To(BeTrue())
			Expect(k8sClient.Get(ctx, projectKey, project)).To(Succeed())
			Expect(project.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", "Ready"),
				HaveField("Status", metav1.ConditionFalse),
				HaveField("Reason", "BuildRunDockerSecretNotFound"),
			)))
		})

		It("marks the Project not ready when the configured BuildRun docker Secret has the wrong type", func() {
			Expect(k8sClient.Create(ctx, &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: sourceNamespace},
			})).To(Succeed())
			Expect(k8sClient.Create(ctx, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "image-credentials",
					Namespace: sourceNamespace,
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					corev1.DockerConfigJsonKey: []byte(`{"auths":{"ghcr.io":{}}}`),
				},
			})).To(Succeed())

			project := &kudeployv1alpha1.Project{
				ObjectMeta: metav1.ObjectMeta{Name: projectName},
			}
			Expect(k8sClient.Create(ctx, project)).To(Succeed())

			controllerReconciler := &ProjectReconciler{
				Client:                        k8sClient,
				Scheme:                        k8sClient.Scheme(),
				BuildRunDockerSecretName:      "image-credentials",
				BuildRunDockerSecretNamespace: sourceNamespace,
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: projectKey})
			Expect(err).NotTo(HaveOccurred())

			syncedSecret := &corev1.Secret{}
			Expect(errors.IsNotFound(k8sClient.Get(ctx, types.NamespacedName{Name: "image-credentials", Namespace: projectName}, syncedSecret))).To(BeTrue())
			Expect(k8sClient.Get(ctx, projectKey, project)).To(Succeed())
			Expect(project.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", "Ready"),
				HaveField("Status", metav1.ConditionFalse),
				HaveField("Reason", "BuildRunDockerSecretInvalidType"),
				HaveField("Message", ContainSubstring(`expected "kubernetes.io/dockerconfigjson"`)),
			)))
		})
	})
})
