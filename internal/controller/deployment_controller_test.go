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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-operator/api/v1alpha1"
)

var _ = Describe("Deployment Controller", func() {
	const (
		namespaceName  = "whoami"
		serviceName    = "whoami"
		deploymentName = "whoami-00001"
	)

	ctx := context.Background()
	deploymentKey := types.NamespacedName{Name: deploymentName, Namespace: namespaceName}

	newScheme := func() *runtime.Scheme {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(appsv1.AddToScheme(scheme)).To(Succeed())
		Expect(kudeployv1alpha1.AddToScheme(scheme)).To(Succeed())
		return scheme
	}

	newReconciler := func(objects ...client.Object) *DeploymentReconciler {
		scheme := newScheme()
		objects = append([]client.Object{
			&corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{Name: namespaceName},
			},
		}, objects...)
		return &DeploymentReconciler{
			Client: fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kudeployv1alpha1.Deployment{}).
				WithObjects(objects...).
				Build(),
			Scheme: scheme,
		}
	}

	newDeployment := func() *kudeployv1alpha1.Deployment {
		return &kudeployv1alpha1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      deploymentName,
				Namespace: namespaceName,
			},
			Spec: kudeployv1alpha1.DeploymentSpec{
				ServiceName:        serviceName,
				Version:            1,
				ServiceAccountName: "service-whoami",
				Image:              "ghcr.io/kudeploy/whoami:latest",
				Ports: []kudeployv1alpha1.ServicePort{
					{Port: 80, TargetPort: 8080},
				},
			},
		}
	}

	It("creates one matching Kubernetes Deployment for the Kudeploy Deployment", func() {
		deployment := newDeployment()
		reconciler := newReconciler(deployment)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
		Expect(err).NotTo(HaveOccurred())

		kubernetesDeployment := &appsv1.Deployment{}
		Expect(reconciler.Get(ctx, deploymentKey, kubernetesDeployment)).To(Succeed())
		Expect(kubernetesDeployment.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(kubernetesDeployment.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(kubernetesDeployment.Labels).To(HaveKeyWithValue("kudeploy.com/deployment", deploymentName))
		Expect(kubernetesDeployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(kubernetesDeployment.Spec.Replicas).To(Equal(ptrInt32(1)))
		Expect(kubernetesDeployment.Spec.Selector.MatchLabels).To(Equal(map[string]string{
			deploymentLabel: deploymentName,
		}))
		Expect(kubernetesDeployment.Spec.Template.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(kubernetesDeployment.Spec.Template.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(kubernetesDeployment.Spec.Template.Labels).To(HaveKeyWithValue("kudeploy.com/deployment", deploymentName))
		Expect(kubernetesDeployment.Spec.Template.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(kubernetesDeployment.Spec.Template.Spec.ServiceAccountName).To(Equal("service-whoami"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Name).To(Equal(serviceName))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/kudeploy/whoami:latest"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].ImagePullPolicy).To(Equal(corev1.PullAlways))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Ports).To(ConsistOf(corev1.ContainerPort{ContainerPort: 8080}))
		Expect(kubernetesDeployment.OwnerReferences).To(HaveLen(1))
		Expect(kubernetesDeployment.OwnerReferences[0].Name).To(Equal(deploymentName))

		Expect(reconciler.Get(ctx, deploymentKey, deployment)).To(Succeed())
		Expect(deployment.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(deployment.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(deployment.Labels).To(HaveKeyWithValue("kudeploy.com/deployment", deploymentName))
		Expect(deployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(deployment.Status.KubernetesDeploymentName).To(Equal(deploymentName))
		Expect(deployment.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", "KubernetesDeploymentProgressing"),
		)))
	})

	It("marks the Kudeploy Deployment ready when the Kubernetes Deployment is available", func() {
		deployment := newDeployment()
		kubernetesDeployment := buildKubernetesDeployment(deployment)
		kubernetesDeployment.Status.Conditions = []appsv1.DeploymentCondition{
			{
				Type:   appsv1.DeploymentAvailable,
				Status: corev1.ConditionTrue,
				Reason: "MinimumReplicasAvailable",
			},
		}
		reconciler := newReconciler(deployment, kubernetesDeployment)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(reconciler.Get(ctx, deploymentKey, deployment)).To(Succeed())
		Expect(deployment.Status.KubernetesDeploymentName).To(Equal(deploymentName))
		Expect(deployment.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"),
			HaveField("Status", metav1.ConditionTrue),
			HaveField("Reason", "KubernetesDeploymentAvailable"),
		)))
	})

	It("does not recreate Kubernetes resources while the Kudeploy Deployment is deleting", func() {
		now := metav1.Now()
		deployment := newDeployment()
		deployment.Finalizers = []string{"test.kudeploy.com/finalizer"}
		deployment.DeletionTimestamp = &now
		reconciler := newReconciler(deployment)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
		Expect(err).NotTo(HaveOccurred())

		kubernetesDeployment := &appsv1.Deployment{}
		Expect(apierrors.IsNotFound(reconciler.Get(ctx, deploymentKey, kubernetesDeployment))).To(BeTrue())
	})
})
