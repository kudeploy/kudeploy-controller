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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-operator/api/v1alpha1"
)

var _ = Describe("Service Controller", func() {
	const (
		namespaceName = "whoami"
		serviceName   = "whoami"
	)

	ctx := context.Background()
	serviceKey := types.NamespacedName{Name: serviceName, Namespace: namespaceName}

	newScheme := func() *runtime.Scheme {
		scheme := runtime.NewScheme()
		Expect(corev1.AddToScheme(scheme)).To(Succeed())
		Expect(kudeployv1alpha1.AddToScheme(scheme)).To(Succeed())
		return scheme
	}

	newReconciler := func(objects ...client.Object) *ServiceReconciler {
		scheme := newScheme()
		return &ServiceReconciler{
			Client: fake.NewClientBuilder().
				WithScheme(scheme).
				WithStatusSubresource(&kudeployv1alpha1.Service{}, &kudeployv1alpha1.Deployment{}).
				WithObjects(objects...).
				Build(),
			Scheme: scheme,
		}
	}

	newService := func() *kudeployv1alpha1.Service {
		return &kudeployv1alpha1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:       serviceName,
				Namespace:  namespaceName,
				Generation: 1,
			},
			Spec: kudeployv1alpha1.ServiceSpec{
				Image: "ghcr.io/kudeploy/whoami:latest",
				Ports: []kudeployv1alpha1.ServicePort{
					{Port: 80, TargetPort: 8080},
				},
			},
		}
	}

	It("creates the first versioned Kudeploy Deployment and a stable Kubernetes Service", func() {
		service := newService()
		reconciler := newReconciler(service)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: serviceKey})
		Expect(err).NotTo(HaveOccurred())

		kudeployDeployment := &kudeployv1alpha1.Deployment{}
		Expect(reconciler.Get(ctx, types.NamespacedName{Name: "whoami-00001", Namespace: namespaceName}, kudeployDeployment)).To(Succeed())
		Expect(kudeployDeployment.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(kudeployDeployment.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(kudeployDeployment.Labels).To(HaveKeyWithValue("kudeploy.com/deployment", "whoami-00001"))
		Expect(kudeployDeployment.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(kudeployDeployment.Spec.ServiceName).To(Equal(serviceName))
		Expect(kudeployDeployment.Spec.Version).To(Equal(int64(1)))
		Expect(kudeployDeployment.Spec.ServiceAccountName).To(Equal("service-whoami"))
		Expect(kudeployDeployment.Spec.Image).To(Equal("ghcr.io/kudeploy/whoami:latest"))
		Expect(kudeployDeployment.Spec.Ports).To(ConsistOf(kudeployv1alpha1.ServicePort{Port: 80, TargetPort: 8080}))

		kubernetesService := &corev1.Service{}
		Expect(reconciler.Get(ctx, serviceKey, kubernetesService)).To(Succeed())
		Expect(kubernetesService.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(kubernetesService.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(kubernetesService.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(kubernetesService.Spec.Ports).To(HaveLen(1))
		Expect(kubernetesService.Spec.Ports[0].Port).To(Equal(int32(80)))
		Expect(kubernetesService.Spec.Ports[0].TargetPort.IntVal).To(Equal(int32(8080)))
		Expect(kubernetesService.Spec.Selector).To(BeNil())

		serviceAccount := &corev1.ServiceAccount{}
		Expect(reconciler.Get(ctx, types.NamespacedName{Name: "service-whoami", Namespace: namespaceName}, serviceAccount)).To(Succeed())
		Expect(serviceAccount.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(serviceAccount.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(serviceAccount.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(serviceAccount.OwnerReferences).To(HaveLen(1))
		Expect(serviceAccount.OwnerReferences[0].Name).To(Equal(serviceName))

		Expect(reconciler.Get(ctx, serviceKey, service)).To(Succeed())
		Expect(service.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(service.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(service.Status.ObservedGeneration).To(Equal(int64(1)))
		Expect(service.Status.LatestVersion).To(Equal(int64(1)))
		Expect(service.Status.LatestDeploymentName).To(Equal("whoami-00001"))
		Expect(service.Status.ServiceAccountName).To(Equal("service-whoami"))
		Expect(service.Status.ActiveVersion).To(Equal(int64(0)))
		Expect(service.Status.ActiveDeploymentName).To(BeEmpty())
		Expect(service.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", "DeploymentProgressing"),
		)))
	})

	It("switches the stable Kubernetes Service selector after the latest Kudeploy Deployment is ready", func() {
		service := newService()
		service.Labels = map[string]string{
			projectLabel:   namespaceName,
			managedByLabel: managedByLabelValue,
		}
		service.Status.ObservedGeneration = 1
		service.Status.LatestVersion = 1
		service.Status.LatestDeploymentName = "whoami-00001"

		kudeployDeployment := &kudeployv1alpha1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "whoami-00001",
				Namespace: namespaceName,
				Labels:    deploymentManagedLabels(namespaceName, serviceName, "whoami-00001"),
			},
			Spec: kudeployv1alpha1.DeploymentSpec{
				ServiceName: serviceName,
				Version:     1,
				Image:       "ghcr.io/kudeploy/whoami:latest",
				Ports:       []kudeployv1alpha1.ServicePort{{Port: 80, TargetPort: 8080}},
			},
			Status: kudeployv1alpha1.DeploymentStatus{
				KubernetesDeploymentName: "whoami-00001",
				Conditions: []metav1.Condition{
					{
						Type:               "Ready",
						Status:             metav1.ConditionTrue,
						Reason:             "KubernetesDeploymentAvailable",
						LastTransitionTime: metav1.Now(),
					},
				},
			},
		}
		kubernetesService := stableKubernetesService(service, nil)
		reconciler := newReconciler(service, kudeployDeployment, kubernetesService)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: serviceKey})
		Expect(err).NotTo(HaveOccurred())

		Expect(reconciler.Get(ctx, serviceKey, kubernetesService)).To(Succeed())
		Expect(kubernetesService.Spec.Selector).To(Equal(map[string]string{
			deploymentLabel: "whoami-00001",
		}))

		Expect(reconciler.Get(ctx, serviceKey, service)).To(Succeed())
		Expect(service.Status.ActiveVersion).To(Equal(int64(1)))
		Expect(service.Status.ActiveDeploymentName).To(Equal("whoami-00001"))
		Expect(service.Status.Conditions).To(ContainElement(SatisfyAll(
			HaveField("Type", "Ready"),
			HaveField("Status", metav1.ConditionTrue),
			HaveField("Reason", "DeploymentReady"),
		)))
	})

	It("creates a new version when Service spec changes and keeps traffic on the previous active version", func() {
		service := newService()
		service.Generation = 2
		service.Status.ObservedGeneration = 1
		service.Status.LatestVersion = 1
		service.Status.LatestDeploymentName = "whoami-00001"
		service.Status.ActiveVersion = 1
		service.Status.ActiveDeploymentName = "whoami-00001"
		service.Spec.Image = "ghcr.io/kudeploy/whoami:v2"

		kubernetesService := stableKubernetesService(service, map[string]string{
			deploymentLabel: "whoami-00001",
		})
		reconciler := newReconciler(service, kubernetesService)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: serviceKey})
		Expect(err).NotTo(HaveOccurred())

		newDeployment := &kudeployv1alpha1.Deployment{}
		Expect(reconciler.Get(ctx, types.NamespacedName{Name: "whoami-00002", Namespace: namespaceName}, newDeployment)).To(Succeed())
		Expect(newDeployment.Spec.Version).To(Equal(int64(2)))
		Expect(newDeployment.Spec.Image).To(Equal("ghcr.io/kudeploy/whoami:v2"))

		Expect(reconciler.Get(ctx, serviceKey, kubernetesService)).To(Succeed())
		Expect(kubernetesService.Spec.Selector).To(Equal(map[string]string{
			deploymentLabel: "whoami-00001",
		}))

		Expect(reconciler.Get(ctx, serviceKey, service)).To(Succeed())
		Expect(service.Status.ObservedGeneration).To(Equal(int64(2)))
		Expect(service.Status.LatestVersion).To(Equal(int64(2)))
		Expect(service.Status.LatestDeploymentName).To(Equal("whoami-00002"))
		Expect(service.Status.ActiveVersion).To(Equal(int64(1)))
		Expect(service.Status.ActiveDeploymentName).To(Equal("whoami-00001"))
	})

	It("ignores deleted Services", func() {
		reconciler := newReconciler()

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: serviceKey})
		Expect(err).NotTo(HaveOccurred())

		service := &kudeployv1alpha1.Service{}
		Expect(apierrors.IsNotFound(reconciler.Get(ctx, serviceKey, service))).To(BeTrue())
	})

	It("does not recreate runtime resources while the Service is deleting", func() {
		now := metav1.Now()
		service := newService()
		service.Finalizers = []string{"test.kudeploy.com/finalizer"}
		service.DeletionTimestamp = &now
		reconciler := newReconciler(service)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: serviceKey})
		Expect(err).NotTo(HaveOccurred())

		kubernetesService := &corev1.Service{}
		Expect(apierrors.IsNotFound(reconciler.Get(ctx, serviceKey, kubernetesService))).To(BeTrue())

		serviceAccount := &corev1.ServiceAccount{}
		Expect(apierrors.IsNotFound(reconciler.Get(ctx, types.NamespacedName{Name: "service-whoami", Namespace: namespaceName}, serviceAccount))).To(BeTrue())
	})

	It("keeps generated version names deterministic and within the Kubernetes name limit", func() {
		Expect(serviceVersionName("whoami", 1)).To(Equal("whoami-00001"))
		Expect(serviceVersionName("whoami", 100000)).To(Equal("whoami-100000"))
		Expect(runtimeServiceAccountNameFor("whoami")).To(Equal("service-whoami"))

		longName := "this-is-a-very-long-service-name-that-still-needs-versioned-deployments"
		generatedName := serviceVersionName(longName, 1)
		Expect(generatedName).To(HaveLen(63))
		Expect(generatedName).To(HaveSuffix("-00001"))
		Expect(generatedName).NotTo(Equal(longName + "-00001"))

		generatedServiceAccountName := runtimeServiceAccountNameFor(longName)
		Expect(generatedServiceAccountName).To(HaveLen(63))
		Expect(generatedServiceAccountName).To(HavePrefix("service-"))
	})
})
