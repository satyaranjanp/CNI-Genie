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
package main

import (
	"fmt"
	"sync"

	"github.com/golang/glog"
	"k8s.io/apimachinery/pkg/util/runtime"
	kubeinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	networklisters "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
//	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	networkv1 "k8s.io/api/networking/v1"

	clientset "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/clientset/versioned"
//	samplescheme "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/clientset/versioned/scheme"
	informers "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/informers/externalversions"
	listers "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/listers/logicalnetworkcontroller/v1alpha1"
	v1alpha1 "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/apis/logicalnetworkcontroller/v1alpha1"

	"k8s.io/apimachinery/pkg/labels"
	"strings"
	"crypto/md5"
	"encoding/base32"
	"github.com/coreos/go-iptables/iptables"
	"time"
	"encoding/json"
)

const (
	controllerAgentName = "network-policy-controller"
	GenieNetworkPolicy = "genieNetworkPolicy"
	GeniePolicyPrefix = "GENIENWPOLICY"
	GenieNetworkPrefix = "GENIELN"
)

type NetworkPolicyController struct {
	kubeclientset kubernetes.Interface
	extclientset clientset.Interface

	networkPoliciesLister networklisters.NetworkPolicyLister
	networkPoliciesSynced cache.InformerSynced
	logicalNwLister        listers.LogicalNetworkLister
	logicalNwSynced        cache.InformerSynced

	npcWorkqueue workqueue.RateLimitingInterface
	recorder record.EventRecorder

	mutex sync.Mutex
}

type NetworkPolicyInfo struct {
	Name string
	Namespace string
	Networks map[string][]string
}

// NewNpcController returns a new network policy controller
func NewNpcController(
	kubeclientset kubernetes.Interface,
	extclientset clientset.Interface,
	kubeInformerFactory kubeinformers.SharedInformerFactory,
	externalObjInformerFactory informers.SharedInformerFactory) *NetworkPolicyController {

	networkPolicyInformer := kubeInformerFactory.Networking().V1().NetworkPolicies()
	logicalNwInformer := externalObjInformerFactory.Logicalnetworkcontroller().V1alpha1().LogicalNetworks()

//	samplescheme.AddToScheme(scheme.Scheme)
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: controllerAgentName})

	npcController := &NetworkPolicyController{
		kubeclientset:     kubeclientset,
		extclientset:   extclientset,
		networkPoliciesLister: networkPolicyInformer.Lister(),
		networkPoliciesSynced: networkPolicyInformer.Informer().HasSynced,
		logicalNwLister:        logicalNwInformer.Lister(),
		logicalNwSynced:        logicalNwInformer.Informer().HasSynced,
		npcWorkqueue:         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "npc"),
		recorder:          recorder,
	}

	logicalNwInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: npcController.addLogicalNetwork,
		UpdateFunc: npcController.updateLogicalNetwork,
		DeleteFunc: npcController.deleteLogicalNetwork,
	})

	networkPolicyInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: npcController.addPolicy,
		UpdateFunc:npcController.updatePolicy,
		DeleteFunc: npcController.deletePolicy,
	})

	return npcController
}

func (npc *NetworkPolicyController) addLogicalNetwork(obj interface{}) {
	l := obj.(*v1alpha1.LogicalNetwork)
	npc.enqueueLogicalNetwork(l, "ADD", "")
}

func (npc *NetworkPolicyController) updateLogicalNetwork(old, cur interface{}) {
	oldLnw := old.(*v1alpha1.LogicalNetwork)
	newLnw := cur.(*v1alpha1.LogicalNetwork)

	if oldLnw.ResourceVersion == newLnw.ResourceVersion {
		return
	}

	if oldLnw.Spec.SubSubnet == newLnw.Spec.SubSubnet {
		return
	}

	npc.enqueueLogicalNetwork(newLnw, "UPDATE", "")
}

func (npc *NetworkPolicyController) deleteLogicalNetwork(obj interface{}) {
	l, ok := obj.(*v1alpha1.LogicalNetwork)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			runtime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		l, ok = tombstone.Obj.(*v1alpha1.LogicalNetwork)
		if !ok {
			runtime.HandleError(fmt.Errorf("Tombstone contained object that is not a logical network object %#v", obj))
			return
		}
	}

	npc.enqueueLogicalNetwork(l, "DELETE", "")
}

func (npc *NetworkPolicyController) addPolicy(obj interface{}) {
	n := obj.(*networkv1.NetworkPolicy)
	npc.enqueueNetworkPolicy(n, "ADD", "")
}

func (npc *NetworkPolicyController) updatePolicy(old, cur interface{}) {
	oldNp := old.(*networkv1.NetworkPolicy)
	newNp := cur.(*networkv1.NetworkPolicy)

	if oldNp.ResourceVersion == newNp.ResourceVersion {
		return
	}

	if oldNp.Annotations == nil && newNp.Annotations == nil {
		return
	} else if oldNp.Annotations != nil && newNp.Annotations != nil {
		if oldNp.Annotations[GenieNetworkPolicy] == newNp.Annotations[GenieNetworkPolicy] {
	 		return
		} //else
	}

	npc.enqueueNetworkPolicy(newNp, "UPDATE", "")
}

func (npc *NetworkPolicyController) deletePolicy(obj interface{}) {
	n, ok := obj.(*networkv1.NetworkPolicy)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			runtime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		n, ok = tombstone.Obj.(*networkv1.NetworkPolicy)
		if !ok {
			runtime.HandleError(fmt.Errorf("Tombstone contained object that is not a network policy object %#v", obj))
			return
		}
	}

	npc.enqueueNetworkPolicy(n, "DELETE", n.Annotations[GenieNetworkPolicy])
}

func (npc *NetworkPolicyController) enqueueLogicalNetwork(lnw *v1alpha1.LogicalNetwork, action string, args string) {
	keyaction := map[string]string {"kind": "logicalnetwork", "name": lnw.Name, "namespace": lnw.Namespace, "action": action, "args": args}
	keyactionjson, err := json.Marshal(keyaction)
	if err != nil {
		glog.Warning("Unable to marshal keyaction for logical network: %v", err.Error())
	}

	npc.npcWorkqueue.AddRateLimited(string(keyactionjson))
}

func (npc *NetworkPolicyController) enqueueNetworkPolicy(np *networkv1.NetworkPolicy, action string, args string) {
	keyaction := map[string]string {"kind": "networkpolicy", "name": np.Name, "namespace": np.Namespace, "action": action, "args": args}
	keyactionjson, err := json.Marshal(keyaction)
	if err != nil {
		glog.Warning("Unable to marshal keyaction for network policy: %v", err.Error())
	}

	npc.npcWorkqueue.AddRateLimited(string(keyactionjson))
}

func (npc *NetworkPolicyController) Run(n int, stopCh <-chan struct{}) error {
	defer runtime.HandleCrash()
	defer npc.npcWorkqueue.ShutDown()

	glog.Info("Starting network policy controller")

	glog.Info("Synchronizing informer caches...")
	if ok := cache.WaitForCacheSync(stopCh, npc.networkPoliciesSynced, npc.logicalNwSynced); !ok {
		return fmt.Errorf("Synchronization of informer caches failed.")
	}

	for i := 0; i < n; i++ {
		// Spawn worker threads
		go wait.Until(npc.worker, time.Second, stopCh)
	}

	glog.Info("Started worker threads")
	<-stopCh
	glog.Info("Shutting down worker threads")

	return nil
}

func (npc *NetworkPolicyController) worker() {
	for npc.processNextWorkItemInQueue() {
	}
}

func (npc *NetworkPolicyController) processNextWorkItemInQueue() bool {
	key, quit := npc.npcWorkqueue.Get()
	if quit {
		return false
	}
	defer npc.npcWorkqueue.Done(key)

	err := npc.syncHandler(key.(string))
	runtime.HandleError(fmt.Errorf("Error in synchandler: key: %v, error: %v", key, err))

	return true
}

func getLogicalNetworksFromAnnotation(annotation string) map[string][]string {
	networks := make(map[string][]string)
	networksets := strings.Split(annotation, ";")
	for _, networkset := range networksets {
		sourceNetworks := strings.Split(networkset[strings.Index(networkset, "-") + 1:], ",")
		for i, _ := range sourceNetworks {
			sourceNetworks[i] = strings.TrimSpace(sourceNetworks[i])
		}
		networks[strings.TrimSpace(networkset[:strings.Index(networkset, "-")])] = sourceNetworks
	}

	return networks
}

// ListNetworkPolicies lists the network policies which are to be imposed on the given logical network.
// If no logical network name is given then select all the policies which have GenieNetwork Policy annotation
func (npc *NetworkPolicyController) ListNetworkPolicies(lnwname string, namespace string) ([]NetworkPolicyInfo, error) {

	policyInfo := make([]NetworkPolicyInfo, 0)

	networkPolicies, err := npc.networkPoliciesLister.NetworkPolicies(namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, policy := range networkPolicies {
		if policy.Annotations == nil || policy.Annotations[GenieNetworkPolicy] == "" {
			continue
		}

		if lnwname != "" {
			if strings.Contains(policy.Annotations[GenieNetworkPolicy], lnwname) {
				networks := getLogicalNetworksFromAnnotation(policy.Annotations[GenieNetworkPolicy])
				if networks[lnwname] != nil {
					policyInfo = append(policyInfo, NetworkPolicyInfo{Name: policy.Name, Namespace: policy.Namespace, Networks: networks})
				} else {
					continue
				}
			} else {
				continue
			}
		} else {

			policyInfo = append(policyInfo, NetworkPolicyInfo {
													Name: policy.Name,
													Namespace: policy.Namespace,
													Networks: getLogicalNetworksFromAnnotation(policy.Annotations[GenieNetworkPolicy]),
											})

		}
	}

	return policyInfo, nil
}

func createIptableChainName(prefix, suffix string) string {
	h := md5.Sum([]byte(suffix))
	str := base32.StdEncoding.EncodeToString(h[:])
	return prefix + str[:20]
}

func (npc *NetworkPolicyController) getCidrFromNetwork(name, namespace string) (string, error) {

	lnw, err := npc.logicalNwLister.LogicalNetworks(namespace).Get(name)
	if err != nil {
		return "", err
	}

	return lnw.Spec.SubSubnet, nil

}

func (npc *NetworkPolicyController) syncNetworkPolicies(networkPolicyInfo []NetworkPolicyInfo) error {

	iptablesCommandExec, err := iptables.New()
	if err != nil {
		return fmt.Errorf("Iptables command executer intialization failed: %s", err.Error())
	}

	iptableCommandArgs := make([]IptablesCommandArgs, 0)

	for _, nwPolicy	:= range networkPolicyInfo {

		nwPolicyChainName := createIptableChainName(GeniePolicyPrefix, nwPolicy.Name + nwPolicy.Namespace)
		err := iptablesCommandExec.NewChain("filter", nwPolicyChainName)
		if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
			return fmt.Errorf("Failed to execute iptables command: %s", err.Error())
		}

		err = iptablesCommandExec.ClearChain("filter", nwPolicyChainName)
		if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
			return fmt.Errorf("Failed to execute iptables command: %s", err.Error())
		}


		iptableCommandArgs = append(iptableCommandArgs, IptablesCommandArgs{
												action: "CREATE",
												table: "filter",
												chain: nwPolicyChainName,
		})

		iptableCommandArgs = append(iptableCommandArgs, IptablesCommandArgs{
			action: "CLEAR",
			table: "filter",
			chain: nwPolicyChainName,
		})


		for destNw, sourceNw := range nwPolicy.Networks {
			//get cidr from logical network
			for _, nw := range sourceNw {
				cidr, err := npc.getCidrFromNetwork(nw, nwPolicy.Namespace)
				if err != nil {
					glog.Errorf ("Failed to get subnet for logical network %s: %v", nw, err)
					continue
				}
				iptableCommandArgs = append(iptableCommandArgs, IptablesCommandArgs{
					action: "APPEND",
					table: "filter",
					chain: nwPolicyChainName,
					args: []string{"-d", cidr, "-j", "ACCEPT"},
				})
			}

			//make an entry of this policy chain in the dest logical network chain
			lnChainName := createIptableChainName(GenieNetworkPrefix, destNw + nwPolicy.Namespace)
			iptableCommandArgs = append(iptableCommandArgs, IptablesCommandArgs{
				action: "INSERT",
				table: "filter",
				chain: lnChainName,
				position: 1,
				args: []string {},
			})
		}
	}
	return nil
}

type IptablesCommandArgs struct {
	action string
	table string
	args []string
	comment string
	chain string
	position int
}

func executeIptableCommands([]IptablesCommandArgs) error {

	return nil
}

func unmarshalKeyActionJson(key string) (map[string]string, error) {
	var keyaction map[string]string
	err := json.Unmarshal([]byte(key), &keyaction)
	if err != nil {
		return nil, err
	}
	return keyaction, nil
}

func (npc *NetworkPolicyController) handleAddNetworkPolicy(name, namespace string) error {
	networkPolicy, err := npc.networkPoliciesLister.NetworkPolicies(namespace).Get(name)
	if err != nil {
		return fmt.Errorf("Failed to get network policy object %s in namespace %s: %v", name, namespace, err)
	}

	err = npc.syncNetworkPolicies([]NetworkPolicyInfo{
		{
				Name: name,
				Namespace: namespace,
				Networks: getLogicalNetworksFromAnnotation(networkPolicy.Annotations[GenieNetworkPolicy]),
		},
	})
	if err != nil {
		return fmt.Errorf("Error synchronizing network policy: name: %s, namesapce: %s; error: %v", name, namespace, err)
	}

	return nil
}

func (npc *NetworkPolicyController) handleUpdateNetworkPolicy(name, namespace string) error {
	return npc.handleAddNetworkPolicy(name, namespace)
}

func (npc *NetworkPolicyController) handleDeleteNetworkPolicy(name, namespace, annotation string) error {
	logicalNetworks := getLogicalNetworksFromAnnotation(annotation)
	for destNw, _ := range logicalNetworks {
		// For each destination logical network, search the respective chain in the
		// iptable and remove the entry for this network policy chain

	}
	return nil
}

func (npc *NetworkPolicyController) syncHandler(key string) error {

	npc.mutex.Lock()
	defer npc.mutex.Unlock()


	keyaction, e := unmarshalKeyActionJson(key)
	if e != nil {
		return(fmt.Errorf("Error while unmarshalling action parameters: %v", e))
	}

	var err error
	if keyaction["kind"] == "networkpolicy" {
		switch keyaction["action"] {
		case "ADD":
			err = npc.handleAddNetworkPolicy(keyaction["name"], keyaction["namespace"])

		case "UPDATE":
			err = npc.handleUpdateNetworkPolicy(keyaction["name"], keyaction["namespace"])

		case "DELETE":
			err = npc.handleDeleteNetworkPolicy(keyaction["name"], keyaction["namespace"], keyaction["args"])

		default:

		}
	} else if keyaction["kind"] == "logicalnetwork" {
		switch keyaction["action"] {
		case "ADD":
		case "UPDATE":
		case "DELETE":
		default:
		}
	}

	return err
}
