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

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// BuildRunSpec defines the desired state of BuildRun
type BuildRunSpec struct {
	// context is the build context path within the checked-out repository.
	// Defaults to ".".
	// +optional
	Context string `json:"context,omitempty"`

	// dockerfile is the Dockerfile path relative to the build context.
	// Defaults to "./Dockerfile".
	// +optional
	Dockerfile string `json:"dockerfile,omitempty"`

	// git describes the Git repository to build.
	// +required
	Git BuildRunGitSpec `json:"git"`

	// image describes the image destination for the build output.
	// +required
	Image BuildRunImageSpec `json:"image"`
}

// BuildRunGitSpec describes the Git source for a build.
type BuildRunGitSpec struct {
	// url is the Git repository URL.
	// +required
	URL string `json:"url"`

	// revision is the Git revision to build, such as a branch, tag, or commit SHA.
	// When omitted, the Git repository's default branch is used.
	// +optional
	Revision string `json:"revision,omitempty"`

	// secretRef references an optional Secret in the same namespace used for Git authentication.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// BuildRunImageSpec describes the image destination for a build.
type BuildRunImageSpec struct {
	// repository is the image repository without a tag.
	// +required
	Repository string `json:"repository"`

	// tag is the image tag to push.
	// +required
	Tag string `json:"tag"`

	// secretRef references an optional Secret in the same namespace used for registry authentication.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// BuildRunStatus defines the observed state of BuildRun.
type BuildRunStatus struct {
	// pipelineRunName is the deterministic Tekton PipelineRun name for this BuildRun.
	// +optional
	PipelineRunName string `json:"pipelineRunName,omitempty"`

	// serviceAccountName is the dedicated ServiceAccount name used by the PipelineRun.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// conditions represent the current state of the BuildRun resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// BuildRun is the Schema for the buildruns API
type BuildRun struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of BuildRun
	// +required
	Spec BuildRunSpec `json:"spec"`

	// status defines the observed state of BuildRun
	// +optional
	Status BuildRunStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// BuildRunList contains a list of BuildRun
type BuildRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []BuildRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BuildRun{}, &BuildRunList{})
}
