package util

// Contains helper functions for OVN
// Eventually these should all be migrated to go-ovn bindings

import (
	"fmt"
	"net"
	"sync"

	networkattachmentdefinitionapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	ocpconfigapi "github.com/openshift/api/config/v1"
	libovsdbclient "github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdbops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/sbdb"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"

	kapi "k8s.io/api/core/v1"
)

// CreateMACBinding Creates MAC binding in OVN SBDB
func CreateMACBinding(sbClient libovsdbclient.Client, logicalPort, datapathName string, portMAC net.HardwareAddr, nextHop net.IP) error {
	p := func(item *sbdb.DatapathBinding) bool {
		return item.ExternalIDs["name"] == datapathName
	}
	datapath, err := libovsdbops.GetDatapathBindingWithPredicate(sbClient, p)
	if err != nil {
		return fmt.Errorf("error getting datapath %s: %v", datapathName, err)
	}

	// find Create mac_binding if needed
	mb := sbdb.MACBinding{
		LogicalPort: logicalPort,
		MAC:         portMAC.String(),
		Datapath:    datapath.UUID,
		IP:          nextHop.String(),
	}

	err = libovsdbops.CreateOrUpdateMacBinding(sbClient, &mb, &mb.Datapath, &mb.LogicalPort, &mb.IP, &mb.MAC)
	if err != nil {
		return fmt.Errorf("failed to create mac binding %+v: %v", mb, err)
	}

	return nil
}

func PlatformTypeIsEgressIPCloudProvider() bool {
	return config.Kubernetes.PlatformType == string(ocpconfigapi.AWSPlatformType) ||
		config.Kubernetes.PlatformType == string(ocpconfigapi.GCPPlatformType) ||
		config.Kubernetes.PlatformType == string(ocpconfigapi.AzurePlatformType)
}

// Information about a nad associated with a specific pod, including:
// - nad configuration, currently only MissRateLimitConfig
// - NetworkSelectionElement representing the nad
type PodNadInfo struct {
	*NadConfig
	Network *networkattachmentdefinitionapi.NetworkSelectionElement
}

// See if this pod needs to plumb over this given network specified by netconf,
// and return all the matching NetworkSelectionElement map if any exists.
//
// Return value:
//    bool: if this Pod is on this Network; true or false
//    map[string]*PodNadInfo: see above
//    error:  error in case of failure
// Note that the same network could exist in the same Pod more than once, but with different net-attach-def name
func IsNetworkOnPod(pod *kapi.Pod, netAttachInfo *NetAttachDefInfo) (bool, map[string]*PodNadInfo, error) {
	nseMap := map[string]*PodNadInfo{}
	// default rate limit configuration of the default network
	nadConf := &NadConfig{MissRateLimitConfig{config.OvnKubeNode.MaxNewConnPPS, config.OvnKubeNode.MaxNewConnBurst}}

	podDesc := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
	if !netAttachInfo.IsSecondary {
		defaultNetwork, err := GetK8sPodDefaultNetwork(pod)
		if err != nil {
			// multus won't add this Pod if this fails, should never happen
			return false, nil, fmt.Errorf("failed to get default network for pod %s: %v", podDesc, err)
		}
		if defaultNetwork == nil {
			nseMap[ovntypes.DefaultNetworkName] = &PodNadInfo{nadConf, nil}
			return true, nseMap, nil
		}
		v, ok := netAttachInfo.NetAttachDefs.Load(GetNadKeyName(defaultNetwork.Namespace, defaultNetwork.Name))
		if !ok {
			return false, nil, nil
		}
		nseMap[ovntypes.DefaultNetworkName] = &PodNadInfo{v.(*NadConfig), defaultNetwork}
		return true, nseMap, nil
	}

	// For non-default network controller, try to see if its name exists in the Pod's k8s.v1.cni.cncf.io/networks, if no,
	// return false;
	allNetworks, err := GetK8sPodAllNetworks(pod)
	if err != nil {
		return false, nil, err
	}
	for _, network := range allNetworks {
		if v, ok := netAttachInfo.NetAttachDefs.Load(GetNadKeyName(network.Namespace, network.Name)); ok {
			nadName := GetNadName(network.Namespace, network.Name, false)
			nseMap[nadName] = &PodNadInfo{v.(*NadConfig), network}
		}
	}
	return len(nseMap) != 0, nseMap, nil
}

func GetNADNamesFromMap(netAttachDefs *sync.Map) []string {
	nadNames := []string{}
	(*netAttachDefs).Range(func(key, value interface{}) bool {
		nadNames = append(nadNames, key.(string))
		return true
	})
	return nadNames
}
