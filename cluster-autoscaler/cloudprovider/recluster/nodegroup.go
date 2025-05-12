// SPDX-License-Identifier: Apache-2.0
// One Rcnode == one CA NodeGroup (max=1, min=0).
package recluster

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	rcv1 "github.com/lcereser6/recluster-sync/api/v1alpha1"

	apiv1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/autoscaler/cluster-autoscaler/cloudprovider"
	"k8s.io/autoscaler/cluster-autoscaler/config"
	simfw "k8s.io/autoscaler/cluster-autoscaler/simulator/framework"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
)

const providerPrefix = "recluster://"

// --------------------------------------------------------------------------
// Node-group struct
// --------------------------------------------------------------------------
type rcNodeGroup struct {
	rc          *rcv1.Rcnode
	nodeIndexer cache.Indexer // shared informer index – fast lookup
}

// --- static identity --------------------------------------------------------
func (g *rcNodeGroup) Id() string            { return g.rc.Name }
func (g *rcNodeGroup) Debug() string         { return fmt.Sprintf("rcnode/%s", g.Id()) }
func (g *rcNodeGroup) MaxSize() int          { return 1 }
func (g *rcNodeGroup) MinSize() int          { return 0 }
func (g *rcNodeGroup) Exist() bool           { return true }
func (g *rcNodeGroup) Autoprovisioned() bool { return false }
func (g *rcNodeGroup) SetDebug(_ string)     {}
func (g *rcNodeGroup) Belongs(*apiv1.Node) bool {
	// never called for single-node groups
	return false
}

// --------------------------------------------------------------------------
// Inventory helpers
// --------------------------------------------------------------------------
func (g *rcNodeGroup) Nodes() ([]cloudprovider.Instance, error) {
	name := templateNodeName(g.rc)
	_, exists, _ := g.nodeIndexer.GetByKey(name)

	status := cloudprovider.InstanceStatus{
		State: cloudprovider.InstanceRunning,
	}
	if !exists {
		return nil, fmt.Errorf("node %s does not exist", name)
	}

	return []cloudprovider.Instance{{
		Id:     name,
		Status: &status,
	}}, nil
}

func (g *rcNodeGroup) TemplateNodeInfo() (*simfw.NodeInfo, error) {
	cpu := resource.NewQuantity(int64(g.rc.Spec.CPUCores*1000), resource.DecimalSI)
	mem := resource.NewQuantity(int64(g.rc.Spec.MemoryGiB)*1024*1024*1024, resource.BinarySI)

	n := &apiv1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "template-" + g.rc.Name,
			Labels: map[string]string{
				"kubernetes.io/arch": strconv.Itoa(g.rc.Spec.CPUCores),
				"kubernetes.io/os":   "linux",
			},
		},
		Status: apiv1.NodeStatus{
			Capacity: apiv1.ResourceList{
				apiv1.ResourceCPU:    *cpu,
				apiv1.ResourceMemory: *mem,
			},
			Allocatable: apiv1.ResourceList{
				apiv1.ResourceCPU:    *cpu,
				apiv1.ResourceMemory: *mem,
			},
		},
	}
	return simfw.NewNodeInfo(n, []*resourcev1.ResourceSlice{},
		&simfw.PodInfo{Pod: cloudprovider.BuildKubeProxy(g.Id())}), nil
}

// --------------------------------------------------------------------------
// Scale-up / scale-down
// --------------------------------------------------------------------------
func (g *rcNodeGroup) TargetSize() (int, error) {
	_, exists, _ := g.nodeIndexer.GetByKey(templateNodeName(g.rc))
	if exists {
		return 1, nil
	}
	return 0, nil
}

func (g *rcNodeGroup) IncreaseSize(delta int) error {
	if delta != 1 {
		return fmt.Errorf("rcNodeGroup only supports +1 (got %d)", delta)
	}
	// The CA only calls IncreaseSize when current target == 0.
	// We just create an Rcnode-linked Node by patching .Spec.DesiredState.
	patch := []byte(`{"spec":{"desiredState":"Running"}}`)
	_, err := dynamicClient().
		Resource(schema.GroupVersionResource{Group: rcv1.GroupVersion.Group, Version: rcv1.GroupVersion.Version, Resource: "rcnodes"}).
		Patch(context.TODO(), g.rc.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (g *rcNodeGroup) AtomicIncreaseSize(delta int) error { return g.IncreaseSize(delta) }

func (g *rcNodeGroup) DeleteNodes(_ []*apiv1.Node) error {
	patch := []byte(`{"spec":{"desiredState":"Stopped"}}`)
	_, err := dynamicClient().
		Resource(schema.GroupVersionResource{Group: rcv1.GroupVersion.Group, Version: rcv1.GroupVersion.Version, Resource: "rcnodes"}).
		Patch(context.TODO(), g.rc.Name, types.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

func (g *rcNodeGroup) ForceDeleteNodes(_ []*apiv1.Node) error { return cloudprovider.ErrNotImplemented }
func (g *rcNodeGroup) DecreaseTargetSize(int) error           { return cloudprovider.ErrNotImplemented }
func (g *rcNodeGroup) Create() (cloudprovider.NodeGroup, error) {
	return nil, cloudprovider.ErrNotImplemented
}
func (g *rcNodeGroup) Delete() error { return cloudprovider.ErrNotImplemented }

// --------------------------------------------------------------------------
// Tunables
// --------------------------------------------------------------------------
func (g *rcNodeGroup) GetOptions(def config.NodeGroupAutoscalingOptions) (*config.NodeGroupAutoscalingOptions, error) {
	return &def, nil
}

// --------------------------------------------------------------------------
// helpers
// --------------------------------------------------------------------------
func templateNodeName(rc *rcv1.Rcnode) string { return "kwok-fake-" + rc.Name }

var (
	dynOnce sync.Once
	dynCli  dynamic.Interface
	dynErr  error
)

func dynamicClient() dynamic.Interface {
	dynOnce.Do(func() {
		// reuse the same kube rest-config that the provider used
		rc, err := clientConfig() // <- function already shown in provider.go
		if err != nil {
			dynErr = err
			return
		}
		dynCli, dynErr = dynamic.NewForConfig(rc)
	})
	if dynErr != nil {
		// You can bubble the error up if you prefer,
		// but klog.Fatal is simplest for a one-off init failure.
		klog.Fatalf("recluster: cannot build dynamic client: %v", dynErr)
	}
	return dynCli
}
