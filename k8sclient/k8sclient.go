// Copyright (c) 2017 Intel Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package k8sclient

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"net"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/skel"
	cnitypes "github.com/containernetworking/cni/pkg/types"
)

// NoK8sNetworkError indicates error, no network in kubernetes
type NoK8sNetworkError struct {
	message string
}

type clientInfo struct {
	Client       KubeClient
	Podnamespace string
	Podname      string
}

// NetConf describes a network.
type NetConf struct {
	CNIVersion string `json:"cniVersion,omitempty"`

	Name         string          `json:"name,omitempty"`
	Type         string          `json:"type,omitempty"`
	Capabilities map[string]bool `json:"capabilities,omitempty"`
	IPAM         IPAM            `json:"ipam,omitempty"`
	DNS          DNS             `json:"dns"`
}

type IPAM struct {
	Type string `json:"type,omitempty"`
}

// NetConfList describes an ordered list of networks.
type NetConfList struct {
	CNIVersion string `json:"cniVersion,omitempty"`

	Name    string     `json:"name,omitempty"`
	Plugins []*NetConf `json:"plugins,omitempty"`
}

// UnmarshallableBool typedef for builtin bool
// because builtin type's methods can't be declared
type UnmarshallableBool bool

// CommonArgs contains the IgnoreUnknown argument
// and must be embedded by all Arg structs
type CommonArgs struct {
	IgnoreUnknown UnmarshallableBool `json:"ignoreunknown,omitempty"`
}

// UnmarshallableString typedef for builtin string
type UnmarshallableString string

// K8sArgs is the valid CNI_ARGS used for Kubernetes
type K8sArgs struct {
	CommonArgs
	IP                         net.IP
	K8S_POD_NAME               UnmarshallableString
	K8S_POD_NAMESPACE          UnmarshallableString
	K8S_POD_INFRA_CONTAINER_ID UnmarshallableString
}

type NetworkStatus struct {
	Name      string    `json:"name"`
	Interface string    `json:"interface,omitempty"`
	IPs       []string  `json:"ips,omitempty"`
	Mac       string    `json:"mac,omitempty"`
	Default   bool      `json:"default,omitempty"`
	DNS       DNS `json:"dns,omitempty"`
}

// DNS contains values interesting for DNS resolvers
type DNS struct {
	Nameservers []string `json:"nameservers,omitempty"`
	Domain      string   `json:"domain,omitempty"`
	Search      []string `json:"search,omitempty"`
	Options     []string `json:"options,omitempty"`
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
	IPRequest string `json:"ipRequest,omitempty"`
	// MacRequest contains an optional requested MAC address for this
	// network attachment
	MacRequest string `json:"macRequest,omitempty"`
	// InterfaceRequest contains an optional requested name for the
	// network interface this attachment will create in the container
	InterfaceRequest string `json:"interfaceRequest,omitempty"`
}

type NetworkAttachmentDefinition struct {
	metav1.TypeMeta `json:",inline"`
	// Note that ObjectMeta is mandatory, as an object
	// name is required
	Metadata metav1.ObjectMeta `json:"metadata,omitempty" description:"standard object metadata"`

	// Specification describing how to invoke a CNI plugin to
	// add or remove network attachments for a Pod.
	// In the absence of valid keys in a Spec, the runtime (or
	// meta-plugin) should load and execute a CNI .configlist
	// or .config (in that order) file on-disk whose JSON
	// “name” key matches this Network object’s name.
	// +optional
	Spec NetworkAttachmentDefinitionSpec `json:"spec"`
}

type NetworkAttachmentDefinitionSpec struct {
	// Config contains a standard JSON-encoded CNI configuration
	// or configuration list which defines the plugin chain to
	// execute.  If present, this key takes precedence over
	// ‘Plugin’.
	// +optional
	Config string `json:"config"`
}

type DelegateNetConf struct {
	Conf          NetConf
	ConfList      NetConfList
	IfnameRequest string `json:"ifnameRequest,omitempty"`
	// MasterPlugin is only used internal housekeeping
	MasterPlugin bool `json:"-"`
	// Conflist plugin is only used internal housekeeping
	ConfListPlugin bool `json:"-"`

	// Raw JSON
	Bytes []byte
}

type DelegateNetConf struct {
	Conf          NetConf
	ConfList      NetConfList
	IfnameRequest string `json:"ifnameRequest,omitempty"`
	// MasterPlugin is only used internal housekeeping
	MasterPlugin bool `json:"-"`
	// Conflist plugin is only used internal housekeeping
	ConfListPlugin bool `json:"-"`

	// Raw JSON
	Bytes []byte
}

func (e *NoK8sNetworkError) Error() string { return string(e.message) }

type defaultKubeClient struct {
	client kubernetes.Interface
}

// defaultKubeClient implements KubeClient
var _ KubeClient = &defaultKubeClient{}

func (d *defaultKubeClient) GetRawWithPath(path string) ([]byte, error) {
	return d.client.ExtensionsV1beta1().RESTClient().Get().AbsPath(path).DoRaw()
}

func (d *defaultKubeClient) GetPod(namespace, name string) (*v1.Pod, error) {
	return d.client.Core().Pods(namespace).Get(name, metav1.GetOptions{})
}

func (d *defaultKubeClient) UpdatePodStatus(pod *v1.Pod) (*v1.Pod, error) {
	return d.client.Core().Pods(pod.Namespace).UpdateStatus(pod)
}

func setKubeClientInfo(c *clientInfo, client KubeClient, k8sArgs *K8sArgs) {
	logging.Debugf("setKubeClientInfo: %v, %v, %v", c, client, k8sArgs)
	c.Client = client
	c.Podnamespace = string(k8sArgs.K8S_POD_NAMESPACE)
	c.Podname = string(k8sArgs.K8S_POD_NAME)
}

func SetNetworkStatus(k *clientInfo, netStatus []*NetworkStatus) error {

	logging.Debugf("SetNetworkStatus: %v, %v", k, netStatus)
	pod, err := k.Client.GetPod(k.Podnamespace, k.Podname)
	if err != nil {
		return logging.Errorf("SetNetworkStatus: failed to query the pod %v in out of cluster comm: %v", k.Podname, err)
	}

	var ns string
	if netStatus != nil {
		var networkStatus []string
		for _, nets := range netStatus {
			data, err := json.MarshalIndent(nets, "", "    ")
			if err != nil {
				return logging.Errorf("SetNetworkStatus: error with Marshal Indent: %v", err)
			}
			networkStatus = append(networkStatus, string(data))
		}

		ns = fmt.Sprintf("[%s]", strings.Join(networkStatus, ","))
	}
	_, err = setPodNetworkAnnotation(k.Client, k.Podnamespace, pod, ns)
	if err != nil {
		return logging.Errorf("SetNetworkStatus: failed to update the pod %v in out of cluster comm: %v", k.Podname, err)
	}

	return nil
}

func setPodNetworkAnnotation(client KubeClient, namespace string, pod *v1.Pod, networkstatus string) (*v1.Pod, error) {
	logging.Debugf("setPodNetworkAnnotation: %v, %s, %v, %s", client, namespace, pod, networkstatus)
	//if pod annotations is empty, make sure it allocatable
	if len(pod.Annotations) == 0 {
		pod.Annotations = make(map[string]string)
	}

	pod.Annotations["k8s.v1.cni.cncf.io/networks-status"] = networkstatus

	pod = pod.DeepCopy()
	var err error
	if resultErr := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		if err != nil {
			// Re-get the pod unless it's the first attempt to update
			pod, err = client.GetPod(pod.Namespace, pod.Name)
			if err != nil {
				return err
			}
		}

		pod, err = client.UpdatePodStatus(pod)
		return err
	}); resultErr != nil {
		return nil, logging.Errorf("status update failed for pod %s/%s: %v", pod.Namespace, pod.Name, resultErr)
	}
	return pod, nil
}

func getPodNetworkAnnotation(client KubeClient, k8sArgs *K8sArgs) (string, string, error) {
	var err error

	logging.Debugf("getPodNetworkAnnotation: %v, %v", client, k8sArgs)
	pod, err := client.GetPod(string(k8sArgs.K8S_POD_NAMESPACE), string(k8sArgs.K8S_POD_NAME))
	if err != nil {
		return "", "", logging.Errorf("getPodNetworkAnnotation: failed to query the pod %v in out of cluster comm: %v", string(k8sArgs.K8S_POD_NAME), err)
	}

	return pod.Annotations["k8s.v1.cni.cncf.io/networks"], pod.ObjectMeta.Namespace, nil
}

func parsePodNetworkObjectName(podnetwork string) (string, string, string, error) {
	var netNsName string
	var netIfName string
	var networkName string

	logging.Debugf("parsePodNetworkObjectName: %s", podnetwork)
	slashItems := strings.Split(podnetwork, "/")
	if len(slashItems) == 2 {
		netNsName = strings.TrimSpace(slashItems[0])
		networkName = slashItems[1]
	} else if len(slashItems) == 1 {
		networkName = slashItems[0]
	} else {
		return "", "", "", logging.Errorf("Invalid network object (failed at '/')")
	}

	atItems := strings.Split(networkName, "@")
	networkName = strings.TrimSpace(atItems[0])
	if len(atItems) == 2 {
		netIfName = strings.TrimSpace(atItems[1])
	} else if len(atItems) != 1 {
		return "", "", "", logging.Errorf("Invalid network object (failed at '@')")
	}

	// Check and see if each item matches the specification for valid attachment name.
	// "Valid attachment names must be comprised of units of the DNS-1123 label format"
	// [a-z0-9]([-a-z0-9]*[a-z0-9])?
	// And we allow at (@), and forward slash (/) (units separated by commas)
	// It must start and end alphanumerically.
	allItems := []string{netNsName, networkName, netIfName}
	for i := range allItems {
		matched, _ := regexp.MatchString("^[a-z0-9]([-a-z0-9]*[a-z0-9])?$", allItems[i])
		if !matched && len([]rune(allItems[i])) > 0 {
			return "", "", "", logging.Errorf(fmt.Sprintf("Failed to parse: one or more items did not match comma-delimited format (must consist of lower case alphanumeric characters). Must start and end with an alphanumeric character), mismatch @ '%v'", allItems[i]))
		}
	}

	logging.Debugf("parsePodNetworkObjectName: parsed: %s, %s, %s", netNsName, networkName, netIfName)
	return netNsName, networkName, netIfName, nil
}

func parsePodNetworkAnnotation(podNetworks, defaultNamespace string) ([]*NetworkSelectionElement, error) {
	var networks []*NetworkSelectionElement

	logging.Debugf("parsePodNetworkAnnotation: %s, %s", podNetworks, defaultNamespace)
	if podNetworks == "" {
		return nil, logging.Errorf("parsePodNetworkAnnotation: pod annotation not having \"network\" as key, refer Multus README.md for the usage guide")
	}

	if strings.IndexAny(podNetworks, "[{\"") >= 0 {
		if err := json.Unmarshal([]byte(podNetworks), &networks); err != nil {
			return nil, logging.Errorf("parsePodNetworkAnnotation: failed to parse pod Network Attachment Selection Annotation JSON format: %v", err)
		}
	} else {
		// Comma-delimited list of network attachment object names
		for _, item := range strings.Split(podNetworks, ",") {
			// Remove leading and trailing whitespace.
			item = strings.TrimSpace(item)

			// Parse network name (i.e. <namespace>/<network name>@<ifname>)
			netNsName, networkName, netIfName, err := parsePodNetworkObjectName(item)
			if err != nil {
				return nil, logging.Errorf("parsePodNetworkAnnotation: %v", err)
			}

			networks = append(networks, &NetworkSelectionElement{
				Name:             networkName,
				Namespace:        netNsName,
				InterfaceRequest: netIfName,
			})
		}
	}

	for _, net := range networks {
		if net.Namespace == "" {
			net.Namespace = defaultNamespace
		}
	}

	return networks, nil
}

func getCNIConfigFromFile(name string, confdir string) ([]byte, error) {
	logging.Debugf("getCNIConfigFromFile: %s, %s", name, confdir)

	// In the absence of valid keys in a Spec, the runtime (or
	// meta-plugin) should load and execute a CNI .configlist
	// or .config (in that order) file on-disk whose JSON
	// “name” key matches this Network object’s name.

	// In part, adapted from K8s pkg/kubelet/dockershim/network/cni/cni.go#getDefaultCNINetwork
	files, err := libcni.ConfFiles(confdir, []string{".conf", ".json", ".conflist"})
	switch {
	case err != nil:
		return nil, logging.Errorf("No networks found in %s", confdir)
	case len(files) == 0:
		return nil, logging.Errorf("No networks found in %s", confdir)
	}

	for _, confFile := range files {
		var confList *libcni.NetworkConfigList
		if strings.HasSuffix(confFile, ".conflist") {
			confList, err = libcni.ConfListFromFile(confFile)
			if err != nil {
				return nil, logging.Errorf("Error loading CNI conflist file %s: %v", confFile, err)
			}

			if confList.Name == name {
				return confList.Bytes, nil
			}

		} else {
			conf, err := libcni.ConfFromFile(confFile)
			if err != nil {
				return nil, logging.Errorf("Error loading CNI config file %s: %v", confFile, err)
			}

			if conf.Network.Name == name {
				// Ensure the config has a "type" so we know what plugin to run.
				// Also catches the case where somebody put a conflist into a conf file.
				if conf.Network.Type == "" {
					return nil, logging.Errorf("Error loading CNI config file %s: no 'type'; perhaps this is a .conflist?", confFile)
				}
				return conf.Bytes, nil
			}
		}
	}

	return nil, logging.Errorf("no network available in the name %s in cni dir %s", name, confdir)
}

// getCNIConfigFromSpec reads a CNI JSON configuration from the NetworkAttachmentDefinition
// object's Spec.Config field and fills in any missing details like the network name
func getCNIConfigFromSpec(configData, netName string) ([]byte, error) {
	var rawConfig map[string]interface{}
	var err error

	logging.Debugf("getCNIConfigFromSpec: %s, %s", configData, netName)
	configBytes := []byte(configData)
	err = json.Unmarshal(configBytes, &rawConfig)
	if err != nil {
		return nil, logging.Errorf("getCNIConfigFromSpec: failed to unmarshal Spec.Config: %v", err)
	}

	// Inject network name if missing from Config for the thick plugin case
	if n, ok := rawConfig["name"]; !ok || n == "" {
		rawConfig["name"] = netName
		configBytes, err = json.Marshal(rawConfig)
		if err != nil {
			return nil, logging.Errorf("getCNIConfigFromSpec: failed to re-marshal Spec.Config: %v", err)
		}
	}

	return configBytes, nil
}

func cniConfigFromNetworkResource(customResource *NetworkAttachmentDefinition, confdir string) ([]byte, error) {
	var config []byte
	var err error

	logging.Debugf("cniConfigFromNetworkResource: %v, %s", customResource, confdir)
	emptySpec := NetworkAttachmentDefinitionSpec{}
	if customResource.Spec == emptySpec {
		// Network Spec empty; generate delegate from CNI JSON config
		// from the configuration directory that has the same network
		// name as the custom resource
		config, err = getCNIConfigFromFile(customResource.Metadata.Name, confdir)
		if err != nil {
			return nil, logging.Errorf("cniConfigFromNetworkResource: err in getCNIConfigFromFile: %v", err)
		}
	} else {
		// Config contains a standard JSON-encoded CNI configuration
		// or configuration list which defines the plugin chain to
		// execute.
		config, err = getCNIConfigFromSpec(customResource.Spec.Config, customResource.Metadata.Name)
		if err != nil {
			return nil, logging.Errorf("cniConfigFromNetworkResource: err in getCNIConfigFromSpec: %v", err)
		}
	}

	return config, nil
}

func getKubernetesDelegate(client KubeClient, net *NetworkSelectionElement, confdir string) (*DelegateNetConf, error) {
	logging.Debugf("getKubernetesDelegate: %v, %v, %s", client, net, confdir)
	rawPath := fmt.Sprintf("/apis/k8s.cni.cncf.io/v1/namespaces/%s/network-attachment-definitions/%s", net.Namespace, net.Name)
	netData, err := client.GetRawWithPath(rawPath)
	if err != nil {
		return nil, logging.Errorf("getKubernetesDelegate: failed to get network resource, refer Multus README.md for the usage guide: %v", err)
	}

	customResource := &NetworkAttachmentDefinition{}
	if err := json.Unmarshal(netData, customResource); err != nil {
		return nil, logging.Errorf("getKubernetesDelegate: failed to get the netplugin data: %v", err)
	}

	configBytes, err := cniConfigFromNetworkResource(customResource, confdir)
	if err != nil {
		return nil, err
	}

	delegate, err := LoadDelegateNetConf(configBytes, net.InterfaceRequest)
	if err != nil {
		return nil, err
	}

	return delegate, nil
}

type KubeClient interface {
	GetRawWithPath(path string) ([]byte, error)
	GetPod(namespace, name string) (*v1.Pod, error)
	UpdatePodStatus(pod *v1.Pod) (*v1.Pod, error)
}

func GetK8sArgs(args *skel.CmdArgs) (*types.K8sArgs, error) {
	k8sArgs := &types.K8sArgs{}

	logging.Debugf("GetK8sNetwork: %v", args)
	err := cnitypes.LoadArgs(args.Args, k8sArgs)
	if err != nil {
		return nil, err
	}

	return k8sArgs, nil
}

// Attempts to load Kubernetes-defined delegates and add them to the Multus config.
// Returns the number of Kubernetes-defined delegates added or an error.
func TryLoadK8sDelegates(k8sArgs *types.K8sArgs, conf *types.NetConf, kubeClient KubeClient) (int, *clientInfo, error) {
	var err error
	clientInfo := &clientInfo{}

	logging.Debugf("TryLoadK8sDelegates: %v, %v, %v", k8sArgs, conf, kubeClient)
	kubeClient, err = GetK8sClient(conf.Kubeconfig, kubeClient)
	if err != nil {
		return 0, nil, err
	}

	if kubeClient == nil {
		if len(conf.Delegates) == 0 {
			// No available kube client and no delegates, we can't do anything
			return 0, nil, logging.Errorf("must have either Kubernetes config or delegates, refer Multus README.md for the usage guide")
		}
		return 0, nil, nil
	}

	setKubeClientInfo(clientInfo, kubeClient, k8sArgs)
	delegates, err := GetK8sNetwork(kubeClient, k8sArgs, conf.ConfDir)
	if err != nil {
		if _, ok := err.(*NoK8sNetworkError); ok {
			return 0, clientInfo, nil
		}
		return 0, nil, logging.Errorf("Multus: Err in getting k8s network from pod: %v", err)
	}

	if err = conf.AddDelegates(delegates); err != nil {
		return 0, nil, err
	}

	return len(delegates), clientInfo, nil
}

func GetK8sClient(kubeconfig string, kubeClient KubeClient) (KubeClient, error) {
	logging.Debugf("GetK8sClient: %s, %v", kubeconfig, kubeClient)
	// If we get a valid kubeClient (eg from testcases) just return that
	// one.
	if kubeClient != nil {
		return kubeClient, nil
	}

	var err error
	var config *rest.Config

	// Otherwise try to create a kubeClient from a given kubeConfig
	if kubeconfig != "" {
		// uses the current context in kubeconfig
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, logging.Errorf("GetK8sClient: failed to get context for the kubeconfig %v, refer Multus README.md for the usage guide: %v", kubeconfig, err)
		}
	} else if os.Getenv("KUBERNETES_SERVICE_HOST") != "" && os.Getenv("KUBERNETES_SERVICE_PORT") != "" {
		// Try in-cluster config where multus might be running in a kubernetes pod
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, logging.Errorf("createK8sClient: failed to get context for in-cluster kube config, refer Multus README.md for the usage guide: %v", err)
		}
	} else {
		// No kubernetes config; assume we shouldn't talk to Kube at all
		return nil, nil
	}

	// creates the clientset
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return &defaultKubeClient{client: client}, nil
}

func GetK8sNetwork(k8sclient KubeClient, k8sArgs *types.K8sArgs, confdir string) ([]*types.DelegateNetConf, error) {
	logging.Debugf("GetK8sNetwork: %v, %v, %v", k8sclient, k8sArgs, confdir)

	netAnnot, defaultNamespace, err := getPodNetworkAnnotation(k8sclient, k8sArgs)
	if err != nil {
		return nil, err
	}

	if len(netAnnot) == 0 {
		return nil, &NoK8sNetworkError{"no kubernetes network found"}
	}

	networks, err := parsePodNetworkAnnotation(netAnnot, defaultNamespace)
	if err != nil {
		return nil, err
	}

	// Read all network objects referenced by 'networks'
	var delegates []*types.DelegateNetConf
	for _, net := range networks {
		delegate, err := getKubernetesDelegate(k8sclient, net, confdir)
		if err != nil {
			return nil, logging.Errorf("GetK8sNetwork: failed getting the delegate: %v", err)
		}
		delegates = append(delegates, delegate)
	}

	return delegates, nil
}