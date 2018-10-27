package networkcrd

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"github.com/containernetworking/cni/pkg/types"
)

type NetworkAttachmentDefinition struct {
	metav1.TypeMeta
	// Note that ObjectMeta is mandatory, as an object
	// name is required
	metav1.ObjectMeta

	// Specification describing how to add or remove network
	// attachments for a Pod. In the absence of valid keys in
	// the Spec field, the implementation shall attach/detach an
	// implementation-known network referenced by the object’s
	// name.
	// +optional
	Spec NetworkAttachmentDefinitionSpec `json:"spec"`
}

type NetworkAttachmentDefinitionSpec struct {
	// Config contains a standard JSON-encoded CNI configuration
	// or configuration list which defines the plugin chain to
	// execute. The CNI configuration may omit the 'name' field
	// which will be populated by the implementation when the
	// Config is passed to CNI delegate plugins.
	// +optional
	Config string `json:"config,omitempty"`
}

// NetworkSelectionElement represents one element of the JSON format
// Network Attachment Selection Annotation as described in section 4.1.2
// of the CRD specification.
type NetworkSelectionElement struct {
	// Name contains the name of the Network object this element selects
	Name string `json:"name"`
	// Namespace contains the optional namespace that the network referenced
	// by Name exists in
	Namespace string `json:"namespace,omitempty"`
	// IPRequest contains an optional requested IP address for this network
	// attachment
	IPRequest []string `json:"ips,omitempty"`
	// MacRequest contains an optional requested MAC address for this
	// network attachment
	MacRequest string `json:"mac,omitempty"`
	// InterfaceRequest contains an optional requested name for the
	// network interface this attachment will create in the container
	InterfaceRequest string `json:"interface,omitempty"`
}

type NetworkStatus struct {
	Name      string    `json:"name"`
	Interface string    `json:"interface,omitempty"`
	IPs       []string  `json:"ips,omitempty"`
	Mac       string    `json:"mac,omitempty"`
	Default   bool      `json:"default,omitempty"`
	DNS       types.DNS `json:"dns,omitempty"`
}
