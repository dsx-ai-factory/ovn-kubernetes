package node

import (
	"fmt"
	"hash/fnv"
	"net"
	"os"
	"os/exec"
	"strings"

	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/vishvananda/netlink"
	"k8s.io/klog/v2"
)

var xdpSFMAC string
var xdpSFDev string
var xdpVethMAC string
var xdpVethDev string
var xdpNSPath string

const (
	XDPOFHighPriority = 1000
	XDPOFLowPriority  = 500
	XDPOFLowCTTable   = 8
	XDPNSPath         = "/run/netns/"
)

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

func xdpToCookie(keyStr string) (string, error) {
	h := fnv.New64a()
	_, err := h.Write([]byte(keyStr))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("0x%x", h.Sum64()), nil
}

// If the patchport OF port changes when in use, exit.
func xdpCheckPatchPortOFFlows(bridgeName, ofPortPhys, patchIntf, ofPortPatch, curOfportPatch string) {
	cookieKey := fmt.Sprintf("%s-%s-%s", bridgeName, ofPortPhys, ofPortPatch)
	oldcookie, err := xdpToCookie(cookieKey)
	if err != nil {
		klog.Errorf("Fatal error: error generating cookie to update XDP flows")
		os.Exit(1)
	}
	oldCookieFilter := fmt.Sprintf("cookie=%s/-1", oldcookie)
	stdout, _, err := util.RunOVSOfctl("dump-aggregate", bridgeName, oldCookieFilter)
	if err != nil {
		klog.Errorf("Fatal error: error getting  XDP flows")
		os.Exit(1)
	}
	hasFlowCountZero := strings.Contains(stdout, "flow_count=0")
	if !hasFlowCountZero {
		klog.Errorf("Fatal error: patch port %s ofport, still used by flows, changed from %s to %s",
			patchIntf, ofPortPatch, curOfportPatch)
		os.Exit(1)
	}
}

func xdpSetupOFFlowsForInterface(allowedIPs []string, bridgeName string, vlanID uint, podMAC string,
	xdpSharedPatchGW *gateway, setup bool) error {
	var xdpOFFLows []string
	var cookie, key string

	op := "Setting up"
	if !setup {
		op = "Tearing Down"
	}
	defaultBridge := xdpSharedPatchGW.openflowManager.defaultBridge
	klog.Infof("%s XDP openflow rules for %v", op, allowedIPs)

	// We could be smarter in using the hash of all the ips, or some such, that'll make deletion
	// easier, but we can cheat a bit as getting a hash of the ips etc might be an overkill, so
	// we use only the 1st ip for the key.
	// TODO: generate key string by hashing all the IPs, brdige name and vlan ID
	ipStr := strings.Join(allowedIPs[:], "-")
	keyStr := strings.Join([]string{"xdp", ipStr, bridgeName, fmt.Sprintf("%d", vlanID)}, "_")
	key, err := xdpToCookie(keyStr)
	if err != nil {
		klog.Errorf("Fatal error: error generating cookie to add XDP flows")
		os.Exit(1)
	}
	if !setup {
		xdpSharedPatchGW.openflowManager.deleteFlowsByKey(key)
		xdpSharedPatchGW.openflowManager.requestFlowSync()

		klog.Infof("Completed %s XDP openflow rules for %v", strings.ToLower(op), allowedIPs)
		return nil
	}

	// These could be part of XDP bridge.

	// Get SF port's OF port
	xdpSFPortOfPort, stderr, err := util.GetOVSOfPort("--if-exists", "get",
		"interface", config.OvnKubeNode.XDPSFRep, "ofport")
	if err != nil {
		return fmt.Errorf("error getting ofport for SF port %s for %s:stderr %v, %v",
			config.OvnKubeNode.XDPSFRep, bridgeName, stderr, err)
	}

	// Get Veth port's OF port
	xdpVethPortOfPort, stderr, err := util.GetOVSOfPort("--if-exists", "get",
		"interface", config.OvnKubeNode.XDPVeth, "ofport")
	if err != nil {
		return fmt.Errorf("error getting ofport for Veth port %s for %s: stderr %v, %v",
			config.OvnKubeNode.XDPVeth, bridgeName, stderr, err)
	}

	// Vlan modification action.
	mod_vlan_id := fmt.Sprintf("mod_vlan_vid:%d,", vlanID)

	cookieKey := fmt.Sprintf("%s-%s-%s", bridgeName, defaultBridge.ofPortPhys, defaultBridge.ofPortPatch)
	cookie, err = xdpToCookie(cookieKey)
	if err != nil {
		return fmt.Errorf("error generating OF cookie using %s-%s: %v", defaultBridge.ofPortPhys,
			defaultBridge.ofPortPatch, err)
	}
	for _, allowedIP := range allowedIPs {
		// From the wire to the pod/VM
		// ---------------------------

		// Flow 1:
		//	Add a rule to send TCP packets for the pod from the wire to the XDP CT zone to check if
		//	we need to send this for XDP processing.
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, dl_vlan=%d, nw_dst=%s/32, tcp,"+
				"actions=ct(table=%d,zone=%d)", cookie, XDPOFHighPriority, defaultBridge.ofPortPhys,
				vlanID, allowedIP, XDPOFLowCTTable, HostXDPCTZone))

		// Flow 2:
		//	For est connections (i.e. initiated from the pod) send to the pod
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_dst=%s/32, tcp, ct_state=+est+trk,"+
				"actions=output:%s", cookie, XDPOFLowCTTable, XDPOFHighPriority, defaultBridge.ofPortPhys,
				vlanID, allowedIP, defaultBridge.ofPortPatch))

		// Flow 3:
		//	Send the others for XDP processing
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_dst=%s/32, tcp,"+
				"actions=strip_vlan,mod_dl_dst:%s,output:%s", cookie, XDPOFLowCTTable, XDPOFLowPriority,
				defaultBridge.ofPortPhys, vlanID, allowedIP, xdpSFMAC, xdpSFPortOfPort))

		// Flow 4:
		//    Add a rule to send packets from XDP SF port to uplink port after adding the VLAN
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, nw_src=%s/32, ip, "+
				"actions=%smod_dl_src:%s,output:%s", cookie, XDPOFHighPriority, xdpSFPortOfPort,
				allowedIP, mod_vlan_id, podMAC, defaultBridge.ofPortPhys))

		// From the pod/VM to wire
		// ---------------------------

		// Flow 1:
		// 	Add a rule to track TCP initiated from the VM to bypass XDP processing
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp, "+
				"actions=ct(table=%d,zone=%d)", cookie, XDPOFHighPriority, defaultBridge.ofPortPatch,
				vlanID, allowedIP, XDPOFLowCTTable, HostXDPCTZone))

		// Flow 2:
		// 	IF it is a SYN, commit to match the return traffic and send it out, bypassing XDP/
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp, tcp_flags=+syn-ack,"+
				"actions=ct(commit,zone=%d),output:%s", cookie, XDPOFLowCTTable, XDPOFHighPriority,
				defaultBridge.ofPortPatch, vlanID, allowedIP, HostXDPCTZone, defaultBridge.ofPortPhys))

		// Flow 3:
		// 	IF it is est send it out, bypassing XDP
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp, ct_state=+est+trk,"+
				"actions=output:%s", cookie, XDPOFLowCTTable, XDPOFHighPriority, defaultBridge.ofPortPatch,
				vlanID, allowedIP, defaultBridge.ofPortPhys))

		// Flow 4:
		// 	Send everything else for XDP processing
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=%d, priority=%d, in_port=%s, dl_vlan=%d, nw_src=%s/32, tcp,"+
				"actions=strip_vlan,mod_dl_dst:%s,output:%s", cookie, XDPOFLowCTTable, XDPOFLowPriority,
				defaultBridge.ofPortPatch, vlanID, allowedIP, xdpVethMAC, xdpVethPortOfPort))

		// Flow 5:
		//    Add a rule to send packets from XDP veth port to patch port after adding the VLAN
		xdpOFFLows = append(xdpOFFLows,
			fmt.Sprintf("cookie=%s, table=0, priority=%d, in_port=%s, ip, nw_dst=%s/32,"+
				"actions=%soutput:%s", cookie, XDPOFHighPriority, xdpVethPortOfPort,
				allowedIP, mod_vlan_id, defaultBridge.ofPortPatch))
	}
	xdpSharedPatchGW.openflowManager.updateFlowCacheEntry(key, xdpOFFLows)
	xdpSharedPatchGW.openflowManager.requestFlowSync()

	klog.Infof("Completed %s XDP openflow rules for %v", strings.ToLower(op), allowedIPs)
	return nil
}

// Initialize routing within the XDP service so that
//
//	All packet go via the SF interface by default.
//
// Additionally, routing table for this NAD with the default gateway and
// set the ARP for also for the default gateway
//
// TODO: It is assumed that the NS is in a trusted env (DPU) and chances that the NS is deleted
// is less (more likely via some admin error etc.), so we don't currently have a sync that
// makes sure all the routes etc. are around (till deleted), need to add a sync so that we
// can make sure that is  the case.
func (nc *SecondaryLocalnetNodeNetworkController) xdpSetupNSForNAD(xdpNS string, setup bool) error {
	defGWIP := nc.Gateway()
	defGWMAC := nc.GatewayMAC()
	vlanID := nc.Vlan()

	op := "Setting up"
	if !setup {
		op = "Tearing Down"
	}
	// Maybe move it to some generic XDP initialization.
	xdpNSPath = XDPNSPath + xdpNS
	xdpDesc := fmt.Sprintf("%s/%s/%v/%d", defGWIP, defGWMAC, nc.Subnets(), vlanID)
	klog.Infof("%s XDP NS %s for %s", op, xdpNSPath, xdpDesc)

	netns, err := ns.GetNS(xdpNSPath)
	if err != nil {
		return fmt.Errorf("error opening XDP NS %s for %s, error: %v", xdpNSPath, xdpDesc, err)
	}
	defer netns.Close()

	err = netns.Do(func(hostNS ns.NetNS) error {

		// Get the MAC addresses for the SF and Veth ports to configure OF rules on
		// the bridge. Assumes veth port is named as "*veth*"
		// veth is not needed here, but we can fail if the NS is not setup correctly.
		ifaces, _ := net.Interfaces()
		for _, iface := range ifaces {
			if strings.Compare(iface.Name, "lo") == 0 {
				continue
			} else if strings.Contains(iface.Name, "veth") {
				xdpVethDev = iface.Name
				xdpVethMAC = iface.HardwareAddr.String()
			} else {
				xdpSFDev = iface.Name
				xdpSFMAC = iface.HardwareAddr.String()
			}
		}

		if xdpVethDev == "" {
			return fmt.Errorf("error getting veth interface for XDP %s", xdpDesc)
		}
		if xdpSFDev == "" {
			return fmt.Errorf("error getting SF interface for XDP %s", xdpDesc)
		}

		// Get SF link
		sfLink, err := netlink.LinkByName(xdpSFDev)
		if err != nil {
			return fmt.Errorf("error getting link %s for %s:%v", xdpSFDev, xdpDesc, err)
		}

		// Since this NS is shared by multiple tenants, we'll manage multiple
		// routing tables. We'll use the Vlan ID as the table index since the
		// gateway etc. should be based on the vlan/subnet.
		table_no := int(vlanID)

		// Default Gateway IP via SF
		gwIP := net.ParseIP(defGWIP)
		gw_ip := &net.IPNet{
			IP:   gwIP,
			Mask: net.CIDRMask(32, 32),
		}
		defGW := netlink.Route{LinkIndex: sfLink.Attrs().Index, Dst: gw_ip, Scope: netlink.SCOPE_LINK, Table: table_no}

		// Default route via Gateway IP via SF
		dst_ip := &net.IPNet{
			IP:   net.IPv4(0, 0, 0, 0),
			Mask: net.CIDRMask(0, 32),
		}
		defRoute := netlink.Route{LinkIndex: sfLink.Attrs().Index, Dst: dst_ip, Gw: gwIP, Table: table_no}

		if setup {
			if err = netlink.RouteAdd(&defGW); err != nil && !os.IsExist(err) {
				return fmt.Errorf("error adding route %v on %s for %s:%v", defGW, xdpSFDev, xdpDesc, err)
			}

			if err = netlink.RouteAdd(&defRoute); err != nil && !os.IsExist(err) {
				return fmt.Errorf("error adding route %v on %s for %s:%v", defRoute, xdpSFDev, xdpDesc, err)
			}

			// Set the ARP entries.

			// Default Gateway IP
			cmd := exec.Command("arp", "-s", defGWIP, defGWMAC, "dev", xdpSFDev)
			err = cmd.Run()
			if err != nil {
				return fmt.Errorf("error adding arp for %s, %s for %s:%v", defGWIP, defGWMAC, xdpDesc, err)
			}

			klog.Infof("Completed %s XDP routes and MAC for %s", strings.ToLower(op), xdpDesc)
			return nil
		}

		// Delete the routes
		// XXX Route table?
		// XXX-We can delete the route table instead, if two NADs won't have the same subnet configured
		if err = netlink.RouteDel(&defGW); err != nil {
			return fmt.Errorf("error removing route %v on %s for %s:%v", defGW, xdpSFDev, xdpDesc, err)
		}

		if err = netlink.RouteDel(&defRoute); err != nil {
			return fmt.Errorf("error removing route %v on %s for %s:%v", defRoute, xdpSFDev, xdpDesc, err)
		}

		// Delete the ARP entries.

		// Default Gateway IP
		cmd := exec.Command("arp", "-d", defGWIP, "dev", xdpSFDev)
		err = cmd.Run()
		if err != nil {
			return fmt.Errorf("error deleting arp for %s, %s for %s:%v", defGWIP, defGWMAC, xdpDesc, err)
		}
		klog.Infof("Completed %s XDP routes and MAC for %s", strings.ToLower(op), xdpDesc)

		return nil
	})
	return err
}

// Set up the routing within the XDP service so that
//
//	All packets to the public IP of the tenant service on the host is via the veth interface
//	All other packet go via the SF interface.
//
// Additionally, set the ARP for the tenant public IP and also for the default gateway on
// the SF route.
func xdpSetupNSForInterface(allowedIPs []string, podMAC string, vlanID uint, setup bool) error {

	op := "Setting up"
	if !setup {
		op = "Tearing Down"
	}
	klog.Infof("%s XDP routes and MAC for %v", op, allowedIPs)
	netns, err := ns.GetNS(xdpNSPath)
	if err != nil {
		return fmt.Errorf("error opening XDP NS %s, error: %v", xdpNSPath, err)
	}
	defer netns.Close()

	if xdpVethDev == "" {
		return fmt.Errorf("error getting veth interface for XDP")
	}
	if xdpSFDev == "" {
		return fmt.Errorf("error getting SF interface for XDP")
	}

	err = netns.Do(func(hostNS ns.NetNS) error {

		// Get veth link
		vethLink, err := netlink.LinkByName(xdpVethDev)
		if err != nil {
			return fmt.Errorf("error getting link for %s:%v", xdpVethDev, err)
		}

		// Use the route table for this subnet/vlan.
		table_no := int(vlanID)

		if setup {
			// And the public tenant IP via the veth.
			for _, allowedIP := range allowedIPs {
				tenantIP := net.ParseIP(allowedIP)
				tenant_ip := &net.IPNet{
					IP:   tenantIP,
					Mask: net.CIDRMask(32, 32),
				}
				s_rule := netlink.NewRule()
				s_rule.Table = table_no
				s_rule.Src = tenant_ip
				route := netlink.Route{LinkIndex: vethLink.Attrs().Index, Dst: tenant_ip}

				if err = netlink.RuleAdd(s_rule); err != nil && !os.IsExist(err) {
					return fmt.Errorf("error adding rule for %s:%v", allowedIP, err)
				}

				if err = netlink.RouteAdd(&route); err != nil && !os.IsExist(err) {
					return fmt.Errorf("error adding route cidr %s: %v", tenantIP, err)
				}

				// Set the ARP entries.

				// Tenant IP on the host
				cmd := exec.Command("arp", "-s", allowedIP, podMAC, "dev", xdpVethDev)
				err = cmd.Run()
				if err != nil {
					return fmt.Errorf("xdp:Error adding arp for %s, %s:%v", allowedIP, podMAC, err)
				}
			}
			klog.Infof("Set up XDP routes and MAC for %v", allowedIPs)
			return nil
		}
		for _, allowedIP := range allowedIPs {
			tenantIP := net.ParseIP(allowedIP)
			tenant_ip := &net.IPNet{
				IP:   tenantIP,
				Mask: net.CIDRMask(32, 32),
			}
			s_rule := netlink.NewRule()
			s_rule.Table = table_no
			s_rule.Src = tenant_ip
			route := netlink.Route{LinkIndex: vethLink.Attrs().Index, Dst: tenant_ip}

			if err = netlink.RuleDel(s_rule); err != nil {
				return fmt.Errorf("error deleting rule for %s:%v", allowedIP, err)
			}

			if err = netlink.RouteDel(&route); err != nil {
				return fmt.Errorf("xdp:Error deleting route cidr %s: %v", tenantIP, err)
			}

			// Delete the ARP entries.

			// Tenant IP on the host
			cmd := exec.Command("arp", "-d", allowedIP, "dev", xdpVethDev)
			err = cmd.Run()
			if err != nil {
				return fmt.Errorf("xdp:Error deleting arp for %s, %s:%v", allowedIP, podMAC, err)
			}
		}
		klog.Infof("Completed %s XDP routes and MAC for %v", strings.ToLower(op), allowedIPs)
		return nil
	})
	return err
}

// Initialize XDP services, mainly
// - Routing table for the subnet/VLAN and ARP configuration for the def gateway
// - OF rules on the network side
func (nc *SecondaryLocalnetNodeNetworkController) initializeXDPServiceForNAD(xdpNS string, setup bool) error {

	op := "Setting up"
	if !setup {
		op = "Tearing Down"
	}
	klog.Infof("%s XDP NS configuration for %s", op, nc.GetNetworkName())
	err := nc.xdpSetupNSForNAD(xdpNS, setup)
	if err != nil {
		klog.Errorf(err.Error())
		return err
	}

	klog.Infof("Completed %s XDP routes Flows", strings.ToLower(op))
	return nil
}

// Set up the XDP services, mainly
// - Routing and ARP configuration in the XDP service namespace
// - OF rules in the bridge to insert the XDP service for TCP.
func (nc *SecondaryLocalnetNodeNetworkController) setXDPServiceForInterface(xdpInfo *XDPInfo, setup bool) error {
	op := "Setting up"
	if !setup {
		op = "Tearing Down"
	}
	xdpNS := config.OvnKubeNode.XDPNamespace
	klog.Infof("%s XDP NS for %v", op, nc.Subnets())
	err := xdpSetupNSForInterface(xdpInfo.allowedIPs, xdpInfo.mac, nc.Vlan(), setup)
	if err != nil {
		klog.Errorf("Error %s NS %s, for %v, %s, %s, %s, %v:%v", xdpNS, strings.ToLower(op),
			xdpInfo.allowedIPs, xdpInfo.mac,
			nc.Gateway(), nc.GatewayMAC(), nc.Subnets(), err)
		return err
	}

	klog.Infof("%s XDP OF Flows", op)
	err = xdpSetupOFFlowsForInterface(xdpInfo.allowedIPs, nc.bridgeName,
		nc.Vlan(), xdpInfo.mac, nc.gateway.(*gateway), setup)
	if err != nil {
		klog.Errorf("Error %s OF flows for %v, %d on %s: %v", strings.ToLower(op),
			nc.Subnets(), nc.Vlan(), nc.bridgeName, err)
		return err
	}
	klog.Infof("Completed %s XDP routes/OF Flows", strings.ToLower(op))
	return nil
}

func (nc *SecondaryLocalnetNodeNetworkController) InitializeXDPServiceForNAD() error {

	klog.Infof("Initializing XDP service for network %s", nc.GetNetworkName())
	err := nc.initializeXDPServiceForNAD(config.OvnKubeNode.XDPNamespace, true)
	if err != nil {
		return fmt.Errorf("error initializing XDP:%v", err)
	}
	klog.Infof("XDP service initialized")
	return nil
}

func (nc *SecondaryLocalnetNodeNetworkController) UpdateXDPServiceForInterface(oldXDPInfo, newXDPInfo *XDPInfo) error {

	// We deal with the flows first so that there is no gap where xdp could be bypassed for this pod.
	// It is ok, to do flows before updating the NS with the IPs, since connections might not proceed,
	// and tcp will rexmit.

	// Add new flows
	err := xdpSetupOFFlowsForInterface(newXDPInfo.allowedIPs, nc.bridgeName,
		nc.Vlan(), newXDPInfo.mac, nc.gateway.(*gateway), true)
	if err != nil {
		// This is problematic, maybe better to panic
		klog.Errorf("Error adding new OF flows for %v, %v on %s: %v",
			nc.Subnets(), nc.Vlan(), nc.bridgeName, err)
		return err
	}
	// Remove old flows
	err = xdpSetupOFFlowsForInterface(oldXDPInfo.allowedIPs, nc.bridgeName,
		nc.Vlan(), oldXDPInfo.mac, nc.gateway.(*gateway), false)
	if err != nil {
		// This is not problematic, per se, so the old flows could be left behind.
		klog.Errorf("Error deleting new OF flows for %s, %d on %s: %v",
			nc.Subnets(), nc.Vlan(), nc.bridgeName, err)
	}

	// Remove old IP info. We remove old before adding new, since there may be an overlap,
	// so adding and then deleting, might delete the overlapping ips.
	err = xdpSetupNSForInterface(oldXDPInfo.allowedIPs, oldXDPInfo.mac, nc.Vlan(), false)
	if err != nil {
		klog.Errorf("Error deleting old IPs from NS %s for %v, %s, %v:%v",
			config.OvnKubeNode.XDPNamespace, oldXDPInfo.allowedIPs, oldXDPInfo.mac,
			nc.Subnets(), err)
		return err
	}

	// Add new IP info.
	err = xdpSetupNSForInterface(newXDPInfo.allowedIPs, newXDPInfo.mac, nc.Vlan(), true)
	if err != nil {
		klog.Errorf("Error adding old IPs from NS %s for %v, %s, %v:%v",
			config.OvnKubeNode.XDPNamespace, newXDPInfo.allowedIPs, newXDPInfo.mac,
			nc.Subnets(), err)
		return err
	}
	klog.Infof("XDP service updated")
	return nil
}

func (nc *SecondaryLocalnetNodeNetworkController) DestroyXDPServiceForNAD() error {
	klog.Infof("Destroying up XDP service for network %s", nc.GetNetworkName())
	err := nc.initializeXDPServiceForNAD(config.OvnKubeNode.XDPNamespace, false)
	if err != nil {
		return fmt.Errorf("error destroying XDP: %v", err)
	}
	klog.Infof("XDP service destroyed")
	return nil
}
