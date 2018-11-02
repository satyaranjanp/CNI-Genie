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
)

func AddCRDNetwork(kubeClient *kubernetes.Clientset, cniArgs utils.CNIArgs, namespace, annotation, cniDir string) (types.Result, error) {
	networks, err := networkcrd.GetNetworkInfo(annotation)
	if err != nil {
		return nil, fmt.Errorf("Error parsing network selection annotation: %v", err)
	}

	var endResult types.Result
	var newErr []error
	for i, net := range networks {
		if net.Namespace == "" {
			net.Namespace = namespace
		}

		network, err := networkcrd.GetNetworkCRDObject(kubeClient, net.Name, net.Namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr,"CNI Genie Error getting network crd object (%s:%s): %v\n", net.Namespace, net.Name, err)
			newErr = append(newErr, fmt.Errorf("Error getting network crd object (%s:%s): %v", net.Namespace, net.Name, err))
			continue
		}

		var netConfigList *libcni.NetworkConfigList
		emptySpec := networkcrd.NetworkAttachmentDefinitionSpec{}
		if network.Spec ==  emptySpec || network.Spec.Config == "" {
			netConfigList, err = networkcrd.GetConfFromFile(network, cniDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "CNI Genie Error extracting plugin configuration from configuration file for network object (%s:%s): %v\n", net.Namespace, net.Name, err)
				newErr = append(newErr, fmt.Errorf("Error extracting plugin configuration from configuration file for network object (%s:%s): %v", net.Namespace, net.Name, err))
				continue
			}
		} else {
			netConfigList, err = networkcrd.GetConfFromSpec(&net, network)
			if err != nil {
				fmt.Fprintf(os.Stderr, "CNI Genie Error extracting plugin configuration from network object (%s:%s): %v\n", net.Namespace, net.Name, err)
				newErr = append(newErr, fmt.Errorf("Error extracting plugin configuration from network object (%s:%s): %v", net.Namespace, net.Name, err))
				continue
			}
		}

		var intfName string
		if net.InterfaceRequest != "" {
			intfName = net.InterfaceRequest
		} else {
			intfName = "eth" + strconv.Itoa(i)
		}
		result, err := delegateAddNetwork(cniArgs, netConfigList, intfName)
		fmt.Fprintf(os.Stderr, "CNI Genie addNetwork via network crd err *** %v; result***  %v\n", err, result)
		if result != nil {
			endResult, err = MergeResult(result, endResult)
			if err != nil {
				fmt.Fprintf(os.Stderr,"CNI Genie Error merging result with end result for network object (%s:%s): %v\n", net.Namespace, net.Name, err)
				newErr = append(newErr, fmt.Errorf("Error merging result with end result for network object (%s:%s): %v", net.Namespace, net.Name, err))
			}
		}
	}

	return endResult, nil
}

func DeleteCRDNetwork(kubeClient *kubernetes.Clientset, cniArgs utils.CNIArgs, namespace, annotation, cniDir string) error {
	networks, err := networkcrd.GetNetworkInfo(annotation)
	if err != nil {
		return nil, fmt.Errorf("Error parsing network selection annotation: %v", err)
	}

	var newErr []error
	for i, net := range networks {
		if net.Namespace == "" {
			net.Namespace = namespace
		}

		network, err := networkcrd.GetNetworkCRDObject(kubeClient, net.Name, net.Namespace)
		if err != nil {
			fmt.Fprintf(os.Stderr,"CNI Genie Error getting network crd object (%s:%s): %v\n", net.Namespace, net.Name, err)
			newErr = append(newErr, fmt.Errorf("Error getting network crd object (%s:%s): %v", net.Namespace, net.Name, err))
			continue
		}

		var netConfigList *libcni.NetworkConfigList
		emptySpec := networkcrd.NetworkAttachmentDefinitionSpec{}
		if network.Spec ==  emptySpec || network.Spec.Config == "" {
			netConfigList, err = networkcrd.GetConfFromFile(network, cniDir)
			if err != nil {
				fmt.Fprintf(os.Stderr, "CNI Genie Error extracting plugin configuration from configuration file for network object (%s:%s): %v\n", net.Namespace, net.Name, err)
				newErr = append(newErr, fmt.Errorf("Error extracting plugin configuration from configuration file for network object (%s:%s): %v", net.Namespace, net.Name, err))
				continue
			}
		} else {
			netConfigList, err = networkcrd.GetConfFromSpec(&net, network)
			if err != nil {
				fmt.Fprintf(os.Stderr, "CNI Genie Error extracting plugin configuration from network object (%s:%s): %v\n", net.Namespace, net.Name, err)
				newErr = append(newErr, fmt.Errorf("Error extracting plugin configuration from network object (%s:%s): %v", net.Namespace, net.Name, err))
				continue
			}
		}

		var intfName string
		if net.InterfaceRequest != "" {
			intfName = net.InterfaceRequest
		} else {
			intfName = "eth" + strconv.Itoa(i)
		}
		err = delegateDeleteNetwork(cniArgs, netConfigList, intfName)
		fmt.Fprintf(os.Stderr, "CNI Genie deleteNetwork via network crd err *** %v\n", err)
	}

	return nil
}