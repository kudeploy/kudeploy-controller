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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeploymentSpec defines the desired state of Deployment.
type DeploymentSpec struct {
	// serviceName is the owning Kudeploy Service name.
	// +required
	ServiceName string `json:"serviceName"`

	// version is the monotonically increasing Service version represented by this Deployment.
	// +required
	Version int64 `json:"version"`

	// serviceAccountName is the runtime ServiceAccount used by the generated Kubernetes Deployment.
	// +required
	ServiceAccountName string `json:"serviceAccountName"`

	// replicas is the desired number of instances captured for this Deployment version.
	// When omitted, 1 is used. Set to 0 to scale this Deployment to zero.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// image is the container image to run.
	// +required
	Image string `json:"image"`

	// imageSecretRef references an optional Secret in the same namespace used for pulling the image.
	// +optional
	ImageSecretRef *corev1.LocalObjectReference `json:"imageSecretRef,omitempty"`

	// command overrides the container entrypoint captured for this Deployment version.
	// +optional
	// +listType=atomic
	Command []string `json:"command,omitempty"`

	// args overrides the container arguments captured for this Deployment version.
	// +optional
	// +listType=atomic
	Args []string `json:"args,omitempty"`

	// resources describes compute resource requests and limits captured for this Deployment version.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// ports describe the network ports exposed by this Deployment.
	// +required
	// +listType=atomic
	Ports []ServicePort `json:"ports"`

	// env describes plain Kubernetes container environment variables captured for this Deployment version.
	// +optional
	// +listType=map
	// +listMapKey=name
	Env []corev1.EnvVar `json:"env,omitempty"`

	// envFrom describes sources used to populate container environment variables captured for this Deployment version.
	// The Service env Secret clone maintained by the controller is added automatically to the Kubernetes Deployment.
	// +optional
	// +listType=atomic
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`

	// readinessProbe describes how Kubernetes determines whether the container is ready to receive traffic.
	// +optional
	ReadinessProbe *corev1.Probe `json:"readinessProbe,omitempty"`

	// livenessProbe describes how Kubernetes determines whether the container should be restarted.
	// +optional
	LivenessProbe *corev1.Probe `json:"livenessProbe,omitempty"`

	// startupProbe describes how Kubernetes determines whether the container has started.
	// +optional
	StartupProbe *corev1.Probe `json:"startupProbe,omitempty"`
}

// DeploymentStatus defines the observed state of Deployment.
type DeploymentStatus struct {
	// kubernetesDeploymentName is the apps/v1 Deployment managed for this Kudeploy Deployment.
	// +optional
	KubernetesDeploymentName string `json:"kubernetesDeploymentName,omitempty"`

	// conditions represent the current state of the Deployment resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Deployment is the Schema for the deployments API.
type Deployment struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Deployment.
	// +required
	Spec DeploymentSpec `json:"spec"`

	// status defines the observed state of Deployment.
	// +optional
	Status DeploymentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// DeploymentList contains a list of Deployment.
type DeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Deployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Deployment{}, &DeploymentList{})
}
