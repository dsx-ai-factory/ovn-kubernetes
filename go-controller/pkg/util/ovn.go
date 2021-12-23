package util

// Contains helper functions for OVN
// Eventually these should all be migrated to go-ovn bindings

import (
	"fmt"
	"net"
	"strings"
	"time"

	networkattachmentdefinitionapi "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"

	kapi "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// CreateMACBinding Creates MAC binding in OVN SBDB
func CreateMACBinding(logicalPort, datapathName string, portMAC net.HardwareAddr, nextHop net.IP) error {
	datapath, err := GetDatapathUUID(datapathName)
	if err != nil {
		return err
	}

	// Check if exact match already exists
	stdout, stderr, err := RunOVNSbctl("--data=bare", "--no-heading", "--columns=_uuid", "find", "MAC_Binding",
		"logical_port="+logicalPort,
		fmt.Sprintf(`mac="%s"`, portMAC),
		"datapath="+datapath,
		fmt.Sprintf("ip=\"%s\"", nextHop))
	if err != nil {
		return fmt.Errorf("failed to check existence of MAC_Binding entry of (%s, %s, %s, %s)"+
			"stderr: %q, error: %v", datapath, logicalPort, portMAC, nextHop, stderr, err)
	}
	if stdout != "" {
		klog.Infof("The MAC_Binding entry of (%s, %s, %s, %s) exists with uuid %s",
			datapath, logicalPort, portMAC, nextHop, stdout)
		return nil
	}

	// Create new binding
	_, stderr, err = RunOVNSbctl("create", "mac_binding", "datapath="+datapath, fmt.Sprintf("ip=\"%s\"", nextHop),
		"logical_port="+logicalPort, fmt.Sprintf(`mac="%s"`, portMAC))
	if err != nil {
		return fmt.Errorf("failed to create a MAC_Binding entry of (%s, %s, %s, %s) "+
			"stderr: %q, error: %v", datapath, logicalPort, portMAC, nextHop, stderr, err)
	}

	return nil
}

// GetDatapathUUID returns the OVN SBDB UUID for a datapath
func GetDatapathUUID(datapathName string) (string, error) {
	// Get datapath from southbound, depending on startup this may take some time, so
	// wait a bit for northd to create the cluster router's datapath in southbound
	var datapath string
	err := wait.PollImmediate(time.Second, 30*time.Second, func() (bool, error) {
		datapath, _, _ = RunOVNSbctl("--data=bare", "--no-heading", "--columns=_uuid", "find", "datapath",
			"external_ids:name="+datapathName)
		datapath = strings.TrimSuffix(datapath, "\n")
		// Ignore errors; can't easily detect which are transient or fatal
		return datapath != "", nil
	})
	if err != nil {
		return "", fmt.Errorf("failed to get the datapath UUID of %s from OVN SB "+
			"stdout: %q, error: %v", ovntypes.OVNClusterRouter, datapath, err)
	}
	return datapath, nil
}

// See if this pod needs to plumb over this given network specified by netconf,
// and return all the matching NetworkSelectionElement map if any exists.
//
// Return value:
//    bool: if this Pod is on this Network; true or false
//    map[string]*networkattachmentdefinitionapi.NetworkSelectionElement: map of NetworkSelectionElement that pod is requested
//    error:  error in case of failure
// Note that the same network could exist in the same Pod more than once, but with different net-attach-def name
// The NetworkSelectionElement map is in the form of map{net_attach_def_name]*networkattachmentdefinitionapi.NetworkSelectionElement
func IsNetworkOnPod(pod *kapi.Pod, netAttachInfo *NetAttachDefInfo) (bool,
	map[string]*networkattachmentdefinitionapi.NetworkSelectionElement, error) {
	nseMap := map[string]*networkattachmentdefinitionapi.NetworkSelectionElement{}

	podDesc := fmt.Sprintf("%s/%s", pod.Namespace, pod.Name)
	if !netAttachInfo.NotDefault {
		defaultNetwork, err := GetK8sPodDefaultNetwork(pod)
		if err != nil {
			// multus won't add this Pod if this fails, should never happen
			return false, nil, fmt.Errorf("failed to get default network for pod %s: %v", podDesc, err)
		}
		if defaultNetwork == nil {
			nseMap[ovntypes.DefaultNetworkName] = nil
			return true, nseMap, nil
		} else {
			if _, ok := netAttachInfo.NetAttachDefs.Load(GetNadKeyName(defaultNetwork.Namespace, defaultNetwork.Name)); !ok {
				return false, nil, nil
			}
		}
		nseMap[ovntypes.DefaultNetworkName] = defaultNetwork
		return true, nseMap, nil
	}

	// For non-default network controller, try to see if its name exists in the Pod's k8s.v1.cni.cncf.io/networks, if no,
	// return false;
	allNetworks, err := GetK8sPodAllNetworks(pod)
	if err != nil {
		return false, nil, err
	}
	for _, network := range allNetworks {
		if _, ok := netAttachInfo.NetAttachDefs.Load(GetNadKeyName(network.Namespace, network.Name)); ok {
			nadName := GetNadName(network.Namespace, network.Name, false)
			nseMap[nadName] = network
		}
	}
	return len(nseMap) != 0, nseMap, nil
}
