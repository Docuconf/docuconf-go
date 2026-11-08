// Package v1alpha1 is the input of function-docuconf: where the image, the
// contract and the platform's values come from, and which composed
// workload receives the rendered configuration.
// +kubebuilder:object:generate=false
// +groupName=docuconf.fn.crossplane.io
// +versionName=v1alpha1
package v1alpha1

import (
	"encoding/json"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// Input configures function-docuconf for one workload.
// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=crossplane
type Input struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Image is the app's image. Its contract is pulled from the registry
	// (docuconf push), and the workload runs it pinned by digest.
	// +optional
	Image *Source `json:"image,omitempty"`

	// Contract is the contract itself, CUE or JSON, for platforms that
	// commit it beside the composition instead of pulling it with the
	// image (SPEC.md section 8). It may also be oci://IMAGE.
	// +optional
	Contract string `json:"contract,omitempty"`

	// Values, Files and Overlays are the values, file sources and overlay
	// values documents of SPEC.md section 7.
	// +optional
	Values Document `json:"values,omitempty"`
	// +optional
	Files Document `json:"files,omitempty"`
	// +optional
	Overlays Document `json:"overlays,omitempty"`

	// Policy is the environment's policy, CUE, unified with the values as
	// docuconf vet -policy does.
	// +optional
	Policy string `json:"policy,omitempty"`

	// Target is the composed workload that receives the configuration.
	Target Target `json:"target"`

	// AllowEnvFrom lets the target container keep envFrom, whose keys the
	// contract cannot check. It is refused otherwise.
	// +optional
	AllowEnvFrom bool `json:"allowEnvFrom,omitempty"`

	// CheckSecrets checks that each Secret the values and file sources
	// name exists in the composite resource's namespace, with the key
	// they name. On by default; it needs Crossplane v2.
	// +optional
	CheckSecrets *bool `json:"checkSecrets,omitempty"`
}

// Source is a string from the composite resource, or a fixed one.
type Source struct {
	// FromFieldPath is a field path in the composite resource, such as
	// spec.image.
	// +optional
	FromFieldPath string `json:"fromFieldPath,omitempty"`
	// Value is used when FromFieldPath is unset.
	// +optional
	Value string `json:"value,omitempty"`
}

// Document is a values document: a map from the composite resource, with
// fixed entries on top.
type Document struct {
	// FromFieldPath is a field path in the composite resource that holds
	// the document, such as spec.config.values. Until it is set, the
	// function waits.
	// +optional
	FromFieldPath string `json:"fromFieldPath,omitempty"`
	// Set holds entries the composition fixes, over the composite
	// resource's.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	Set map[string]any `json:"set,omitempty"`
}

// Target names the workload.
type Target struct {
	// Resource is the name of the desired composed resource: a
	// Deployment, StatefulSet, DaemonSet, Job, CronJob or Pod that an
	// earlier function in the pipeline declares.
	Resource string `json:"resource"`
	// Container is the app's container. The first one by default.
	// +optional
	Container string `json:"container,omitempty"`
}

// DeepCopyObject copies the input.
func (in *Input) DeepCopyObject() runtime.Object {
	b, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	out := &Input{}
	if err := json.Unmarshal(b, out); err != nil {
		panic(err)
	}
	return out
}
