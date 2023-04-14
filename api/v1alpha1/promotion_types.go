/*
Copyright 2023 Thomas Stadler <thomas@thomasst.xyz>

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
	meta "k8s.io/apimachinery/pkg/api/meta"
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
	// Name is the name you want to give this copy operation.
	// E.g. "Application Version"
	// +required
	Name string `json:"name"`

	// The source path to copy from.
	// +required
	Source string `json:"source"`

	// The target path to copy to.
	// +required
	Target string `json:"target"`
}

// PromotionStatus defines the observed state of Promotion
type PromotionStatus struct {
	// ObservedGeneration is the last observed generation of the Promotion
	// object.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is a list of the current conditions of the Promotion.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// LastPullRequestURL is the URL of the pull request created by the promotion.
	// +optional
	LastPullRequestURL string `json:"lastPullRequestUrl,omitempty"`

	// LastPullRequestNumber is the number of the pull request created by the promotion.
	// +optional
	LastPullRequestNumber int `json:"lastPullRequestNumber,omitempty"`
}

const (
	// PromotionOperationSucceedReason represents the fact that the promotion operations succeeded.
	PromotionOperationSucceedReason string = "PromotionOperationSucceed"

	// PromotionOperationFailedReason represents the fact that the promotion operations failed.
	PromotionOperationFailedReason string = "PromotionOperationFailed"
)

// PromotionProgressing resets the conditions of the Promotion to metav1.Condition of
// type ReadyCondition with status 'Unknown' and ProgressingReason
// reason and message. It returns the modified Promotion.
func PromotionProgressing(promotion Promotion) Promotion {
	promotion.Status.ObservedGeneration = promotion.Generation
	promotion.Status.Conditions = []metav1.Condition{}
	newCondition := metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionUnknown,
		Reason:  ProgressingReason,
		Message: "reconciliation in progress",
	}
	meta.SetStatusCondition(promotion.GetStatusConditions(), newCondition)
	return promotion
}

// PromotionReady sets the ReadyCondition to 'True', with the given reason and message.
// It returns the modified Promotion.
func PromotionReady(promotion Promotion, reason string, message string) Promotion {
	newCondition := metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	meta.SetStatusCondition(promotion.GetStatusConditions(), newCondition)
	return promotion
}

// PromotionNotReady sets the ReadyCondition on the Promotion to 'False', with
// the given reason and message. It returns the modified Promotion.
func PromotionNotReady(promotion Promotion, reason string, message string) Promotion {
	newCondition := metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}
	meta.SetStatusCondition(promotion.GetStatusConditions(), newCondition)
	return promotion
}

// PromotionReadyMessage returns the message of the metav1.Condition of type
// ReadyCondition with status 'True' if present, or an empty string.
func PromotionReadyMessage(promotion Promotion) string {
	if c := meta.FindStatusCondition(promotion.Status.Conditions, ReadyCondition); c != nil {
		if c.Status == metav1.ConditionTrue {
			return c.Message
		}
	}
	return ""
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (in *Promotion) GetStatusConditions() *[]metav1.Condition {
	return &in.Status.Conditions
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
