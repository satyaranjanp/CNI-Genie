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
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	networkv1 "k8s.io/api/networking/v1"

	clientset "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/clientset/versioned"
	networkscheme "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/clientset/versioned/scheme"
	informers "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/informers/externalversions"
	listers "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/listers/network/v1"

	"crypto/md5"
	"encoding/json"
	. "github.com/Huawei-PaaS/CNI-Genie/utils"
	"github.com/coreos/go-iptables/iptables"
	"k8s.io/apimachinery/pkg/labels"
	"strconv"
	"strings"
	"time"
)

const (
	ControllerAgentName = "network-policy-controller"
	GenieNetworkPolicy  = "genieNetworkPolicy"
	GeniePolicyPrefix   = "GnPlc-"
	GenieNetworkPrefix  = "GnNtk-"

	FilterTable  = "filter"
	ForwardChain = "FORWARD"
	InputChain   = "INPUT"
	OutputChain  = "OUTPUT"
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

// NewNpcController returns a new network policy controller
func NewNpcController(
	kubeclientset kubernetes.Interface,
	extclientset clientset.Interface,
	kubeInformerFactory kubeinformers.SharedInformerFactory,
	externalObjInformerFactory informers.SharedInformerFactory) *NetworkPolicyController {

	networkPolicyInformer := kubeInformerFactory.Networking().V1().NetworkPolicies()
	logicalNwInformer := externalObjInformerFactory.Alpha().V1().LogicalNetworks()

	networkscheme.AddToScheme(scheme.Scheme)
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartLogging(glog.Infof)
	eventBroadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{Interface: kubeclientset.CoreV1().Events("")})
	recorder := eventBroadcaster.NewRecorder(scheme.Scheme, v1.EventSource{Component: ControllerAgentName})

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
	l := obj.(*LogicalNetwork)
	npc.enqueueLogicalNetwork(l, "ADD", "")
}

func (npc *NetworkPolicyController) updateLogicalNetwork(old, cur interface{}) {
	oldLn := old.(*LogicalNetwork)
	newLn := cur.(*LogicalNetwork)

	if oldLn.ResourceVersion == newLn.ResourceVersion {
		return
	}

	if oldLn.Spec.SubSubnet != newLn.Spec.SubSubnet {
		npc.enqueueLogicalNetwork(newLn, "UPDATE", "")
	}
}

func (npc *NetworkPolicyController) deleteLogicalNetwork(obj interface{}) {
	l, ok := obj.(*LogicalNetwork)
	if !ok {
		tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
		if !ok {
			runtime.HandleError(fmt.Errorf("Couldn't get object from tombstone %#v", obj))
			return
		}
		l, ok = tombstone.Obj.(*LogicalNetwork)
		if !ok {
			runtime.HandleError(fmt.Errorf("Tombstone contained object that is not a logical network object %#v", obj))
			return
		}
	}

	npc.enqueueLogicalNetwork(l, "DELETE", l.Spec.SubSubnet)
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

	if oldNp.Annotations != nil || newNp.Annotations != nil {
		if oldNp.Annotations != nil && newNp.Annotations != nil && (oldNp.Annotations[GenieNetworkPolicy] == newNp.Annotations[GenieNetworkPolicy]) {
			return
		} //else

		npc.enqueueNetworkPolicy(newNp, "UPDATE", oldNp.Annotations[GenieNetworkPolicy])
		return
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

	if n.Annotations != nil && n.Annotations[GenieNetworkPolicy] != "" {
		glog.Infof("Before delete: annotation: %v", n.Annotations[GenieNetworkPolicy])
		npc.enqueueNetworkPolicy(n, "DELETE", n.Annotations[GenieNetworkPolicy])
		return
	}

	npc.enqueueNetworkPolicy(n, "DELETE", "")
}

func (npc *NetworkPolicyController) enqueueLogicalNetwork(lnw *LogicalNetwork, action string, args string) {
	keyaction := map[string]string{"kind": "logicalnetwork", "name": lnw.Name, "namespace": lnw.Namespace, "action": action, "args": args}
	keyactionjson, err := json.Marshal(keyaction)
	if err != nil {
		glog.Warning("Unable to marshal keyaction for logical network: %v", err.Error())
	}

	npc.npcWorkqueue.Add(string(keyactionjson))
}

func (npc *NetworkPolicyController) enqueueNetworkPolicy(np *networkv1.NetworkPolicy, action string, args string) {
	keyaction := map[string]string{"kind": "networkpolicy", "name": np.Name, "namespace": np.Namespace, "action": action, "args": args}
	keyactionjson, err := json.Marshal(keyaction)
	if err != nil {
		glog.Warning("Unable to marshal keyaction for network policy: %v", err.Error())
	}

	npc.npcWorkqueue.Add(string(keyactionjson))
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

type NetworkPolicy struct {
	NetworkSelector string
	PeerNetworks    string
}

func createIptableChainName(prefix, suffix string) string {
	m := md5.Sum([]byte(suffix))
	return (prefix + fmt.Sprintf("%x", m))[:26]
}

func (npc *NetworkPolicyController) getCidrFromNetwork(name, namespace string) (string, error) {

	lnw, err := npc.logicalNwLister.LogicalNetworks(namespace).Get(name)
	if err != nil {
		return "", err
	}

	return lnw.Spec.SubSubnet, nil

}

type NetworkPolicyInfo struct {
	Name      string
	Namespace string
	Networks  map[string][]string
}

func unmarshalKeyActionJson(key string) (map[string]string, error) {
	var keyaction map[string]string
	err := json.Unmarshal([]byte(key), &keyaction)
	if err != nil {
		return nil, err
	}
	return keyaction, nil
}

func getLogicalNetworksFromAnnotation(annotation string) (map[string][]string, error) {
	networkPolicies := make([]NetworkPolicy, 0)
	policyNetworkMap := make(map[string][]string)
	glog.Infof("In getLogicalNetworksFromAnnotation: annotation: %v",annotation)
	err := json.Unmarshal([]byte(annotation), &networkPolicies)
	if err != nil {
		return nil, fmt.Errorf("Error while unmarshalling annotation: %v", err)
	}

	for _, policy := range networkPolicies {
		policyNetworkMap[policy.NetworkSelector] = append(policyNetworkMap[policy.NetworkSelector], strings.Split(policy.PeerNetworks, ",")...)
	}

	return policyNetworkMap, nil
}

func (npc *NetworkPolicyController) handleNetworkPolicyAdd(name, namespace string) error {
	glog.Infof("In handleNetworkPolicyAdd")
	networkPolicy, err := npc.networkPoliciesLister.NetworkPolicies(namespace).Get(name)
	if err != nil {
		return fmt.Errorf("Failed to get network policy object %s in namespace %s: %v", name, namespace, err)
	}

	networks, err := getLogicalNetworksFromAnnotation(networkPolicy.Annotations[GenieNetworkPolicy])
	if err != nil {
		return fmt.Errorf("Error while unmarshalling logical networks info from annotation of policy object %s: %v", networkPolicy.Name, err)
	}

	iptablesCommandExec, err := iptables.New()
	if err != nil {
		glog.Errorf("Iptables command executer intialization failed: %v", err.Error())
		return fmt.Errorf("Iptables command executer intialization failed: %v", err.Error())
	}

	nwPolicyChainName := createIptableChainName(GeniePolicyPrefix, name+namespace)

	err = iptablesCommandExec.NewChain("filter", nwPolicyChainName)
	if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}

	err = iptablesCommandExec.ClearChain("filter", nwPolicyChainName)
	if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}

	for nwSelector, peerNw := range networks {
		for _, peer := range peerNw {
			l, err := npc.logicalNwLister.LogicalNetworks(namespace).Get(peer)
			if err != nil {
				continue
			}
			spec := []string{"-s", l.Spec.SubSubnet, "-j", "ACCEPT"}
			err = iptablesCommandExec.AppendUnique(FilterTable, nwPolicyChainName, spec...)
			if err != nil {
				continue
			}
		}

/*		lnChain := createIptableChainName(GenieNetworkPrefix, nwSelector+namespace)
		rulespec := []string{"-j", nwPolicyChainName}
		exists, err := iptablesCommandExec.Exists(FilterTable, lnChain, rulespec...)
		if err != nil {
			glog.Warningf("Failed to check if logical network (%s) contains network policy (%s) as a rule: %v", nwSelector, name, err.Error())
			continue
		}
		if !exists {
			err := iptablesCommandExec.Insert(FilterTable, lnChain, 1, rulespec...)
			if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
				glog.Warningf("Failed to insert network policy (%s) rule in logical network (%s) chain: %v", name, nwSelector, err.Error())
				continue
			}
		}
*/
		err = npc.handleLogicalNetworkAdd(nwSelector, namespace)
		if err != nil {
			glog.Infof("Skipping handling logical network (%s): %v", nwSelector, err)
			continue
		}
	}

	if err != nil {
		return fmt.Errorf("Error synchronizing network policy: name: %s, namesapce: %s; error: %v", name, namespace, err)
	}

	return nil
}

func (npc *NetworkPolicyController) handleNetworkPolicyUpdate(name, namespace string) error {
	return npc.handleNetworkPolicyAdd(name, namespace)
}

func (npc *NetworkPolicyController) handleNetworkPolicyDelete(name, namespace, annotation string) error {
	logicalNetworks, err := getLogicalNetworksFromAnnotation(annotation)
	if err != nil {
		return err
	}

	iptablesCommandExec, err := iptables.New()
	if err != nil {
		return fmt.Errorf("Iptables command executer intialization failed: %v", err.Error())
	}

	npChain := createIptableChainName(GeniePolicyPrefix, name+namespace)

	for destNw := range logicalNetworks {
		// For each destination logical network, search the respective chain in the
		// iptable and remove the entry for this network policy chain
		logicalNwChain := createIptableChainName(GenieNetworkPrefix, destNw+namespace)
		rules, err := iptablesCommandExec.List(FilterTable, logicalNwChain)
		if err != nil {
			if err.(*iptables.Error).ExitStatus() != 1 {
				return fmt.Errorf("Failed to list rules for logical network (%s) chain: %v", destNw, err)
			} else {
				glog.Infof("Iptable chain for logical network (%s) does not exist, so skipping.", logicalNwChain)
				continue
			}
		}
		for i, rule := range rules {
			if strings.Contains(rule, npChain) {
				err := iptablesCommandExec.Delete(FilterTable, logicalNwChain, strconv.Itoa(i))
				if err != nil {
					break
				}
			}
		}
	}

	err = iptablesCommandExec.ClearChain(FilterTable, npChain)
	if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
		return fmt.Errorf("Error flushing network policy chain (%s) before deleting it: %v", npChain, err.Error())
	}
	err = iptablesCommandExec.DeleteChain(FilterTable, npChain)
	if err != nil {
		return fmt.Errorf("Error while deleting iptable chain %s for network policy %s: %v", npChain, name, err)
	}

	return nil
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

		networks, _ := getLogicalNetworksFromAnnotation(policy.Annotations[GenieNetworkPolicy])
		if lnwname != "" {
			if strings.Contains(policy.Annotations[GenieNetworkPolicy], lnwname) {
				if networks[lnwname] != nil {
					policyInfo = append(policyInfo, NetworkPolicyInfo{Name: policy.Name, Namespace: policy.Namespace, Networks: networks})
				} else {
					for _, peers := range networks {
						for _, p := range peers {
							if lnwname == strings.TrimSpace(p) {
								policyInfo = append(policyInfo, NetworkPolicyInfo{Name: policy.Name, Namespace: policy.Namespace, Networks: nil})
							}
						}
					}
				}
			} else {
				continue
			}
		} else {
			policyInfo = append(policyInfo, NetworkPolicyInfo{
				Name:      policy.Name,
				Namespace: policy.Namespace,
				Networks:  networks,
			})
		}
	}

	return policyInfo, nil
}

func (npc *NetworkPolicyController) handleLogicalNetworkAdd(name, namespace string) error {
	logicalNetwork, err := npc.logicalNwLister.LogicalNetworks(namespace).Get(name)
	if err != nil {
		return fmt.Errorf("Error while getting logical network %s:%s : %v", namespace, name, err)
	}

	policyInfo, err := npc.ListNetworkPolicies(name, namespace)
	if err != nil {
		glog.Errorf("Error in ListNetworkPolicies for logical network (%s): %v", name, err)
	}

	if len(policyInfo) != 0 {
		iptablesCommandExec, err := iptables.New()
		if err != nil {
			glog.Errorf("Iptables command executer intialization failed: %v", err.Error())
			return fmt.Errorf("Iptables command executer intialization failed: %v", err.Error())
		}

		for _, policy := range policyInfo {
			policyChain := createIptableChainName(GeniePolicyPrefix, policy.Name+namespace)
			if policy.Networks != nil {
				lnChain := createIptableChainName(GenieNetworkPrefix, name+namespace)
				err = iptablesCommandExec.NewChain("filter", lnChain)
				if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}

				rulespec := []string{"-d", logicalNetwork.Spec.SubSubnet, "-j", lnChain}
				exists, err := iptablesCommandExec.Exists(FilterTable, ForwardChain, rulespec...)
				if err != nil {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				if !exists {
					err := iptablesCommandExec.Insert(FilterTable, ForwardChain, 1, rulespec...)
					if err != nil {
						glog.Errorf("Failed to execute iptables command: %v", err.Error())
						return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
					}
				}

				exists, err = iptablesCommandExec.Exists(FilterTable, InputChain, rulespec...)
				if err != nil {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				if !exists {
					err := iptablesCommandExec.Insert(FilterTable, InputChain, 1, rulespec...)
					if err != nil {
						glog.Errorf("Failed to execute iptables command: %v", err.Error())
						return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
					}
				}

				exists, err = iptablesCommandExec.Exists(FilterTable, OutputChain, rulespec...)
				if err != nil {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				if !exists {
					err := iptablesCommandExec.Insert(FilterTable, OutputChain, 1, rulespec...)
					if err != nil {
						glog.Errorf("Failed to execute iptables command: %v", err.Error())
						return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
					}
				}

				rulespec = []string{"-j", "REJECT"}
				err = iptablesCommandExec.AppendUnique(FilterTable, lnChain, rulespec...)
				if err != nil {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}

				if err != nil {
					glog.Errorf("Error while geting network policies using %s as network selector: %v", err.Error())
					return fmt.Errorf("Error while geting network policies using %s as network selector: %v", name, err)
				}

				rulespec = []string{"-j", policyChain}
				exists, err = iptablesCommandExec.Exists(FilterTable, lnChain, rulespec...)
				if err != nil {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				if !exists {
					err := iptablesCommandExec.Insert(FilterTable, lnChain, 1, rulespec...)
					if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
						glog.Errorf("Failed to execute iptables command: %v", err.Error())
						return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
					}
				}
			}else {
				rulespec := []string{"-s", logicalNetwork.Spec.SubSubnet, "-j", "ACCEPT"}
				err := iptablesCommandExec.AppendUnique(FilterTable, policyChain, rulespec...)
				if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
					glog.Errorf("Failed to execute iptables command: %v", err.Error())
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
			}
		}
	}

	return nil
}

func (npc *NetworkPolicyController) handleLogicalNetworkUpdate(name, namespace string) error {
	logicalNetwork, err := npc.logicalNwLister.LogicalNetworks(namespace).Get(name)
	if err != nil {
		return fmt.Errorf("Error while getting logical network %s:%s : %v", namespace, name, err)
	}

	iptablesCommandExec, err := iptables.New()
	if err != nil {
		return fmt.Errorf("Iptables command executer intialization failed: %v", err.Error())
	}

	lnChain := createIptableChainName(GenieNetworkPrefix, name+namespace)

	fwChainRules, err := iptablesCommandExec.List(FilterTable, ForwardChain)
	if err != nil {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}
	for pos, rule := range fwChainRules {
		if strings.Contains(rule, lnChain) {
			err = iptablesCommandExec.Delete(FilterTable, ForwardChain, strconv.Itoa(pos))
			break
		}
	}
	rulespec := []string{"-d", logicalNetwork.Spec.SubSubnet, "-j", lnChain}
	err = iptablesCommandExec.Insert(FilterTable, ForwardChain, 1, rulespec...)
	if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}

	inChainRules, err := iptablesCommandExec.List(FilterTable, InputChain)
	if err != nil {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}
	for pos, rule := range inChainRules {
		if strings.Contains(rule, lnChain) {
			err = iptablesCommandExec.Delete(FilterTable, InputChain, strconv.Itoa(pos))
			break
		}
	}
	err = iptablesCommandExec.Insert(FilterTable, InputChain, 1, rulespec...)
	if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}

	outChainRules, err := iptablesCommandExec.List(FilterTable, OutputChain)
	if err != nil {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}
	for pos, rule := range outChainRules {
		if strings.Contains(rule, lnChain) {
			err = iptablesCommandExec.Delete(FilterTable, OutputChain, strconv.Itoa(pos))
			break
		}
	}
	err = iptablesCommandExec.Insert(FilterTable, OutputChain, 1, rulespec...)
	if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
		return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
	}

	return nil
}

func (npc *NetworkPolicyController) handleLogicalNetworkDelete(name, namespace, subnet string) error {
	policyInfo, err := npc.ListNetworkPolicies(name, namespace)
	if err != nil {
		glog.Errorf("Error in ListNetworkPolicies for logical network (%s): %v", name, err)
	}

	if len(policyInfo) != 0 {
		iptablesCommandExec, err := iptables.New()
		if err != nil {
			return fmt.Errorf("Iptables command executer intialization failed: %v", err.Error())
		}

		for _, policy := range policyInfo {
			if policy.Networks != nil {
				lnChain := createIptableChainName(GenieNetworkPrefix, name+namespace)

				fwChainRules, err := iptablesCommandExec.List(FilterTable, ForwardChain)
				if err != nil {
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				for pos, rule := range fwChainRules {
					if strings.Contains(rule, lnChain) {
						err = iptablesCommandExec.Delete(FilterTable, ForwardChain, strconv.Itoa(pos))
						break
					}
				}

				inChainRules, err := iptablesCommandExec.List(FilterTable, InputChain)
				if err != nil {
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				for pos, rule := range inChainRules {
					if strings.Contains(rule, lnChain) {
						err = iptablesCommandExec.Delete(FilterTable, InputChain, strconv.Itoa(pos))
						break
					}
				}

				outChainRules, err := iptablesCommandExec.List(FilterTable, OutputChain)
				if err != nil {
					return fmt.Errorf("Failed to execute iptables command: %v", err.Error())
				}
				for pos, rule := range outChainRules {
					if strings.Contains(rule, lnChain) {
						err = iptablesCommandExec.Delete(FilterTable, OutputChain, strconv.Itoa(pos))
						break
					}
				}

				err = iptablesCommandExec.ClearChain(FilterTable, lnChain)
				if err != nil && err.(*iptables.Error).ExitStatus() != 1 {
					return fmt.Errorf("Error flushing logical network chain (%s) before deleting it: %v", lnChain, err.Error())
				}
				err = iptablesCommandExec.DeleteChain(FilterTable, lnChain)
				if err != nil {
					return fmt.Errorf("Error while deleting iptable chain for logical network %s : %v", name, err)
				}
			} else {
				policyChain := createIptableChainName(GeniePolicyPrefix, policy.Name + policy.Namespace)
				plcRules, err := iptablesCommandExec.List(FilterTable, policyChain)
				if err != nil {
					glog.Errorf("Failed to list rules for policy chain (%s) for policy (%s): %v", policyChain, policy.Name, err.Error())
					continue
				}
				for pos, rule := range plcRules {
					if strings.Contains(rule, subnet) {
						err = iptablesCommandExec.Delete(FilterTable, policyChain, strconv.Itoa(pos))
						if err != nil {
							glog.Errorf("Failed to remove rule for subnet (%s) of logical network (%s:%s) from policy chain (%s) for policy (%s:%s): %v", subnet, namespace, name, policyChain, namespace, policy.Name, err)
						}
						break
					}
				}
			}
		}
	}
	return nil
}

func (npc *NetworkPolicyController) syncHandler(key string) error {

	npc.mutex.Lock()
	defer npc.mutex.Unlock()

	glog.Infof("Starting syncHandler for key: %s", key)
	keyaction, e := unmarshalKeyActionJson(key)
	if e != nil {
		return (fmt.Errorf("Error while unmarshalling action parameters: %v", e))
	}

	var err error
	switch keyaction["kind"] {
	case "networkpolicy":
		switch keyaction["action"] {
		case "ADD":
			err = npc.handleNetworkPolicyAdd(keyaction["name"], keyaction["namespace"])

		case "UPDATE":
			err = npc.handleNetworkPolicyUpdate(keyaction["name"], keyaction["namespace"])

		case "DELETE":
			err = npc.handleNetworkPolicyDelete(keyaction["name"], keyaction["namespace"], keyaction["args"])

		default:

		}

	case "logicalnetwork":
		switch keyaction["action"] {
		case "ADD":
			err = npc.handleLogicalNetworkAdd(keyaction["name"], keyaction["namespace"])

		case "UPDATE":
			err = npc.handleLogicalNetworkUpdate(keyaction["name"], keyaction["namespace"])

		case "DELETE":
			err = npc.handleLogicalNetworkDelete(keyaction["name"], keyaction["namespace"], keyaction["args"])

		default:
		}
	}

	return err
}
