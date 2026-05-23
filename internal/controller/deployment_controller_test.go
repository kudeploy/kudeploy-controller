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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kudeployv1alpha1 "github.com/kudeploy/kudeploy-controller/api/v1alpha1"
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
			&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "service-whoami-env",
					Namespace: namespaceName,
				},
				Data: map[string][]byte{
					"TOKEN": []byte("secret"),
				},
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
				Replicas:           ptrInt32(0),
				Image:              "ghcr.io/kudeploy/whoami:latest",
				ImageSecretRef:     &corev1.LocalObjectReference{Name: "registry-credentials"},
				Command:            []string{"/whoami"},
				Args:               []string{"--port=8080"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
				},
				Env: []corev1.EnvVar{
					{Name: "LOG_LEVEL", Value: "debug"},
				},
				EnvFrom: []corev1.EnvFromSource{
					{
						ConfigMapRef: &corev1.ConfigMapEnvSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: "whoami-config"},
						},
					},
				},
				Ports: []kudeployv1alpha1.ServicePort{
					{Port: 80, TargetPort: 8080},
				},
				ReadinessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/ready",
							Port: intstr.FromInt32(8080),
						},
					},
				},
				LivenessProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/live",
							Port: intstr.FromInt32(8080),
						},
					},
				},
				StartupProbe: &corev1.Probe{
					ProbeHandler: corev1.ProbeHandler{
						HTTPGet: &corev1.HTTPGetAction{
							Path: "/startup",
							Port: intstr.FromInt32(8080),
						},
					},
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
		Expect(kubernetesDeployment.Spec.Replicas).To(Equal(ptrInt32(0)))
		Expect(kubernetesDeployment.Spec.RevisionHistoryLimit).To(Equal(ptrInt32(0)))
		Expect(kubernetesDeployment.Spec.Strategy.Type).To(Equal(appsv1.RollingUpdateDeploymentStrategyType))
		Expect(kubernetesDeployment.Spec.Strategy.RollingUpdate).NotTo(BeNil())
		Expect(kubernetesDeployment.Spec.Strategy.RollingUpdate.MaxUnavailable.IntVal).To(Equal(int32(0)))
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
		Expect(kubernetesDeployment.Spec.Template.Spec.ImagePullSecrets).To(ConsistOf(corev1.LocalObjectReference{Name: "registry-credentials"}))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Command).To(Equal([]string{"/whoami"}))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Args).To(Equal([]string{"--port=8080"}))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Resources.Requests.Cpu().String()).To(Equal("50m"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Resources.Requests.Memory().String()).To(Equal("64Mi"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String()).To(Equal("250m"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Resources.Limits.Memory().String()).To(Equal("128Mi"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Env).To(ConsistOf(corev1.EnvVar{Name: "LOG_LEVEL", Value: "debug"}))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].EnvFrom).To(ConsistOf(
			corev1.EnvFromSource{
				ConfigMapRef: &corev1.ConfigMapEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "whoami-config"},
				},
			},
			corev1.EnvFromSource{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{Name: "whoami-00001-env"},
				},
			},
		))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Ports).To(ConsistOf(corev1.ContainerPort{ContainerPort: 8080}))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].ReadinessProbe.HTTPGet.Path).To(Equal("/ready"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].LivenessProbe.HTTPGet.Path).To(Equal("/live"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].StartupProbe.HTTPGet.Path).To(Equal("/startup"))
		Expect(kubernetesDeployment.OwnerReferences).To(HaveLen(1))
		Expect(kubernetesDeployment.OwnerReferences[0].Name).To(Equal(deploymentName))

		envSecret := &corev1.Secret{}
		Expect(reconciler.Get(ctx, types.NamespacedName{Name: "whoami-00001-env", Namespace: namespaceName}, envSecret)).To(Succeed())
		Expect(envSecret.Data).To(Equal(map[string][]byte{"TOKEN": []byte("secret")}))
		Expect(envSecret.Labels).To(HaveKeyWithValue("kudeploy.com/project", namespaceName))
		Expect(envSecret.Labels).To(HaveKeyWithValue("kudeploy.com/service", serviceName))
		Expect(envSecret.Labels).To(HaveKeyWithValue("kudeploy.com/deployment", deploymentName))
		Expect(envSecret.Labels).To(HaveKeyWithValue("app.kubernetes.io/managed-by", "kudeploy"))
		Expect(envSecret.OwnerReferences).To(HaveLen(1))
		Expect(envSecret.OwnerReferences[0].Name).To(Equal(deploymentName))

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

	It("preserves selector and external metadata on existing Kubernetes Deployments while applying desired replicas", func() {
		deployment := newDeployment()
		existing := buildKubernetesDeployment(deployment)
		existing.Labels["team"] = "platform"
		existing.Annotations = map[string]string{
			"kubectl.kubernetes.io/restartedAt": "2026-05-16T00:00:00Z",
		}
		existing.Spec.Replicas = ptrInt32(3)
		existing.Spec.Selector.MatchLabels = map[string]string{
			"existing-selector": "kept",
		}
		existing.Spec.Template.Labels = map[string]string{
			deploymentLabel:           deploymentName,
			"sidecar.istio.io/inject": "true",
		}
		existing.Spec.Template.Annotations = map[string]string{
			"kubectl.kubernetes.io/restartedAt": "2026-05-16T00:00:00Z",
		}

		deployment.Spec.Image = "ghcr.io/kudeploy/whoami:v2"
		reconciler := newReconciler(deployment, existing)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
		Expect(err).NotTo(HaveOccurred())

		kubernetesDeployment := &appsv1.Deployment{}
		Expect(reconciler.Get(ctx, deploymentKey, kubernetesDeployment)).To(Succeed())
		Expect(kubernetesDeployment.Labels).To(HaveKeyWithValue("team", "platform"))
		Expect(kubernetesDeployment.Labels).To(HaveKeyWithValue(deploymentLabel, deploymentName))
		Expect(kubernetesDeployment.Annotations).To(HaveKeyWithValue("kubectl.kubernetes.io/restartedAt", "2026-05-16T00:00:00Z"))
		Expect(kubernetesDeployment.Spec.Replicas).To(Equal(ptrInt32(0)))
		Expect(kubernetesDeployment.Spec.Selector.MatchLabels).To(Equal(map[string]string{
			"existing-selector": "kept",
		}))
		Expect(kubernetesDeployment.Spec.Template.Labels).To(HaveKeyWithValue("sidecar.istio.io/inject", "true"))
		Expect(kubernetesDeployment.Spec.Template.Labels).To(HaveKeyWithValue(deploymentLabel, deploymentName))
		Expect(kubernetesDeployment.Spec.Template.Annotations).To(HaveKeyWithValue("kubectl.kubernetes.io/restartedAt", "2026-05-16T00:00:00Z"))
		Expect(kubernetesDeployment.Spec.Template.Spec.Containers[0].Image).To(Equal("ghcr.io/kudeploy/whoami:v2"))
	})

	It("defaults Kubernetes Deployment replicas to one", func() {
		deployment := newDeployment()
		deployment.Spec.Replicas = nil
		reconciler := newReconciler(deployment)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
		Expect(err).NotTo(HaveOccurred())

		kubernetesDeployment := &appsv1.Deployment{}
		Expect(reconciler.Get(ctx, deploymentKey, kubernetesDeployment)).To(Succeed())
		Expect(kubernetesDeployment.Spec.Replicas).To(Equal(ptrInt32(1)))
	})

	It("does not overwrite an existing env Secret clone", func() {
		deployment := newDeployment()
		clone := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "whoami-00001-env",
				Namespace: namespaceName,
			},
			Data: map[string][]byte{"TOKEN": []byte("old")},
		}
		reconciler := newReconciler(deployment, clone)

		_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: deploymentKey})
		Expect(err).NotTo(HaveOccurred())

		envSecret := &corev1.Secret{}
		Expect(reconciler.Get(ctx, types.NamespacedName{Name: "whoami-00001-env", Namespace: namespaceName}, envSecret)).To(Succeed())
		Expect(envSecret.Data).To(Equal(map[string][]byte{"TOKEN": []byte("old")}))
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
