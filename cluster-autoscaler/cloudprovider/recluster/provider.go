// SPDX-License-Identifier: Apache-2.0
// Recluster cloud-provider – bridges Cluster-Autoscaler with Rcnode CRDs.
//
// One Rcnode == one CA-NodeGroup (maxSize=1, minSize=0).
//
// Author: you © 2025
package recluster

import (
	"context"
	"os"
	"strings"

	rcv1 "github.com/lcereser6/recluster-sync/api/v1alpha1" // shared API module

	apiv1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	"k8s.io/autoscaler/cluster-autoscaler/utils/errors"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const providerName = "recluster"

var _ cloudprovider.CloudProvider = &ReclusterProvider{}

// ------------------------------------------------------------------------------
// Public factory: called by cluster-autoscaler/builder_all.go
// ------------------------------------------------------------------------------
func BuildReclusterProvider(
	opts config.AutoscalingOptions,
	discovery cloudprovider.NodeGroupDiscoveryOptions,
	rl *cloudprovider.ResourceLimiter,
	factory informers.SharedInformerFactory) cloudprovider.CloudProvider {

	restCfg, err := clientConfig()
	if err != nil {
		klog.Fatalf("recluster: cannot build rest config: %v", err)
	}

	prov, err := buildFromREST(restCfg, rl, factory.Core().V1().Nodes().Informer())
	if err != nil {
		klog.Fatalf("recluster: init failed: %v", err)
	}
	return prov
}

// Decide between in-cluster and $KUBECONFIG (for kind / kwok local runs).
func clientConfig() (*rest.Config, error) {
	if os.Getenv("KWOK_PROVIDER_MODE") == "local" {
		apiCfg, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
		if err != nil {
			return nil, err
		}
		return clientcmd.NewDefaultClientConfig(*apiCfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	}
	return rest.InClusterConfig()
}

// ------------------------------------------------------------------------------
// Internal initialiser
// ------------------------------------------------------------------------------
func buildFromREST(cfg *rest.Config, rl *cloudprovider.ResourceLimiter,
	nodeInformer cache.SharedIndexInformer) (*ReclusterProvider, error) {

	//k8s, err := kube.NewForConfig(cfg)

	dy, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	// ── list all Rcnode CRs ───────────────────────────────────────────────────
	rcGVR := rcv1.GroupVersion.WithResource("rcnodes")
	unstr, err := dy.Resource(rcGVR).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	ngs := []cloudprovider.NodeGroup{}
	for i := range unstr.Items {
		var rc rcv1.Rcnode
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(
			unstr.Items[i].Object, &rc); err != nil {
			return nil, err
		}

		ngs = append(ngs, &rcNodeGroup{
			rc:          &rc,
			nodeIndexer: nodeInformer.GetIndexer(),
		})
	}

	klog.Infof("recluster provider initialised with %d Rcnode objects", len(ngs))

	return &ReclusterProvider{
		nodeGroups:      ngs,
		resourceLimiter: rl,
	}, nil
}

// ------------------------------------------------------------------------------
// ReclusterProvider – minimal implementation
// ------------------------------------------------------------------------------
type ReclusterProvider struct {
	nodeGroups      []cloudprovider.NodeGroup
	resourceLimiter *cloudprovider.ResourceLimiter
}

func (*ReclusterProvider) Name() string { return providerName }

func (p *ReclusterProvider) NodeGroups() []cloudprovider.NodeGroup { return p.nodeGroups }

func (p *ReclusterProvider) NodeGroupForNode(n *apiv1.Node) (cloudprovider.NodeGroup, error) {
	if !strings.HasPrefix(n.Spec.ProviderID, providerName) { // foreign node
		return nil, nil
	}
	id := strings.TrimPrefix(n.Spec.ProviderID, providerName+"://")
	for _, ng := range p.nodeGroups {
		if ng.Id() == id {
			return ng, nil
		}
	}
	return nil, nil
}

// --- miscellaneous stubs -----------------------------------------------------
func (p *ReclusterProvider) HasInstance(*apiv1.Node) (bool, error) { return true, nil }
func (p *ReclusterProvider) Pricing() (cloudprovider.PricingModel, errors.AutoscalerError) {
	return nil, cloudprovider.ErrNotImplemented
}
func (p *ReclusterProvider) GetAvailableMachineTypes() ([]string, error) {
	return nil, cloudprovider.ErrNotImplemented
}
func (p *ReclusterProvider) NewNodeGroup(string, map[string]string, map[string]string,
	[]apiv1.Taint, map[string]resource.Quantity) (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}
func (p *ReclusterProvider) GetResourceLimiter() (*cloudprovider.ResourceLimiter, error) {
	return p.resourceLimiter, nil
}
func (*ReclusterProvider) GPULabel() string                          { return "" }
func (*ReclusterProvider) GetAvailableGPUTypes() map[string]struct{} { return nil }
func (*ReclusterProvider) GetNodeGpuConfig(*apiv1.Node) *cloudprovider.GpuConfig {
	return nil
}
func (*ReclusterProvider) Cleanup() error { return nil }
func (*ReclusterProvider) Refresh() error { return nil }
