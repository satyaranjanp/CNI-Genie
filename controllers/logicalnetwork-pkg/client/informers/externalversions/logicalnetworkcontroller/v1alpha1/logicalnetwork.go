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

package v1alpha1

import (
	time "time"

	v1 "controller/k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "controller/k8s.io/apimachinery/pkg/runtime"
	watch "controller/k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
	logicalnetworkcontroller_v1alpha1 "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/apis/logicalnetworkcontroller/v1alpha1"
	versioned "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/clientset/versioned"
	internalinterfaces "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/informers/externalversions/internalinterfaces"
	v1alpha1 "github.com/Huawei-PaaS/CNI-Genie/controllers/logicalnetwork-pkg/client/listers/logicalnetworkcontroller/v1alpha1"
)

// LogicalNetworkInformer provides access to a shared informer and lister for
// LogicalNetworks.
type LogicalNetworkInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1alpha1.LogicalNetworkLister
}

type logicalNetworkInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewLogicalNetworkInformer constructs a new informer for LogicalNetwork type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewLogicalNetworkInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredLogicalNetworkInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredLogicalNetworkInformer constructs a new informer for LogicalNetwork type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredLogicalNetworkInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.LogicalnetworkcontrollerV1alpha1().LogicalNetworks(namespace).List(options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.LogicalnetworkcontrollerV1alpha1().LogicalNetworks(namespace).Watch(options)
			},
		},
		&logicalnetworkcontroller_v1alpha1.LogicalNetwork{},
		resyncPeriod,
		indexers,
	)
}

func (f *logicalNetworkInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredLogicalNetworkInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *logicalNetworkInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&logicalnetworkcontroller_v1alpha1.LogicalNetwork{}, f.defaultInformer)
}

func (f *logicalNetworkInformer) Lister() v1alpha1.LogicalNetworkLister {
	return v1alpha1.NewLogicalNetworkLister(f.Informer().GetIndexer())
}
