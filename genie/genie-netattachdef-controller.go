package genie

import (
	"fmt"
	"github.com/Huawei-PaaS/CNI-Genie/networkcrd"
	"github.com/Huawei-PaaS/CNI-Genie/utils"
	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	"k8s.io/client-go/kubernetes"
	"os"
	"strconv"
	"strings"
)

func getNetworkConfig(kubeClient *kubernetes.Clientset, netElem *networkcrd.NetworkSelectionElement, cniDir string) (*libcni.NetworkConfigList, error) {
	networkobj, err := networkcrd.GetNetworkCRDObject(kubeClient, netElem.Name, netElem.Namespace)
	if err != nil {
		return nil, fmt.Errorf("Error getting network crd object: %v", err)
	}

	var netConfigList *libcni.NetworkConfigList
	emptySpec := networkcrd.NetworkAttachmentDefinitionSpec{}
	if networkobj.Spec == emptySpec || networkobj.Spec.Config == "" {
		netConfigList, err = networkcrd.GetConfigFromFile(networkobj, cniDir)
		if err != nil {
			return nil, fmt.Errorf("Error extracting plugin configuration from configuration file: %v", err)
		}
	} else {
		netConfigList, err = networkcrd.GetConfigFromSpec(netElem, networkobj)
		if err != nil {
			return nil, fmt.Errorf("Error extracting plugin configuration from network object spec: %v", err)
		}
	}

	return netConfigList, nil
}

func getReservedIntfnames(networks []networkcrd.NetworkSelectionElement) (map[int64]bool, error) {
	reserved := make(map[int64]bool)
	req := make(map[string]int)
	l := len(DefaultIfNamePrefix)
	for i := range networks {
		if ifName := networks[i].Interface; ifName != "" {
			if req[ifName] == 0 {
				req[ifName]++
			} else {
				return nil, fmt.Errorf("Repetitive request for same interface name: %s", ifName)
			}
			if index := strings.Index(ifName, DefaultIfNamePrefix); index == 0 {
				i, err := strconv.ParseInt(ifName[index+l:], 10, 64)
				if err != nil {
					continue
				}
				reserved[i] = true
			}
		}
	}
	return reserved, nil
}

func addNetworkByCrdSpec(kubeClient *kubernetes.Clientset, cniArgs utils.CNIArgs, namespace, annotation, cniDir string) (types.Result, error) {
	networks, err := networkcrd.GetNetworkInfo(annotation)
	if err != nil {
		return nil, fmt.Errorf("Error parsing network selection annotation: %v", err)
	}

	reservedIfNames, err := getReservedIntfnames(networks)
	if err != nil {
		return nil, err
	}
	var currIndex int = -1
	var intfName string
	var errs utils.AggError
	var endResult types.Result
	for _, net := range networks {
		var err error
		var netConfigList *libcni.NetworkConfigList

		if net.Namespace == "" {
			net.Namespace = namespace
		}

		netConfigList, err = getNetworkConfig(kubeClient, &net, cniDir)
		if err != nil {
			e := fmt.Errorf("Error getting plugin configuration from network object (%s:%s): %v", net.Namespace, net.Name, err)
			fmt.Fprintf(os.Stderr, "CNI Genie"+"%v\n", e)
			errs = append(errs, e)
			continue
		}

		intfName, currIndex = getIntfName(net.Interface, reservedIfNames, currIndex)
		result, err := delegateAddNetwork(cniArgs, netConfigList, intfName)
		if err != nil {
			e := fmt.Errorf("Error adding network for network object (%s:%s): %v", net.Namespace, net.Name, err)
			fmt.Fprintf(os.Stderr, "CNI Genie"+"%v\n", e)
			errs = append(errs, e)
		} else {
			fmt.Fprintf(os.Stderr, "CNI Genie add network successful for network object (%s:%s)\n", net.Namespace, net.Name)
		}

		if result != nil {
			endResult, err = mergeWithResult(result, endResult)
			if err != nil {
				e := fmt.Errorf("Error merging result with end result for network object (%s:%s): %v", net.Namespace, net.Name, err)
				fmt.Fprintf(os.Stderr, "CNI Genie"+"%v\n", e)
				errs = append(errs, e)
			}
		}
	}

	if err := errs.AggregateErrors(); err != nil {
		return nil, err
	}
	return endResult, nil
}

func deleteNetworkByCrdSpec(kubeClient *kubernetes.Clientset, cniArgs utils.CNIArgs, namespace, annotation, cniDir string) error {
	networks, err := networkcrd.GetNetworkInfo(annotation)
	if err != nil {
		return fmt.Errorf("Error parsing network selection annotation: %v", err)
	}

	reservedIfNames, err := getReservedIntfnames(networks)
	if err != nil {
		return err
	}
	var currIndex int = -1
	var intfName string
	var errs utils.AggError
	for _, net := range networks {
		if net.Namespace == "" {
			net.Namespace = namespace
		}
		netConfigList, err := getNetworkConfig(kubeClient, &net, cniDir)
		if err != nil {
			e := fmt.Errorf("Error getting plugin configuration from network object: %v", err)
			fmt.Fprintf(os.Stderr, "CNI Genie"+"%v\n", e)
			errs = append(errs, e)
			continue
		}

		intfName, currIndex = getIntfName(net.Interface, reservedIfNames, currIndex)
		err = delegateDeleteNetwork(cniArgs, netConfigList, intfName)
		if err != nil {
			e := fmt.Errorf("Error deleting network for network object (%s:%s): %v", net.Namespace, net.Name, err)
			fmt.Fprintf(os.Stderr, "CNI Genie"+"%v\n", e)
			errs = append(errs, err)
		} else {
			fmt.Fprintf(os.Stderr, "CNI Genie delete network successful for network object (%s:%s)\n", net.Namespace, net.Name)
		}
	}

	return errs.AggregateErrors()
}
