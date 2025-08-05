package bridgeconfig

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"sync"

	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
)

const (
	XDPOFHighPriority = 1000
	XDPOFLowPriority  = 500
	XDPOFLowCTTable   = 8
)

// A watered down version of NewBridgeConfiguration since we assume bridge is created
// etc.
func NewXDPBridgeConfiguration(netName, bridgeName string) (*BridgeConfiguration, error) {
	uplinkName, err := getIntfName(bridgeName)
	if err != nil {
		return nil, fmt.Errorf("failed to find uplink for %s: %w", bridgeName, err)
	}

	patchNADStr := strings.Replace(netName, "-", ".", -1)
	res := BridgeConfiguration{
		bridgeName: bridgeName,
		uplinkName: uplinkName,
		netConfig: map[string]*BridgeUDNConfiguration{
			netName: {PatchPort: "patch-" + patchNADStr + "_ovn_localnet_port-to-br-int"},
		},
		localnetPatchPorts: &sync.Map{},
	}

	return &res, nil
}

// If the patchport OF port changes when in use, exit.
func XdpCheckPatchPortOFFlows(bridgeName, ofPortPhys, patchIntf, ofPortPatch, curOfportPatch string) error {
	cookieKey := fmt.Sprintf("%s-%s-%s", bridgeName, ofPortPhys, ofPortPatch)
	oldcookie, err := XDPToCookie(cookieKey)
	if err != nil {
		return fmt.Errorf("error generating cookie to update XDP flows")
	}
	oldCookieFilter := fmt.Sprintf("cookie=%s/-1", oldcookie)
	stdout, _, err := util.RunOVSOfctl("dump-aggregate", bridgeName, oldCookieFilter)
	if err != nil {
		return fmt.Errorf("error getting  XDP flows")
	}
	hasFlowCountZero := strings.Contains(stdout, "flow_count=0")
	if !hasFlowCountZero {
		return fmt.Errorf("patch port %s ofport, still used by flows, changed from %s to %s",
			patchIntf, ofPortPatch, curOfportPatch)
	}
	return nil
}

// XXX We separate this out from phys since, the localnet port could change. Need to take care of
// that in healthcheck.
func (b *BridgeConfiguration) SetXDPBridgePatchOfPorts(netName string) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	netConfig, found := b.netConfig[netName]
	if !found {
		return fmt.Errorf("failed to find network %s configuration on bridge %s", netName, b.bridgeName)
	}
	oldOfportPatch := netConfig.OfPortPatch
	err := netConfig.setOfPatchPort()
	if err != nil {
		return err
	}

	// Since the localnet patch port could be created/destroyed by ovn-controller as needed,
	// we could have bridge.ofPortPatch set to a diff value. healthcheck will see if the
	// changed value invalidates flows. Here, we don't need to since, either the patch port already
	// exists and should be the same as bridge.ofPortPatch or the patch port doesn't, in which
	// case the new value will differ from bridge.ofPortPatch, but there should be no flows
	// configured. We check just to make sure.
	if oldOfportPatch != "" && oldOfportPatch != netConfig.OfPortPatch {
		err = XdpCheckPatchPortOFFlows(b.bridgeName, b.ofPortPhys, netConfig.PatchPort, oldOfportPatch, netConfig.OfPortPatch)
		if err != nil {
			klog.Errorf("Fatal error: %v", err)
			os.Exit(1)
		}
		klog.Infof("XDP patch port %q changing ofport from %s to %s", netConfig.PatchPort, oldOfportPatch, netConfig.OfPortPatch)
	}
	return nil
}

// Assume this service is not shared by tenant services with different routing
// requirements, i.e. default route etc. This means we can configure
// routing in the "main" table itself. When this service shared with tenant
// services with different routing requirements, we'll need to either spawn
// multiple instances of this service (i.e. one per tenant service) or use
// routing tables.

// Some of these can be moved to util, but we keep them here so that all the
// XDP related code is in one place, which makes it easier to maintain .

// Set the OF flows on the OVS bridge to insert the XDP functionality between the
// phys port and OVN/br-int.
// Assumes that the OVS bridge is not used for any other purpose other than
// phys connectivity (using normal flow) - this bridge is unlike the shared OVS
// bridge that is used for both phys connectivity and E-W.
// This assumption is true for the current use case, i.e. RP; when we expand
// the service, this needs to be revisited.
//
// Essentially, we create 4 flows:
//
// Flow for inserting on the physical side:
// -----------------------------------------
// Flow 1:
//      All TCP packets coming in on the physical port for the tenant VLAN:
//          strip the vlan, modify the dst mac to that of the XDP SF and send
//          to the XDP SF port.
// Flow 2:
//      All packets coming from the SF port:
//          add tenant VLAN tag and send on the physical port.
//
// Flows for inserting on the OVN side:
// ------------------------------------
// Flow 3:
//      All packets coming in from the XDP's veth port:
//          add tenant VLAN tag and send to the patch port (OVN/br-int)
//
// Flow 4:
//      All packets coming from the patch port (OVN/br-int)
//          strip the vlan, modify the dst mac to that of the XDP veth and
//          send to the XDP veth port.
//
// All except Flow 3 are related to the NAD (i.e. VLAN, Gateway etc.); flow3
// uses the podMAC to configure flows. So, we configure Flows 1,2 and 4 when
// the NAD shows up and Flow 3 whenever a pod using that NAD shows up.
//
// Normal Flows
// ------------
//     All other flows will take the normal route and should be the same as
//     if the XDP service doesn't exist (e.g. UDP)
//

func XDPToCookie(keyStr string) (string, error) {
	h := fnv.New64a()
	_, err := h.Write([]byte(keyStr))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("0x%x", h.Sum64()), nil
}

func (b *BridgeConfiguration) XDPFlows(allowedIPs []string, network, bridgeName, podMAC, xdpSFMAC, xdpVethMAC string, vlanID uint) ([]string, error) {
	var xdpOFFLows []string
	var cookie string

	netconfig := b.GetNetworkConfig(network)

	// Get SF port's OF port
	xdpSFPortOfPort, stderr, err := util.GetOVSOfPort("--if-exists", "get",
		"interface", config.OvnKubeNode.XDPSFRep, "ofport")
	if err != nil {
		return xdpOFFLows, fmt.Errorf("error getting ofport for SF port %s for %s:stderr %v, %v",
			config.OvnKubeNode.XDPSFRep, bridgeName, stderr, err)
	}

	// Get Veth port's OF port
	xdpVethPortOfPort, stderr, err := util.GetOVSOfPort("--if-exists", "get",
		"interface", config.OvnKubeNode.XDPVeth, "ofport")
	if err != nil {
		return xdpOFFLows, fmt.Errorf("error getting ofport for Veth port %s for %s: stderr %v, %v",
			config.OvnKubeNode.XDPVeth, bridgeName, stderr, err)
	}

	// Vlan modification action.
	mod_vlan_id := fmt.Sprintf("mod_vlan_vid:%d,", vlanID)

	cookieKey := fmt.Sprintf("%s-%s-%s", bridgeName, b.ofPortPhys, netconfig.OfPortPatch)
	cookie, err = XDPToCookie(cookieKey)
	if err != nil {
		return xdpOFFLows, fmt.Errorf("error generating OF cookie using %s-%s: %v", b.ofPortPhys,
			netconfig.OfPortPatch, err)
	}
	for _, allowedIP := range allowedIPs {
		// From the wire to the pod/VM
		// ---------------------------

		// Flow 1:
		//	Add a rule to send TCP packets for the pod from the wire to the XDP CT zone to check if
		//	we need to send this for XDP processing.
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, dl_vlan=%d, nw_dst=%s/32, tcp,"+
				"actions=ct(table=%d,zone=%d)", cookie, XDPOFHighPriority, b.ofPortPhys,
				vlanID, allowedIP, XDPOFLowCTTable, config.Default.HostXDPCTZone))

		// Flow 2:
		//	For est connections (i.e. initiated from the pod) send to the pod
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_dst=%s/32, tcp, ct_state=+est+trk,"+
				"actions=output:%s", cookie, XDPOFLowCTTable, XDPOFHighPriority, b.ofPortPhys,
				vlanID, allowedIP, netconfig.OfPortPatch))

		// Flow 3:
		//	Send the others for XDP processing
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_dst=%s/32, tcp,"+
				"actions=strip_vlan,mod_dl_dst:%s,output:%s", cookie, XDPOFLowCTTable, XDPOFLowPriority,
				b.ofPortPhys, vlanID, allowedIP, xdpSFMAC, xdpSFPortOfPort))

		// Flow 4:
		//    Add a rule to send packets from XDP SF port to uplink port after adding the VLAN
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, nw_src=%s/32, ip, "+
				"actions=%smod_dl_src:%s,output:%s", cookie, XDPOFHighPriority, xdpSFPortOfPort,
				allowedIP, mod_vlan_id, podMAC, b.ofPortPhys))

		// From the pod/VM to wire
		// ---------------------------

		// Flow 1:
		// 	Add a rule to track TCP initiated from the VM to bypass XDP processing
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp, "+
				"actions=ct(table=%d,zone=%d)", cookie, XDPOFHighPriority, netconfig.OfPortPatch,
				vlanID, allowedIP, XDPOFLowCTTable, config.Default.HostXDPCTZone))

		// Flow 2:
		// 	IF it is a SYN, commit to match the return traffic and send it out, bypassing XDP/
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp, tcp_flags=+syn-ack,"+
				"actions=ct(commit,zone=%d),output:%s", cookie, XDPOFLowCTTable, XDPOFHighPriority,
				netconfig.OfPortPatch, vlanID, allowedIP, config.Default.HostXDPCTZone, b.ofPortPhys))

		// Flow 3:
		// 	IF it is est send it out, bypassing XDP
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp, ct_state=+est+trk,"+
				"actions=output:%s", cookie, XDPOFLowCTTable, XDPOFHighPriority, netconfig.OfPortPatch,
				vlanID, allowedIP, b.ofPortPhys))

		// Flow 4:
		// 	Send everything else for XDP processing
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp,"+
				"actions=strip_vlan,mod_dl_dst:%s,output:%s", cookie, XDPOFLowCTTable, XDPOFLowPriority,
				netconfig.OfPortPatch, vlanID, allowedIP, xdpVethMAC, xdpVethPortOfPort))

		// Flow 5:
		//    Add a rule to send packets from XDP veth port to patch port after adding the VLAN
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, ip, nw_dst=%s/32,"+
				"actions=%soutput:%s", cookie, XDPOFHighPriority, xdpVethPortOfPort,
				allowedIP, mod_vlan_id, netconfig.OfPortPatch))
	}

	return xdpOFFLows, nil
}
