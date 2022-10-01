/*
Copyright 2022.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ResolverSpec defines the desired state of cluster dns
type ResolverSpec struct {
	// QueryLogConfig configure the DNS query log configuration
	QueryLogConfig string `json:"queryLogging"`
}

// DNSStatus defines the observed state of the cluster DNS zone
type ResolverStatus struct {
	State *string `json:"state,omitempty"`
}

//+kubebuilder:resource:path=resolvers
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// Resolver is the Schema for the repos API
type Resolver struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ResolverSpec   `json:"spec,omitempty"`
	Status ResolverStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ResolverList contains a list of Resolvers
type ResolverList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Resolver `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Resolver{}, &ResolverList{})
}
