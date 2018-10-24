package networkcrd

import (
	"github.com/containernetworking/cni/pkg/types"
	"strings"
	"encoding/json"
	"fmt"
	"k8s.io/client-go/kubernetes"
//	"regexp"
//	"errors"
	"github.com/containernetworking/cni/libcni"
	"github.com/Huawei-PaaS/CNI-Genie/utils"
	"github.com/Huawei-PaaS/CNI-Genie/genie"
	"net"
	"strconv"
	"os"
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

func getNetworkInfo(annotation string) ([]NetworkSelectionElement, error) {
	var networks []NetworkSelectionElement
	if true == strings.ContainsAny(annotation, "[{") {
		err := json.Unmarshal([]byte(annotation), &networks)
		if err != nil {
			return nil, fmt.Errorf("Error unmarshalling network annotation: %v", err)
		}

		for _, network := range networks {
			if err = validateFields(network); err != nil {
				return nil, fmt.Errorf("Error in validation: %v", err)
			}
		}
	} else {
		nw := strings.Split(annotation, ",")
		for _, n := range nw {
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

func getPluginConf(net *NetworkSelectionElement, networkCrd *NetworkAttachmentDefinition) ([]byte, error) {
	conf := make(map[string]interface{})
	configbytes := []byte{networkCrd.Spec.Config}

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

	if net.IPRequest != "" || net.MacRequest != "" {
		err := insertOptionalParameters(net, conf["plugins"].([]interface{})[0])
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

	return configbytes, nil
}

func getNetworkCRDOnbject(kubeClient *kubernetes.Clientset, name, namespace string) (*NetworkAttachmentDefinition, error) {
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

func AddNetwork(kubeClient *kubernetes.Clientset, cniArgs utils.CNIArgs, namespace, annotation string) (types.Result, error) {
	networks, err := getNetworkInfo(annotation)
	if err != nil {
		return nil, fmt.Errorf("Error parsing network selection annotation: %v", err)
	}

	var endResult types.Result
	var newErr []error
	for i, net := range networks {
		if net.Namespace == "" {
			net.Namespace = namespace
		}

		networkcrd, err := getNetworkCRDOnbject(kubeClient, net.Name, net.Namespace)
		if err != nil {
			newErr = append(newErr, fmt.Errorf("Error getting network crd object (%s:%s): %v", net.Namespace, net.Name, err))
		}

		// check for eth name

		pluginConf, err := getPluginConf(&net, &networkcrd)
		if err != nil {
			newErr = append(newErr,fmt.Errorf("Error extracting plugin configuration from network object (%s:%s): %v", net.Namespace, net.Name, err))
		}
		netConfigList, err := libcni.ConfListFromBytes(pluginConf)
		if err != nil {
			newErr = append(newErr, fmt.Errorf("Error creating network configuration list for network object (%s:%s): %v", net.Namespace, net.Name, err))
		}

		var intfName string
		if net.InterfaceRequest != "" {
			intfName = net.InterfaceRequest
		} else {
			intfName = "eth" + strconv.Itoa(i)
		}
		result, err := genie.DelegateAddNetwork(cniArgs, netConfigList, intfName)
		fmt.Fprintf(os.Stderr, "CNI Genie addNetwork via network crd err *** %v; result***  %v\n", err, result)
		if result != nil {
			endResult, err = genie.MergeResult(result, endResult)
			if err != nil {
				newErr = append(newErr, fmt.Errorf("Error merging result with end result for network object (%s:%s): %v", net.Namespace, net.Name, err))
			}
		}
	}

	return endResult, nil
}
