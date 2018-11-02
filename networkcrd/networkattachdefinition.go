package networkcrd

import (

	"strings"
	"encoding/json"
	"fmt"
	"k8s.io/client-go/kubernetes"
	//	"regexp"
	//	"errors"
	"net"
	"os"
	"github.com/containernetworking/cni/libcni"
)

const (
	DefaultCNIDir = "/etc/cni/net.d"
)

/*func validateName(name string) error {
	exp := "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	if name != "" && false == regexp.MatchString(exp, name) {
		return errors.New("Expression mismatch: must consist of lower case alphanumeric characters and must start and end with an alphanumeric character")
	}
	return nil
}*/

func validateFields(network *NetworkSelectionElement) error {
	//var err error
	/*	if err = validateName(network.Name); err != nil {
			return fmt.Errorf("Error validating network name (%s): %v", network.Name, err)
		}
		if err = validateName(network.Namespace); err != nil {
			return fmt.Errorf("Error validating network namespace (%s): %v", network.Namespace, err)
		}
		if err = validateName(network.InterfaceRequest); err != nil {
			return fmt.Errorf("Error validating network interface name (%s): %v", network.InterfaceRequest, err)
		}
	*/
	/* validation of network name and namespace are being purposefully omitted*/

	if len(network.IPRequest) > 0 {
		for _, ip := range network.IPRequest {
			if nil == net.ParseIP(ip) {
				return fmt.Errorf("Invalid IP address: %s", ip)
			}
		}
	}

	if network.MacRequest != "" {
		if _, err := net.ParseMAC(network.MacRequest); err != nil {
			return fmt.Errorf("Invalid mac address %s", network.MacRequest)
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
		network.InterfaceRequest = network.Name[ni+1:]
		network.Name = network.Name[:ni]
	}
}

func GetNetworkInfo(annotation string) ([]NetworkSelectionElement, error) {
	var networks []NetworkSelectionElement
	if true == strings.ContainsAny(annotation, "[{") {
		fmt.Fprintln(os.Stderr,"CNI Genie GetNetworkInfo in true == strings.ContainsAny(annotation, [{/)")
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
		fmt.Fprintln(os.Stderr,"CNI Genie GetNetworkInfo in else part")
		nw := strings.Split(annotation, ",")
		for _, n := range nw {
			var network NetworkSelectionElement
			parseNetworkInfoFromAnnot(&network, n)
			fmt.Fprintf(os.Stderr,"CNI Genie GetNetworkInfo after parseNetworkInfoFromAnnot: %v\n", network)
			if err := validateFields(&network); err != nil {
				return nil, fmt.Errorf("Error in validation: %v", err)
			}

			networks = append(networks, network)
		}
	}
	fmt.Fprintf(os.Stderr,"CNI Genie GetNetworkInfo networks: %v\n", networks)
	return networks, nil
}

type optionalParameters struct {
	Cni struct {
		    Ips []string `json:"ips,omitempty"`
		    Mac string `json:"mac,omitempty"`
	    } `json:"cni"`
}

func confListFromConf(conf map[string]interface{}) map[string]interface{} {
	conflist := map[string]interface{}{
		"cniVersion": conf["cniVersion"],
		"name": conf["name"],
		"plugins": []interface{}{conf},
	}
	conf["name"] = ""
	conf["cniVersion"] = ""
	return conflist
}

func insertOptionalParameters(net *NetworkSelectionElement, conf map[string]interface{}) error {
	var opt optionalParameters
	opt.Cni.Ips = net.IPRequest
	opt.Cni.Mac = net.MacRequest
	bytes, err := json.Marshal(opt)
	if err != nil {
		return err
	}
	conf["args"] = string(bytes)
	return nil
}

func GetConfFromFile(networkCrd *NetworkAttachmentDefinition, cniDir string) (*libcni.NetworkConfigList, error) {
	return libcni.LoadConfList(networkCrd.Name, cniDir)
}

func GetConfFromSpec(net *NetworkSelectionElement, networkCrd *NetworkAttachmentDefinition) (*libcni.NetworkConfigList, error) {
	conf := make(map[string]interface{})
	fmt.Fprintf(os.Stderr,"CNI Genie GetPluginConf networkCrd.Spec.Config: %v\n", networkCrd.Spec.Config)
	configbytes := []byte(networkCrd.Spec.Config)

	err := json.Unmarshal(configbytes, &conf)
	if err != nil {
		return nil, fmt.Errorf("Error parsing plugin configuration data: %v", err)
	}

	marshalRequired := false
	if name, ok := conf["name"].(string); !ok || (ok == true && strings.TrimSpace(name) == "" ) {
		conf["name"] = networkCrd.Name
		marshalRequired = true
	}

	if conf["type"] != nil && conf["plugins"] == nil {
		conf = confListFromConf(conf)
		marshalRequired = true
	}

	if len(net.IPRequest) > 0 || net.MacRequest != "" {
		err := insertOptionalParameters(net, conf["plugins"].([]interface{})[0].(map[string]interface{}))
		if err != nil {
			return nil, fmt.Errorf("Error inserting optional parameters in plugin configuration: %v", err)
		}
		marshalRequired = true
	}

	if marshalRequired == true {
		var confbytes []byte
		confbytes, err = json.Marshal(&conf)
		if err != nil {
			return nil, fmt.Errorf("Error marshalling plugin conf: %v", err)
		}

		return confbytes, nil
	}

	netConfigList, err := libcni.ConfListFromBytes(configbytes)
	if err != nil {
		return nil, fmt.Errorf("Error getting conflist from bytes: %v", err)
	}

	return netConfigList, nil
}

func GetNetworkCRDObject(kubeClient *kubernetes.Clientset, name, namespace string) (*NetworkAttachmentDefinition, error) {
	path := fmt.Sprintf("/apis/k8s.cni.cncf.io/v1/namespaces/%s/network-attachment-definitions/%s", namespace, name)
	fmt.Fprintf(os.Stderr,"CNI Genie GetNetworkCRDObject path: %v\n", path)
	obj, err := kubeClient.ExtensionsV1beta1().RESTClient().Get().AbsPath(path).DoRaw()
	if err != nil {
		return nil, fmt.Errorf("Error performing GET request: %v", err)
	}
	fmt.Fprintf(os.Stderr,"CNI Genie GetNetworkCRDObject raw object: %v\n", obj)
	networkcrd := &NetworkAttachmentDefinition{}
	err = json.Unmarshal(obj, networkcrd)
	if err != nil {
		return nil, err
	}

	return networkcrd, nil
}

