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

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ServiceSpec defines the desired state of Service.
type ServiceSpec struct {
	// image is the container image to run.
	// +required
	Image string `json:"image"`

	// ports describe the network ports exposed by this Service.
	// +required
	// +listType=atomic
	Ports []ServicePort `json:"ports"`
}

// ServicePort describes one exposed Service port.
type ServicePort struct {
	// port is the stable Kubernetes Service port.
	// +required
	Port int32 `json:"port"`

	// targetPort is the container port. When omitted, port is used.
	// +optional
	TargetPort int32 `json:"targetPort,omitempty"`
}

// ServiceStatus defines the observed state of Service.
type ServiceStatus struct {
	// observedGeneration is the latest metadata.generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// latestVersion is the newest version created for this Service.
	// +optional
	LatestVersion int64 `json:"latestVersion,omitempty"`

	// latestDeploymentName is the newest Kudeploy Deployment created for this Service.
	// +optional
	LatestDeploymentName string `json:"latestDeploymentName,omitempty"`

	// serviceAccountName is the runtime ServiceAccount used by this Service's Deployments.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// activeVersion is the version currently receiving traffic.
	// +optional
	ActiveVersion int64 `json:"activeVersion,omitempty"`

	// activeDeploymentName is the Kudeploy Deployment currently receiving traffic.
	// +optional
	ActiveDeploymentName string `json:"activeDeploymentName,omitempty"`

	// conditions represent the current state of the Service resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Service is the Schema for the services API.
type Service struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of Service.
	// +required
	Spec ServiceSpec `json:"spec"`

	// status defines the observed state of Service.
	// +optional
	Status ServiceStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ServiceList contains a list of Service.
type ServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []Service `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Service{}, &ServiceList{})
}
