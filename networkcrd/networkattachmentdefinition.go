package networkcrd

import (
	"encoding/json"
	"fmt"
	"github.com/containernetworking/cni/libcni"
	"k8s.io/client-go/kubernetes"
	"net"
	"regexp"
	"strings"
)

func matchRegex(str string) error {
	exp := "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	if str != "" {
		matched, _ := regexp.MatchString(exp, str)
		if matched == false {
			return fmt.Errorf("Expression mismatch: must consist of lower case alphanumeric characters and must start and end with an alphanumeric character")
		}
	}
	return nil
}

func validateIfName(ifName string) error {
	return matchRegex(ifName)
}

func validateFields(network *NetworkSelectionElement) error {
	/* validation of network name and namespace are being purposefully omitted*/
	if len(network.IPs) > 0 {
		for _, ip := range network.IPs {
			if nil == net.ParseIP(ip) {
				return fmt.Errorf("Invalid IP address: %s", ip)
			}
		}
	}

	if network.Mac != "" {
		if _, err := net.ParseMAC(network.Mac); err != nil {
			return fmt.Errorf("Invalid mac address %s", network.Mac)
		}
	}

	if network.Interface != "" {
		if err := validateIfName(network.Interface); err != nil {
			return fmt.Errorf("Invalid interface name %s", network.Interface)
		}
	}

	return nil
}

func parseNetworkInfoFromAnnot(network *NetworkSelectionElement, annot string) {
	ns := strings.Index(annot, "/")
	if ns >= 0 {
		network.Namespace = annot[:ns]
	}
	network.Name = annot[ns+1:]
	ni := strings.LastIndex(network.Name, "@")
	if ni >= 0 {
		network.Interface = network.Name[ni+1:]
		network.Name = network.Name[:ni]
	}
}

func GetNetworkInfo(annotation string) ([]NetworkSelectionElement, error) {
	var networks []NetworkSelectionElement
	if true == strings.ContainsAny(annotation, "[{") {
		err := json.Unmarshal([]byte(annotation), &networks)
		if err != nil {
			return nil, fmt.Errorf("Error unmarshalling network annotation: %v", err)
		}

		for _, network := range networks {
			if err = validateFields(&network); err != nil {
				return nil, fmt.Errorf("Error in validation: %v", err)
			}
		}
	} else {
		nw := strings.Split(annotation, ",")
		for _, n := range nw {
			n = strings.TrimSpace(n)
			var network NetworkSelectionElement
			parseNetworkInfoFromAnnot(&network, n)

			if err := validateFields(&network); err != nil {
				return nil, fmt.Errorf("Error in validation: %v", err)
			}

			networks = append(networks, network)
		}
	}

	return networks, nil
}

type optionalParameters struct {
	Cni struct {
		Ips []string `json:"ips,omitempty"`
		Mac string   `json:"mac,omitempty"`
	} `json:"cni"`
}

func confListFromConfBytes(bytes []byte) (*libcni.NetworkConfigList, error) {
	conf, err := libcni.ConfFromBytes(bytes)
	if err != nil {
		return nil, err
	}
	confList, err := libcni.ConfListFromConf(conf)
	if err != nil {
		return nil, err
	}
	return confList, nil
}

func insertOptionalParameters(net *NetworkSelectionElement, netConfigList *libcni.NetworkConfigList) error {
	conf := make(map[string]interface{})
	err := json.Unmarshal(netConfigList.Plugins[0].Bytes, &conf)
	if err != nil {
		return err
	}

	var opt optionalParameters
	opt.Cni.Ips = net.IPs
	opt.Cni.Mac = net.Mac
	conf["args"] = interface{}(opt)

	bytes, err := json.Marshal(&conf)
	if err != nil {
		return err
	}

	netConfigList.Plugins[0].Bytes = bytes
	return nil
}

func GetConfigFromFile(networkCrd *NetworkAttachmentDefinition, cniDir string) (*libcni.NetworkConfigList, error) {
	return libcni.LoadConfList(cniDir, networkCrd.Name)
}

func GetConfigFromSpec(net *NetworkSelectionElement, networkCrd *NetworkAttachmentDefinition) (*libcni.NetworkConfigList, error) {
	config := make(map[string]interface{})
	configbytes := []byte(networkCrd.Spec.Config)
	var netConfigList *libcni.NetworkConfigList
	err := json.Unmarshal(configbytes, &config)
	if err != nil {
		return nil, fmt.Errorf("Error parsing plugin configuration data: %v", err)
	}

	if name, ok := config["name"].(string); !ok || strings.TrimSpace(name) == "" {
		config["name"] = networkCrd.Name
		configbytes, err = json.Marshal(config)
		if err != nil {
			return nil, fmt.Errorf("Error inserting name into config: %v", err)
		}
	}

	if _, ok := config["plugins"]; ok {
		netConfigList, err = libcni.ConfListFromBytes(configbytes)
		if err != nil {
			return nil, fmt.Errorf("Error getting conflist from bytes: %v", err)
		}
	} else {
		netConfigList, err = confListFromConfBytes(configbytes)
		if err != nil {
			return nil, fmt.Errorf("Error converting conf bytes to conflist: %v", err)
		}
	}

	if len(net.IPs) > 0 || net.Mac != "" {
		err := insertOptionalParameters(net, netConfigList)
		if err != nil {
			return nil, fmt.Errorf("Error inserting optional parameters in plugin configuration: %v", err)
		}
	}

	return netConfigList, nil
}

func GetNetworkCRDObject(kubeClient *kubernetes.Clientset, name, namespace string) (*NetworkAttachmentDefinition, error) {
	path := fmt.Sprintf("/apis/k8s.cni.cncf.io/v1/namespaces/%s/network-attachment-definitions/%s", namespace, name)
	obj, err := kubeClient.ExtensionsV1beta1().RESTClient().Get().AbsPath(path).DoRaw()
	if err != nil {
		return nil, fmt.Errorf("Error performing GET request: %v", err)
	}
	networkcrd := &NetworkAttachmentDefinition{}
	err = json.Unmarshal(obj, networkcrd)
	if err != nil {
		return nil, err
	}

	return networkcrd, nil
}
