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

const (
	GitProviderGitHub string = "github"
)

// EnvironmentSpec defines the desired state of Environment
type EnvironmentSpec struct {
	// Path is the filesystem path to the environment directory
	// relative from the root of the source repository.
	// Defaults to the root of the repository.
	// +optional
	Path string `json:"path,omitempty"`

	// Source defines the source repository of the environment.
	// +required
	Source Source `json:"source"`

	// ApiTokenSecretRef refers to a secret containing the API token
	// needed for doing pull requests.
	// Its a generic secret with the key "token".
	// +optional
	ApiTokenSecretRef *corev1.LocalObjectReference `json:"apiTokenSecretRef,omitempty"`

	// GitProvider is the name of the git provider.
	// Required for pull request strategy.
	// +Kubebuilder:Validation:Enum=github
	// +optional
	GitProvider string `json:"gitProvider"`
}

// const (
// 	SSHSecretObjectNameSuffix string = "-ssh"
// )

// Source defines the source repository of the environment.
type Source struct {
	// URL is the URL of the source repository.
	// +required
	URL string `json:"url"`

	// Ref defines the git reference to use.
	// Defaults to the "master" branch.
	// +optional
	Reference *GitRepositoryRef `json:"ref,omitempty"`

	// SecretRef is the name of the secret containing the credentials
	// to access the source repository.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

const (
	DefaultBranch string = "master"
)

// GitRepositoryRef specifies the Git reference to resolve and checkout.
type GitRepositoryRef struct {
	// Branch to check out, defaults to 'master' if no other field is defined.
	// +optional
	Branch string `json:"branch,omitempty"`
}

// EnvironmentStatus defines the observed state of Environment
type EnvironmentStatus struct {
	// ObservedGeneration is the last observed generation of the Environment
	// object.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions is a list of the current conditions of the Environment.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedCommitHash is the last observed commit hash of the Environment
	// object.
	// +optional
	ObservedCommitHash string `json:"observedCommitHash,omitempty"`
}

const (
	// EnvironmentOperationSucceedReason represents the fact that the environment listing and
	// download operations succeeded.
	EnvironmentOperationSucceedReason string = "EnvironmentOperationSucceed"

	// EnvironmentOperationFailedReason represents the fact that the environment listing or
	// download operations failed.
	EnvironmentOperationFailedReason string = "EnvironmentOperationFailed"
)

// EnvironmentProgressing resets the conditions of the Environment to metav1.Condition of
// type ReadyCondition with status 'Unknown' and ProgressingReason
// reason and message. It returns the modified Environment.
func EnvironmentProgressing(environment Environment) Environment {
	environment.Status.ObservedGeneration = environment.Generation
	environment.Status.Conditions = []metav1.Condition{}
	newCondition := metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionUnknown,
		Reason:  ProgressingReason,
		Message: "reconciliation in progress",
	}
	meta.SetStatusCondition(environment.GetStatusConditions(), newCondition)
	return environment
}

// EnvironmentReady sets the given commit on the Environment and sets the
// ReadyCondition to 'True', with the given reason and message. It returns
// the modified Environment.
func EnvironmentReady(environment Environment, reason string, message string, commit string) Environment {
	environment.Status.ObservedCommitHash = commit
	newCondition := metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  reason,
		Message: message,
	}
	meta.SetStatusCondition(environment.GetStatusConditions(), newCondition)
	return environment
}

// EnvironmentNotReady sets the ReadyCondition on the Environment to 'False', with
// the given reason and message. It returns the modified Environment.
func EnvironmentNotReady(environment Environment, reason string, message string) Environment {
	newCondition := metav1.Condition{
		Type:    ReadyCondition,
		Status:  metav1.ConditionFalse,
		Reason:  reason,
		Message: message,
	}
	meta.SetStatusCondition(environment.GetStatusConditions(), newCondition)
	return environment
}

// EnvironmentReadyMessage returns the message of the metav1.Condition of type
// ReadyCondition with status 'True' if present, or an empty string.
func EnvironmentReadyMessage(environment Environment) string {
	if c := meta.FindStatusCondition(environment.Status.Conditions, ReadyCondition); c != nil {
		if c.Status == metav1.ConditionTrue {
			return c.Message
		}
	}
	return ""
}

// IsReady returns true if the Environment is ready, i.e. if the
// ReadyCondition is present and has status 'True'.
func (e *Environment) IsReady() bool {
	if c := meta.FindStatusCondition(e.Status.Conditions, ReadyCondition); c != nil {
		if c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

// GetStatusConditions returns a pointer to the Status.Conditions slice
func (e *Environment) GetStatusConditions() *[]metav1.Condition {
	return &e.Status.Conditions
}

// func (e *Environment) IsGitRepositoryPrivate() bool {
// 	return e.Spec.Source.SecretRef != nil
// }

// func (e *Environment) GetSSHSecretObjectName() string {
// 	return e.Name + SSHSecretObjectNameSuffix
// }

func (e *Environment) GetBranch() string {
	if e.Spec.Source.Reference != nil {
		return e.Spec.Source.Reference.Branch
	}
	return DefaultBranch
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Environment is the Schema for the environments API
type Environment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnvironmentSpec   `json:"spec,omitempty"`
	Status EnvironmentStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EnvironmentList contains a list of Environment
type EnvironmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Environment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Environment{}, &EnvironmentList{})
}
