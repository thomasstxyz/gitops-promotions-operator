/*
Copyright 2023.

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

// PromotionSpec defines the desired state of Promotion
type PromotionSpec struct {
	// The source environment to promote from.
	// +required
	SourceEnvironmentRef *corev1.LocalObjectReference `json:"sourceEnvironmentRef"`

	// The target environment to promote to.
	// +required
	TargetEnvironmentRef *corev1.LocalObjectReference `json:"targetEnvironmentRef"`

	// Copy defines a list of copy operations to perform.
	// +required
	Copy []CopyOperation `json:"copy"`

	// Strategy defines the strategy to use when promoting.
	// +required
	// +kubebuilder:validation:Enum=pull-request
	Strategy string `json:"strategy"`
}

// CopyOperation defines a file/directory copy operation.
type CopyOperation struct {
	// The source path to copy from.
	// +required
	Source string `json:"source"`

	// The target path to copy to.
	// +required
	Target string `json:"target"`
}

// PromotionStatus defines the observed state of Promotion
type PromotionStatus struct {
	// ObservedGeneration is the last observed generation of the Environment
	// object.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is a list of the current conditions of the Environment.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// PullRequestURL is the URL of the pull request created by the promotion.
	// +optional
	PullRequestURL string `json:"pullRequestUrl,omitempty"`

	// PullRequestNumber is the number of the pull request created by the promotion.
	// +optional
	PullRequestNumber int `json:"pullRequestNumber,omitempty"`

	// PullRequestBranch is the branch of the pull request created by the promotion.
	// +optional
	PullRequestBranch string `json:"pullRequestBranch,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Promotion is the Schema for the promotions API
type Promotion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PromotionSpec   `json:"spec,omitempty"`
	Status PromotionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// PromotionList contains a list of Promotion
type PromotionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Promotion `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Promotion{}, &PromotionList{})
}
