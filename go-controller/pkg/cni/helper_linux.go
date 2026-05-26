// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux
// +build linux

package cni

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/containernetworking/plugins/pkg/ip"
	"github.com/containernetworking/plugins/pkg/ns"
	"github.com/safchain/ethtool"
	"github.com/vishvananda/netlink"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	"sigs.k8s.io/knftables"

	ovncnitypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/cni/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

func addRoute(ipn *net.IPNet, gw net.IP, dev netlink.Link, mtu, table int) error {
	return util.GetNetLinkOps().RouteAdd(&netlink.Route{
		LinkIndex: dev.Attrs().Index,
		Scope:     netlink.SCOPE_UNIVERSE,
		Dst:       ipn,
		Gw:        gw,
		MTU:       mtu,
		Table:     table,
	})
}

func replaceRouteECMP(ipn *net.IPNet, gw net.IP, devs []netlink.Link, mtu int) error {
	ecmpRoute := &netlink.Route{
		Dst: ipn,
		MTU: mtu,
	}
	ecmpRoute.MultiPath = make([]*netlink.NexthopInfo, len(devs))
	for i, dev := range devs {
		ecmpRoute.MultiPath[i] = &netlink.NexthopInfo{
			LinkIndex: dev.Attrs().Index,
			Gw:        gw,
			Hops:      0, // Weight (0 means weight of 1)
		}
	}
	return util.GetNetLinkOps().RouteReplace(ecmpRoute)
}

// This is a good value that allows fast streams of small packets to be aggregated,
// without introducing noticeable latency in slower traffic.
const udpPacketAggregationTimeout = 50 * time.Microsecond

var udpPacketAggregationTimeoutBytes = []byte(fmt.Sprintf("%d\n", udpPacketAggregationTimeout.Nanoseconds()))

var defaultVFHardwareAddress = make(net.HardwareAddr, 6)

// sets up the host side of a veth for UDP packet aggregation
func setupVethUDPAggregationHost(ifname string) error {
	e, err := ethtool.NewEthtool()
	if err != nil {
		return fmt.Errorf("failed to initialize ethtool: %v", err)
	}
	defer e.Close()

	err = e.Change(ifname, map[string]bool{
		"rx-gro":                true,
		"rx-udp-gro-forwarding": true,
	})
	if err != nil {
		return fmt.Errorf("could not enable interface features: %v", err)
	}
	channels, err := e.GetChannels(ifname)
	if err == nil {
		channels.RxCount = uint32(runtime.NumCPU())
		_, err = e.SetChannels(ifname, channels)
	}
	if err != nil {
		return fmt.Errorf("could not update channels: %v", err)
	}

	timeoutFile := fmt.Sprintf("/sys/class/net/%s/gro_flush_timeout", ifname)
	err = os.WriteFile(timeoutFile, udpPacketAggregationTimeoutBytes, 0644)
	if err != nil {
		return fmt.Errorf("could not set flush timeout: %v", err)
	}

	return nil
}

// sets up the container side of a veth for UDP packet aggregation
func setupVethUDPAggregationContainer(ifname string) error {
	e, err := ethtool.NewEthtool()
	if err != nil {
		return fmt.Errorf("failed to initialize ethtool: %v", err)
	}
	defer e.Close()

	channels, err := e.GetChannels(ifname)
	if err == nil {
		channels.TxCount = uint32(runtime.NumCPU())
		_, err = e.SetChannels(ifname, channels)
	}
	if err != nil {
		return fmt.Errorf("could not update channels: %v", err)
	}

	return nil
}

func setSysctl(sysctl string, newVal int) error {
	return os.WriteFile(sysctl, []byte(strconv.Itoa(newVal)), 0o640)
}

// safely move the netdev to the pod namespace, making sure to avoid name conflicts
func safeMoveIfToNetns(ifname string, netns ns.NetNS, containerID string) (newNetdeviceName string, err error) {
	newNetdeviceName = ifname
	err = moveIfToNetns(ifname, netns)

	if err != nil {
		if strings.Contains(err.Error(), "file exists") {
			// netdev with the same name exists in the pod
			newNetdeviceName = generateIfName(containerID)
			err = util.RenameLink(ifname, newNetdeviceName)
			if err != nil {
				return ifname, err
			}
			err = moveIfToNetns(newNetdeviceName, netns)
			if err != nil {
				return ifname, err
			}
		} else {
			return ifname, err
		}
	}
	return newNetdeviceName, nil
}

func moveIfToNetns(ifname string, netns ns.NetNS) error {
	dev, err := util.GetNetLinkOps().LinkByName(ifname)
	if err != nil {
		return fmt.Errorf("failed to lookup device %v: %q", ifname, err)
	}

	// move netdevice to ns
	if err = util.GetNetLinkOps().LinkSetNsFd(dev, int(netns.Fd())); err != nil {
		return fmt.Errorf("failed to move device %+v to netns: %q", ifname, err)
	}

	return nil
}

func setupNetwork(link netlink.Link, ifInfo *PodInterfaceInfo) error {
	// make sure link is up
	if link.Attrs().Flags&net.FlagUp == 0 {
		if err := util.GetNetLinkOps().LinkSetUp(link); err != nil {
			return fmt.Errorf("failed to set up interface %s: %v", link.Attrs().Name, err)
		}
	}

	if ifInfo.SkipIPConfig {
		klog.Infof("Skipping network configuration for pod: %s", ifInfo.PodUID)
		return nil
	}

	// set the IP address
	for _, ip := range ifInfo.IPs {
		addr := &netlink.Addr{IPNet: ip}
		if err := util.GetNetLinkOps().AddrAdd(link, addr); err != nil {
			return fmt.Errorf("failed to add IP addr %s to %s: %v", ip, link.Attrs().Name, err)
		}
	}
	for _, gw := range ifInfo.Gateways {
		if err := addRoute(nil, gw, link, ifInfo.RoutableMTU, 0); err != nil {
			return fmt.Errorf("failed to add gateway route to link '%s': %v", link.Attrs().Name, err)
		}
	}

	// all pod links of the same secondary UDN up to the current one
	var links []netlink.Link
	var iptableNum int

	if len(ifInfo.PodIfNamesOfSameNAD) != 0 {
		// this is a pod with multiple interfaces of the same non-primary UDN, we need to configure the following sysctl configuration
		// stop ARP flux and stop RP filter drop from happening
		//   sysctl -w net.ipv4.conf.<linkName>.arp_ignore=1 # only reply to ARP requests if its sent to the IP on this interface
		//   sysctl -w net.ipv4.conf.<linkName>.arp_announce=2 # when sending ARP use the IP that belongs to this interface
		//   sysctl -w net.ipv4.conf.<linkName>.rp_filter=2 # allow reverse path filter check to pass if there is any route to the source
		linkName := link.Attrs().Name
		if err := setSysctl(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/arp_ignore", linkName), 1); err != nil {
			return fmt.Errorf("failed to set arp_ignore for %s: %v", linkName, err)
		}
		if err := setSysctl(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/arp_announce", linkName), 2); err != nil {
			return fmt.Errorf("failed to set arp_announce for %s: %v", linkName, err)
		}
		if err := setSysctl(fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/rp_filter", linkName), 2); err != nil {
			return fmt.Errorf("failed to set rp_filter for %s: %v", linkName, err)
		}

		if len(ifInfo.Routes) != 0 {
			// needs ip table for each Pod interface of the same secondary UDN, table number start from 100
			iptableNum = 100 + link.Attrs().Index

			// ECMP routes are needed
			// get all the Pod interface names of the same nadName
			// up to the current link name. This is used for configuring ECMP routes via multiple pod interfaces
			links = make([]netlink.Link, 0, len(ifInfo.PodIfNamesOfSameNAD))
			for _, ifName := range ifInfo.PodIfNamesOfSameNAD {
				if ifName == link.Attrs().Name {
					// loop all pod interfaces of the same secondary UDN until the current pod interface
					links = append(links, link)
					break
				}
				ifLink, err := util.GetNetLinkOps().LinkByName(ifName)
				if err != nil {
					return fmt.Errorf("failed to lookup pod interface %s when setup route for link %s: %v", ifName, link.Attrs().Name, err)
				}
				links = append(links, ifLink)
			}

			// ip rules are needed to force traffic from an ip to egress the correct interface via a route table for that interface:
			//   ip rule add from <IP_on_this_interface> table <iptableNum>
			for _, ip := range ifInfo.IPs {
				rule := netlink.NewRule()
				rule.Src = util.GetIPNetFullMaskFromIP(ip.IP)
				rule.Table = iptableNum
				if err := util.GetNetLinkOps().RuleAdd(rule); err != nil {
					return fmt.Errorf("failed to add IP rule for %s to table %d: %v", ip.IP, iptableNum, err)
				}

				// add scope link route to the specific table
				route := &netlink.Route{
					LinkIndex: link.Attrs().Index,
					Scope:     netlink.SCOPE_LINK,
					Dst:       util.IPsToNetworkIPs(ip)[0],
					Table:     iptableNum,
				}
				if err := util.GetNetLinkOps().RouteAdd(route); err != nil && !os.IsExist(err) {
					return fmt.Errorf("failed to add link scope route to %v via %v to table %v: %v", ip, linkName, iptableNum, err)
				}
			}
		}
	}

	for _, route := range ifInfo.Routes {
		if len(ifInfo.PodIfNamesOfSameNAD) == 0 {
			// if there is no other interface of the same NAD, just add route directly
			if err := addRoute(route.Dest, route.NextHop, link, ifInfo.RoutableMTU, 0); err != nil && !os.IsExist(err) {
				return fmt.Errorf("failed to add pod route %v via %v: %v", route.Dest, route.NextHop, err)
			}
		} else {
			if len(links) == 1 {
				// if this is the first pod interface of this NAD, just add route directly
				if err := addRoute(route.Dest, route.NextHop, link, ifInfo.RoutableMTU, 0); err != nil && !os.IsExist(err) {
					return fmt.Errorf("failed to add pod route %v via %v: %v", route.Dest, route.NextHop, err)
				}
			} else {
				// otherwise replace it with ECMP routes
				if err := replaceRouteECMP(route.Dest, route.NextHop, links, ifInfo.RoutableMTU); err != nil {
					return fmt.Errorf("failed to replace pod route %v via %v through links %v: %v", route.Dest, route.NextHop, links, err)
				}
			}

			// add ECMP route to specific IP route table
			if err := addRoute(route.Dest, route.NextHop, link, ifInfo.RoutableMTU, iptableNum); err != nil && !os.IsExist(err) {
				return fmt.Errorf("failed to add pod route %v nexthop %v via %v table %v: %v", route.Dest, route.NextHop, link.Attrs().Name, iptableNum, err)
			}
		}
	}

	return nil
}

func setupInterface(netns ns.NetNS, containerID, ifName string, ifInfo *PodInterfaceInfo) (*current.Interface, *current.Interface, error) {
	hostIface := &current.Interface{}
	contIface := &current.Interface{}
	ifnameSuffix := ""

	var oldHostVethName string
	err := netns.Do(func(hostNS ns.NetNS) error {
		// create the veth pair in the container and move host end into host netns
		// set host interface name now for default network as it is already known; otherwise for secondary network,
		// host interface will be renamed later.
		if ifInfo.NetName == types.DefaultNetworkName {
			hostIface.Name = containerID[:15]
		} else {
			hostIface.Name = ""
		}
		contIface.Mac = ifInfo.MAC.String()
		hostVeth, containerVeth, err := ip.SetupVethWithName(ifName, hostIface.Name, ifInfo.MTU, contIface.Mac, hostNS)
		if err != nil {
			return err
		}
		hostIface.Mac = hostVeth.HardwareAddr.String()
		contIface.Name = containerVeth.Name

		link, err := util.GetNetLinkOps().LinkByName(contIface.Name)
		if err != nil {
			return fmt.Errorf("failed to lookup %s: %v", contIface.Name, err)
		}

		err = setupNetwork(link, ifInfo)
		if err != nil {
			return err
		}
		contIface.Sandbox = netns.Path()

		if ifInfo.EnableUDPAggregation {
			err = setupVethUDPAggregationContainer(contIface.Name)
			if err != nil {
				return fmt.Errorf("could not enable UDP packet aggregation in container: %v", err)
			}
		}

		oldHostVethName = hostVeth.Name

		// to generate the unique host interface name, postfix it with the podInterface index for non-default network
		if ifInfo.NetName != types.DefaultNetworkName {
			ifnameSuffix = fmt.Sprintf("_%d", containerVeth.Index)
		}

		// If we have the ipv6 gateway LLA then this is a primary layer2 UDN
		if len(ifInfo.GatewayIPv6LLA) > 0 {
			if err = setupIngressFilter(ifName, ifInfo.GatewayIPv6LLA.String()); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	// rename the host end of veth pair for the secondary network
	if ifInfo.NetName != types.DefaultNetworkName {
		hostIface.Name = containerID[:(15-len(ifnameSuffix))] + ifnameSuffix
		if err := util.RenameLink(oldHostVethName, hostIface.Name); err != nil {
			return nil, nil, fmt.Errorf("failed to rename %s to %s: %v", oldHostVethName, hostIface.Name, err)
		}
	}

	if ifInfo.EnableUDPAggregation {
		err = setupVethUDPAggregationHost(hostIface.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("could not enable UDP packet aggregation on host veth interface %q: %v", hostIface.Name, err)
		}
	}

	return hostIface, contIface, nil
}

// generate a unique interface name for the temporary netdev that will be moved to pod namespace
func generateIfName(containerID string) string {
	randomId := util.GenerateId(5) // random ID with 5 chars
	// ifname max length is 15
	return containerID[:(15-len(randomId))] + randomId
}

// Setup sriov interface in the pod
func setupSriovInterface(netns ns.NetNS, containerID, ifName string, ifInfo *PodInterfaceInfo, deviceID string, isVFIO bool) (*current.Interface, *current.Interface, error) {
	hostIface := &current.Interface{}
	contIface := &current.Interface{}

	netdevice := ifInfo.NetdevName

	// 0. init contIface for VFIO
	if isVFIO {
		if util.IsAuxDeviceName(deviceID) {
			return nil, nil, fmt.Errorf("VFIO not supported for device %s", deviceID)
		}
		// if the SR-IOV device is bound to VFIO, then there is nothing
		// to do as it will be passed to the KVM VM directly
		contIface.Name = ifName
		contIface.Mac = ifInfo.MAC.String()
		contIface.Sandbox = netns.Path()
	} else {
		if len(netdevice) != 0 {
			// 1. Move netdevice to Container namespace
			newNetdevName, err := safeMoveIfToNetns(netdevice, netns, containerID)
			if err != nil {
				return nil, nil, err
			}
			err = netns.Do(func(_ ns.NetNS) error {
				contIface.Name = ifName
				err = util.RenameLink(newNetdevName, contIface.Name)
				if err != nil {
					return err
				}
				link, err := util.GetNetLinkOps().LinkByName(contIface.Name)
				if err != nil {
					return err
				}
				err = util.GetNetLinkOps().LinkSetHardwareAddr(link, ifInfo.MAC)
				if err != nil {
					return err
				}
				err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU)
				if err != nil {
					return err
				}
				err = util.GetNetLinkOps().LinkSetUp(link)
				if err != nil {
					return err
				}

				err = setupNetwork(link, ifInfo)
				if err != nil {
					return err
				}

				contIface.Mac = ifInfo.MAC.String()
				contIface.Sandbox = netns.Path()

				return nil
			})
			if err != nil {
				return nil, nil, err
			}
		}
	}

	if !ifInfo.IsDPUHostMode {
		// 2. get device representor name
		hostRepName, err := util.GetFunctionRepresentorName(deviceID)
		if err != nil {
			return nil, nil, err
		}
		hostIface.Name = hostRepName

		// 3. For KubeVirt, the VFIO is assigned directly and the MAC address
		// cannot be configured from within the guest netns. For Kata Containers,
		// ovn-k8s initially configures the VF in the Pod network namespace, but the
		// runtime later rebinds the VF to VFIO. In both cases, the MAC address
		// must be programmed through the VF representor (PCI device IDs only).
		if util.IsPCIDeviceName(deviceID) {
			if err := util.SetVFHardwreAddress(deviceID, ifInfo.MAC); err != nil {
				return nil, nil, err
			}
		}

		link, err := util.GetNetLinkOps().LinkByName(hostIface.Name)
		if err != nil {
			return nil, nil, err
		}

		hostIface.Mac = link.Attrs().HardwareAddr.String()
		// Do not bring the representor up or set MTU here. In full mode the representor must be
		// added to br-int first, then brought up (and set MTU) in ConfigureOVS after the port
		// is added to br-int. This avoids a race where an old pod's CmdDel could bring down the
		// same VF representor and remove it from br-int after we brought the link up but before
		// we added it to br-int.
	}
	return hostIface, contIface, nil
}

func getPfEncapIP(deviceID string) (string, error) {
	stdout, err := ovsGet("Open_vSwitch", ".", "external_ids", "ovn-pf-encap-ip-mapping")
	if err != nil {
		return "", fmt.Errorf("failed to get ovn-pf-encap-ip-mapping, error: %v", err)
	}

	if len(stdout) == 0 {
		return "", nil
	}

	encapIpMapping := map[string]string{}
	mappings := strings.Split(stdout, ",")
	for _, mapping := range mappings {
		tokens := strings.Split(mapping, ":")
		if len(tokens) != 2 {
			return "", fmt.Errorf("bad ovn-pf-encap-ip-mapping config: %s", stdout)
		}

		encapIpMapping[tokens[0]] = tokens[1]
	}

	uplinkRepName, err := util.GetUplinkRepresentorName(deviceID)
	if err != nil {
		// FIXME(leih): unlikely to happen, treat this as a valid case and ignore for now.
		klog.Errorf("Failed to get uplink representor for VF PCI address %s: %v",
			deviceID, err)
		return "", nil
	}

	encapIP := encapIpMapping[uplinkRepName]
	return encapIP, nil
}

// ValidateOVSInterfaceConfiguration checks if the OVS port is configured correctly
func ValidateOVSInterfaceConfiguration(namespace, podName, hostIfaceName string, ifInfo *PodInterfaceInfo) error {
	ifaceID := util.GetIfaceId(namespace, podName)
	if ifInfo.NetName != types.DefaultNetworkName { // UDN network
		ifaceID = util.GetUDNIfaceId(namespace, podName, ifInfo.NADKey)
	}
	// if the specified port was created for other Pod/NAD, return error
	extIds, err := ovsFind("Interface", "external_ids", "name="+hostIfaceName)
	if err == nil && len(extIds) == 1 {
		extId := extIds[0]
		ifaceIDStr := util.GetExternalIDValByKey(extId, "iface-id")
		nadKeyString := util.GetExternalIDValByKey(extId, types.NADExternalID)
		// if NADExternalID does not exist, it is default network
		if nadKeyString == "" {
			nadKeyString = types.DefaultNetworkName
		}
		if ifaceIDStr != ifaceID {
			return fmt.Errorf("OVS port %s was added for iface-id (%s), now readding it for (%s)", hostIfaceName, ifaceIDStr, ifaceID)
		}
		if nadKeyString != ifInfo.NADKey {
			return fmt.Errorf("OVS port %s was added for NAD key (%s), expect (%s)", hostIfaceName, nadKeyString, ifInfo.NADKey)
		}
	}
	return nil
}

// ConfigureOVS performs OVS configurations in order to set up Pod networking
func ConfigureOVS(ctx context.Context, namespace, podName, podIfName, hostIfaceName string,
	ifInfo *PodInterfaceInfo, sandboxID, deviceID string, isVFIO bool, getter PodInfoGetter) error {

	ifaceID := util.GetIfaceId(namespace, podName)
	if ifInfo.NetName != types.DefaultNetworkName {
		ifaceID = util.GetUDNIfaceId(namespace, podName, ifInfo.NADKey)
	}

	initialPodUID := ifInfo.PodUID
	ipStrs := make([]string, len(ifInfo.IPs))
	for i, ip := range ifInfo.IPs {
		ipStrs[i] = ip.String()
	}

	br_type, err := getDatapathType("br-int")
	if err != nil {
		return fmt.Errorf("failed to get datapath type for bridge br-int : %v", err)
	}

	klog.Infof("ConfigureOVS: namespace: %s, podName: %s, hostIfaceName: %s, network: %s, NAD %s, SandboxID: %q, PCI device ID: %s, UID: %q, MAC: %s, IPs: %v, ifaceID: %s, ovn_kube_node: %s",
		namespace, podName, hostIfaceName, ifInfo.NetName, ifInfo.NADKey, sandboxID, deviceID, initialPodUID, ifInfo.MAC, ipStrs, ifaceID, ifInfo.OvnKubeMode)

	// Find and remove any existing OVS port with this iface-id. Pods can
	// have multiple sandboxes if some are waiting for garbage collection,
	// but only the latest one should have the iface-id set.
	names, _ := ovsFind("Interface", "name", "external-ids:iface-id="+ifaceID)
	for _, name := range names {
		if name == hostIfaceName {
			// this may be result of restarting ovnkube-node, and it is trying to add the same VF representor to
			// br-int for the same pod; do not delete port in this case.
			continue
		}
		if out, err := ovsExec("--with-iface", "del-port", "br-int", name); err != nil {
			klog.Warningf("Failed to delete stale OVS port %q with iface-id %q from br-int: %v\n %q",
				name, ifaceID, err, out)
		}
	}

	// if the specified port was created for other Pod/NAD, return error
	err = ValidateOVSInterfaceConfiguration(namespace, podName, hostIfaceName, ifInfo)
	if err != nil {
		return err
	}

	// Add the new sandbox's OVS port, tag the port as transient so stale
	// pod ports are scrubbed on hard reboot
	ovsArgs := []string{
		"--may-exist", "add-port", "br-int", hostIfaceName, "other_config:transient=true",
		"--", "set", "interface", hostIfaceName,
		fmt.Sprintf("external_ids:attached_mac=%s", ifInfo.MAC),
		fmt.Sprintf("external_ids:iface-id=%s", ifaceID),
		fmt.Sprintf("external_ids:iface-id-ver=%s", initialPodUID),
		fmt.Sprintf("external_ids:sandbox=%s", sandboxID),
	}

	// pod interface name, used to identify CNI request with the same NAD
	if podIfName != "" {
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:pod-if-name=%s", podIfName))
	}

	// In case of multi-vtep, host has multipe NICs and each NIC has a VTEP interface, the mapping
	// of VTEP IP to NIC is stored in Open_vSwitch table's `external_ids:ovn-pf-encap-ip-mapping`,
	// the value's format is:
	//   enp1s0f0:<vtep-ip1>,enp193s0f0:<vtep-ip2>,enp197s0f0:<vtep-ip3>
	// Here configure the OVS Interface's encap-ip according to the mapping.
	if deviceID != "" {
		encapIP, err := getPfEncapIP(deviceID)
		if err != nil {
			return err
		}
		if len(encapIP) > 0 {
			ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:encap-ip=%s", encapIP))
		}
	}

	// IP address might be empty, or IPAM is optional for secondary flatL2 networks; thus, the ifaces may not
	// have IP addresses.
	if len(ifInfo.IPs) > 0 {
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:ip_addresses=%s", strings.Join(ipStrs, ",")))
	}
	if ifInfo.OvnKubeMode != "" {
		// Since ovn-kube can run in full mode and dpu mode, we need to mark the type in order to know
		// which owns what resource
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:ovn_kube_mode=%s", ifInfo.OvnKubeMode))
	}

	if br_type == types.DatapathUserspace {
		_, err := util.GetSriovnetOps().GetRepresentorPortFlavour(hostIfaceName)
		if err != nil {
			// The error is not important: the given port is not a switchdev one and won't
			// be used with DPDK. It can happen for legitimate reason. Keep a trace of the
			// event and continue configuring OVS.
			klog.Infof("Port %s cannot be used with DPDK, will use netlink interface in OVS",
				hostIfaceName)
		} else {
			dpdkArgs := []string{"type=dpdk"}
			ovsArgs = append(ovsArgs, dpdkArgs...)
			ovsArgs = append(ovsArgs, fmt.Sprintf("mtu_request=%v", ifInfo.MTU))
		}
	}

	if len(ifInfo.NetdevName) != 0 {
		// NOTE: For SF representor same external_id is used due to https://github.com/ovn-kubernetes/ovn-kubernetes/pull/3054
		// Review this line when upgrade mechanism will be implemented
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:netdev-name=%s", ifInfo.NetdevName))
	}
	if isVFIO {
		// VFIO case
		ovsArgs = append(ovsArgs, "external_ids:vf-is-vfio=true")
	}

	if ifInfo.NetName != types.DefaultNetworkName {
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:%s=%s", types.NetworkExternalID, ifInfo.NetName))
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:%s=%s", types.NADExternalID, ifInfo.NADKey))
		// keep the lagacy external-ids for now to help with potential ovn-k8s downgrade
		ovsArgs = append(ovsArgs, fmt.Sprintf("external_ids:%s=%s", types.LegacyNetworkExternalID, ifInfo.NADKey))
	} else {
		ovsArgs = append(ovsArgs, []string{"--", "--if-exists", "remove", "interface", hostIfaceName, "external_ids", types.NetworkExternalID}...)
		ovsArgs = append(ovsArgs, []string{"--", "--if-exists", "remove", "interface", hostIfaceName, "external_ids", types.NADExternalID}...)
		ovsArgs = append(ovsArgs, []string{"--", "--if-exists", "remove", "interface", hostIfaceName, "external_ids", types.LegacyNetworkExternalID}...)
	}

	// add ovs port with 3 retries every 100 ms
	backoff := wait.Backoff{
		Duration: 100 * time.Millisecond,
		Steps:    3,
	}
	hwOffloadEnabled, err := isHWOffloadEnabled()
	if err != nil {
		return fmt.Errorf("error checking hw-offload flag: %v", err)
	}
	var link netlink.Link
	if link, err = util.GetNetLinkOps().LinkByName(hostIfaceName); err != nil {
		return fmt.Errorf("failed to find interface %s: %v", hostIfaceName, err)
	}
	err = retry.OnError(backoff, func(e error) bool {
		klog.Errorf("Failed to add port %s to br-int: %v", hostIfaceName, e)
		delPortArgs := []string{
			"--if-exists", "del-port", "br-int", hostIfaceName,
		}
		if out, err := ovsExec(delPortArgs...); err != nil {
			klog.Errorf("Fail to delete pod interface %s, stdout: %s, error: %v", hostIfaceName, out, err)
			// couldn't clean up port, stop retrying
			return false
		}
		return true // retry
	}, func() error {
		if out, err := ovsExec(ovsArgs...); err != nil {
			return fmt.Errorf("failure in plugging pod interface %s: stdout: %q, error: %v", hostIfaceName, out, err)
		}
		if !hwOffloadEnabled {
			klog.V(7).Info("Skip offload verification since hw-offload is not enabled")
			return nil
		}
		if br_type == types.DatapathUserspace {
			klog.V(7).Info("Skip offload verification since the link is on a 'netdev' datapath")
			return nil
		}
		// to make sure offload is enabled for the interface, look for tc ingress
		// filters for the link, return error if no rules are found
		if numOfIngress, err := util.GetNetLinkOps().CountIngressFilters(link); err != nil {
			return fmt.Errorf("failed to find ingress filter for %s: %v", hostIfaceName, err)
		} else if numOfIngress == 0 {
			return fmt.Errorf("ingress filters for %s not found", hostIfaceName)
		} else {
			klog.V(5).Infof("Found %d ingress filter(s) for %s", numOfIngress, hostIfaceName)
			return nil
		}
	})
	if err != nil {
		return err
	}

	if err := clearPodBandwidth(sandboxID); err != nil {
		return err
	}

	if deviceID != "" {
		// 4. set MTU on the representor
		if err = util.GetNetLinkOps().LinkSetMTU(link, ifInfo.MTU); err != nil {
			return fmt.Errorf("failed to set MTU on %s: %v", hostIfaceName, err)
		}
		// 5. if the interface is not up, set it to up
		err = util.GetNetLinkOps().LinkSetUp(link)
		if err != nil {
			return fmt.Errorf("failed to set link UP on %s: %v", hostIfaceName, err)
		}
	}

	if ifInfo.Ingress > 0 || ifInfo.Egress > 0 {
		err = netlink.LinkSetTxQLen(link, 1000)
		if err != nil {
			return fmt.Errorf("failed to set host veth txqlen: %v", err)
		}

		if err := setPodBandwidth(sandboxID, hostIfaceName, ifInfo.Ingress, ifInfo.Egress); err != nil {
			return err
		}
	}

	if err = waitForPodInterface(ctx, ifInfo, hostIfaceName, ifaceID, getter,
		namespace, podName, initialPodUID); err != nil {
		// Ensure the error shows up in node logs, rather than just
		// being reported back to the runtime.
		klog.Warningf("[%s/%s %s] pod uid %s: %v", namespace, podName, sandboxID, initialPodUID, err)
		return err
	}
	return nil
}

type PodRequestInterfaceOps interface {
	ConfigureInterface(pr *PodRequest, getter PodInfoGetter, ifInfo *PodInterfaceInfo) ([]*current.Interface, error)
	UnconfigureInterface(pr *PodRequest, ifInfo *PodInterfaceInfo) error
}

type defaultPodRequestInterfaceOps struct{}

var podRequestInterfaceOps PodRequestInterfaceOps = &defaultPodRequestInterfaceOps{}

func getDanConfigPath(danConfigDir, sandboxId string) string {
	return filepath.Join(danConfigDir, sandboxId+".json")
}

func writeDanConfig(danConfigPath string, dan *ovncnitypes.DanConfig) error {

	var curDanConfig *ovncnitypes.DanConfig
	if _, err := os.Stat(danConfigPath); errors.Is(err, os.ErrNotExist) {
		curDanConfig = dan
	} else {
		curDanConfig = &ovncnitypes.DanConfig{}
		data, err := os.ReadFile(danConfigPath)
		if err != nil {
			return fmt.Errorf("failed to read Kata DAN config: %s", danConfigPath)
		}
		if err = json.Unmarshal(data, curDanConfig); err != nil {
			return fmt.Errorf("failed to parse Kata DAN config: %s", danConfigPath)
		}

		curDanConfig.Devices = append(curDanConfig.Devices, dan.Devices...)
	}

	jsonData, err := json.MarshalIndent(curDanConfig, "", "  ")
	if err != nil {
		return err
	}

	err = os.WriteFile(danConfigPath, jsonData, 0600)
	if err != nil {
		return fmt.Errorf("failed to write DAN config file %s, error: %v", danConfigPath, err)
	}

	return nil
}

func (pr *PodRequest) generateDanConfig(ifInfo *PodInterfaceInfo, pciDeviceID string) error {
	klog.V(5).Infof("Generate DAN config for pod %s/%s on network %s NAD %s",
		pr.PodNamespace, pr.PodName, pr.netName, pr.nadName)

	var ipAddrs []string
	var routes []ovncnitypes.Route

	for _, a := range ifInfo.IPs {
		ipAddrs = append(ipAddrs, a.String())
	}

	if len(ifInfo.Gateways) > 0 {
		// add default gateway route
		routes = append(routes,
			ovncnitypes.Route{
				Gateway: ifInfo.Gateways[0].String(),
			})
	}

	for _, r := range ifInfo.Routes {
		routes = append(routes,
			ovncnitypes.Route{
				Dest:    r.Dest.String(),
				Gateway: r.NextHop.String(),
			})
	}

	var dan ovncnitypes.DanConfig
	dan.Netns = &pr.Netns

	danDev := ovncnitypes.DanDevice{
		Name:     pr.IfName,
		GuestMac: ifInfo.MAC.String(),
		Device: ovncnitypes.Device{
			Type:        ovncnitypes.VfioDanDeviceType,
			PciDeviceID: pciDeviceID,
		},
		NetworkInfo: ovncnitypes.NetworkInfo{
			Interface: ovncnitypes.Interface{
				IPAddresses: ipAddrs,
				MTU:         uint64(ifInfo.MTU),
			},
			Routes: routes,
		},
	}

	dan.Devices = append(dan.Devices, danDev)
	danConfigPath := getDanConfigPath(config.Default.KataDanConfDir, pr.SandboxID)

	return writeDanConfig(danConfigPath, &dan)
}

func (*defaultPodRequestInterfaceOps) ConfigureInterface(pr *PodRequest, getter PodInfoGetter, ifInfo *PodInterfaceInfo) ([]*current.Interface, error) {
	klog.V(5).Infof("Set up interface %+v with CNI Conf %v for pod %s/%s on network %s NAD %s",
		*pr, pr.CNIConf, pr.PodNamespace, pr.PodName, pr.netName, pr.nadName)
	if pr.CNIConf.DeviceID == "" && ifInfo.IsDPUHostMode {
		return nil, fmt.Errorf("unexpected configuration, pod request on DPU host Device ID must be provided")
	}
	netns, err := ns.GetNS(pr.Netns)
	if err != nil {
		return nil, fmt.Errorf("failed to open netns %q: %v", pr.Netns, err)
	}
	defer netns.Close()

	var hostIface, contIface *current.Interface

	if pr.CNIConf.DeviceID != "" {
		// SR-IOV Case
		hostIface, contIface, err = setupSriovInterface(netns, pr.SandboxID, pr.IfName, ifInfo, pr.CNIConf.DeviceID, pr.IsVFIO)
	} else {
		// General case
		hostIface, contIface, err = setupInterface(netns, pr.SandboxID, pr.IfName, ifInfo)
	}
	if err != nil {
		return nil, err
	}

	if !ifInfo.IsDPUHostMode {
		err = ConfigureOVS(pr.ctx, pr.PodNamespace, pr.PodName, pr.IfName, hostIface.Name, ifInfo, pr.SandboxID, pr.CNIConf.DeviceID, pr.IsVFIO, getter)
		if err != nil {
			pr.deletePort(hostIface.Name, pr.PodNamespace, pr.PodName)
			return nil, err
		}
	}

	// Only configure IPv6 specific stuff and wait for addresses to become usable
	// if there are any IPv6 addresses to assign. v4 doesn't have the concept
	// of tentative addresses so it doesn't need any of this.
	haveV6 := false
	for _, ip := range ifInfo.IPs {
		if ip.IP.To4() == nil {
			haveV6 = true
			break
		}
	}
	if haveV6 && !pr.IsVFIO {
		err = netns.Do(func(_ ns.NetNS) error {
			// deny IPv6 neighbor solicitations
			dadSysctlIface := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/dad_transmits", contIface.Name)
			if _, err := os.Stat(dadSysctlIface); !os.IsNotExist(err) {
				err = setSysctl(dadSysctlIface, 0)
				if err != nil {
					klog.Warningf("Failed to disable IPv6 DAD: %q", err)
				}
			}
			// generate address based on EUI64
			genSysctlIface := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/addr_gen_mode", contIface.Name)
			if _, err := os.Stat(genSysctlIface); !os.IsNotExist(err) {
				err = setSysctl(genSysctlIface, 0)
				if err != nil {
					klog.Warningf("Failed to set IPv6 address generation mode to EUI64: %q", err)
				}
			}

			return ip.SettleAddresses(contIface.Name, 10*time.Second)
		})
		if err != nil {
			klog.Warningf("Failed to settle addresses: %q", err)
		}
	}

	return []*current.Interface{hostIface, contIface}, nil
}

func (*defaultPodRequestInterfaceOps) UnconfigureInterface(pr *PodRequest, ifInfo *PodInterfaceInfo) error {
	podDesc := fmt.Sprintf("for pod %s/%s NAD %s", pr.PodNamespace, pr.PodName, pr.nadName)
	klog.V(5).Infof("Tear down interface (%+v) with CNI conf %v %s", *pr, pr.CNIConf, podDesc)

	if ifInfo.IsDPUHostMode {
		if pr.CNIConf.DeviceID == "" {
			klog.Warningf("Unexpected configuration %s, pod request on DPU host. device ID must be provided", podDesc)
			return nil
		}
		// nothing else to do in DPUHostMode for VFIO device
		if pr.IsVFIO {
			return nil
		}
		// in the case of VF, we need to rename the container interface to VF name and move it to host
	}

	ifnameSuffix := ""
	isSecondary := pr.netName != types.DefaultNetworkName
	// nothing needs to be done for the VFIO case in the container namespace
	if !pr.IsVFIO {
		netns, err := ns.GetNS(pr.Netns)
		if err != nil {
			// According to CNI Specification, DEL command should be completed without error
			// even if netns is missing.
			klog.V(5).Infof("Netns doesn't exist for %s: %v", podDesc, err)
			return nil
		}
		defer netns.Close()

		hostNS, err := ns.GetCurrentNS()
		if err != nil {
			return fmt.Errorf("failed to get host namespace %s: %v", podDesc, err)
		}
		defer hostNS.Close()

		// 1. For SRIOV case, we'd need to move device from container namespace back to the host namespace
		// 2. If it is secondary network and not dpu-host mode, then get the container interface index
		//    so that we know the host-side interface name.
		err = netns.Do(func(_ ns.NetNS) error {
			// container side interface deletion
			link, err := util.GetNetLinkOps().LinkByName(pr.IfName)
			if err != nil {
				return fmt.Errorf("failed to get container interface %s %s: %v", pr.IfName, podDesc, err)
			}
			if pr.CNIConf.DeviceID != "" {
				// SR-IOV Case
				// for VMI's virt-lanuncher pod, Ifname is renamed to Ifname+"-nic"
				if strings.HasPrefix(pr.PodName, "virt-launcher") {
					ifName := pr.IfName + "-nic"
					link, err = util.GetNetLinkOps().LinkByName(ifName)
					if err != nil {
						return fmt.Errorf("failed to get virt-launcher container interface %s %s: %v", ifName, podDesc, err)
					}
				}
				err = util.GetNetLinkOps().LinkSetDown(link)
				if err != nil {
					return fmt.Errorf("failed to bring down container interface %s %s: %v", pr.IfName, podDesc, err)
				}
				err = util.GetNetLinkOps().LinkSetMTU(link, config.DefaultVFMTU)
				if err != nil {
					return fmt.Errorf("failed to reset MTU on tne container interface %s %s: %v", pr.IfName, podDesc, err)
				}
				// rename device to make sure it is unique in the host namespace:
				// if original name of device is empty, sandbox id and a '0' letter prefix is used to make up the unique name.
				oldName := ifInfo.NetdevName
				if oldName == "" {
					id := fmt.Sprintf("_%d", link.Attrs().Index)
					oldName = pr.SandboxID[:(15-len(id))] + id
				}
				err = util.GetNetLinkOps().LinkSetName(link, oldName)
				if err != nil {
					return fmt.Errorf("failed to rename container interface %s to %s %s: %v",
						pr.IfName, oldName, podDesc, err)
				}
				// move device to host netns
				err = util.GetNetLinkOps().LinkSetNsFd(link, int(hostNS.Fd()))
				if err != nil {
					return fmt.Errorf("failed to move container interface %s back to host namespace %s: %v", pr.IfName, podDesc, err)
				}
			}
			if isSecondary {
				ifnameSuffix = fmt.Sprintf("_%d", link.Attrs().Index)
			}
			return nil
		})
		if err != nil {
			klog.Errorf("Error in UnconfigureInterface: %v", err)
		}
	}

	if !ifInfo.IsDPUHostMode {
		var err error
		// host side interface deletion
		var hostIfName string
		if !util.IsNetworkSegmentationSupportEnabled() || isSecondary {
			// this is a secondary network (not primary) or segmentation is not enabled
			hostIfName = pr.SandboxID[:(15-len(ifnameSuffix))] + ifnameSuffix
		}
		if pr.CNIConf.DeviceID != "" {
			hostIfName, err = util.GetFunctionRepresentorName(pr.CNIConf.DeviceID)
			if err != nil {
				klog.Errorf("Failed to get the representor name for DeviceID %s for pod %s: %v",
					pr.CNIConf.DeviceID, podDesc, err)
			}
			// if the specified port was created for other Pod/NAD, return error
			err := ValidateOVSInterfaceConfiguration(pr.PodNamespace, pr.PodName, hostIfName, ifInfo)
			if err != nil {
				klog.Error(err.Error())
				return nil
			}
			if pr.IsVFIO {
				if err := util.SetVFHardwreAddress(pr.CNIConf.DeviceID, defaultVFHardwareAddress); err != nil {
					klog.Warningf("Failed to reset VF hardware address: %s", pr.CNIConf.DeviceID)
				}
			}
		}
		portList, err := ovsFind("interface", "name", "external-ids:sandbox="+pr.SandboxID)
		if err != nil {
			return fmt.Errorf("failed to list interfaces in OVS during delete for sandbox: %s, err: %w",
				pr.SandboxID, err)
		}
		// hostIfName is not empty if using device ID, a secondary network, or segmentation not enabled
		// delete the port in traditional fashion
		if hostIfName != "" {
			pr.deletePort(hostIfName, pr.PodNamespace, pr.PodName)
		} else {
			// this is a primary interface deletion and segmentation is enabled, delete all ports
			// delete happens in reverse order for attached networks, so this is the final deletion
			// In other words we dont have to worry about accidentally deleting a secondary network interface at
			// this point.
			if len(portList) > 1 {
				klog.V(5).Infof("Removing multiple interfaces for primary network segmentation (%+v) %s: %s",
					*pr, podDesc, strings.Join(portList, ","))
			}
			pr.deletePorts(portList, pr.PodNamespace, pr.PodName)
		}
		err = clearPodBandwidthForPorts(portList, pr.SandboxID)
		if err != nil {
			klog.Errorf("Failed to clearPodBandwidth sandbox %v %s: %v", pr.SandboxID, podDesc, err)
		}
		pr.deletePodConntrack()
	}
	return nil
}

func (pr *PodRequest) deletePodConntrack() {
	if pr.CNIConf.PrevResult == nil {
		return
	}
	result, err := current.NewResultFromResult(pr.CNIConf.PrevResult)
	if err != nil {
		klog.Warningf("Could not convert result to current version: %v", err)
		return
	}

	for _, ip := range result.IPs {
		// Skip known non-sandbox interfaces
		if ip.Interface != nil {
			intIdx := *ip.Interface
			if intIdx >= 0 &&
				intIdx < len(result.Interfaces) && result.Interfaces[intIdx].Sandbox == "" {
				continue
			}
		}
		_, err = util.DeleteConntrack(ip.Address.IP.String(), 0, "", netlink.ConntrackReplyAnyIP, nil)
		if err != nil {
			klog.Errorf("Failed to delete Conntrack Entry for %s: %v", ip.Address.IP.String(), err)
			continue
		}
	}
}

func (pr *PodRequest) deletePort(ifaceName, podNamespace, podName string) {
	podDesc := fmt.Sprintf("%s/%s", podNamespace, podName)

	var isVFDevice bool
	link, err := util.GetNetLinkOps().LinkByName(ifaceName)
	if err != nil {
		klog.Warningf("Failed to find host-side link %s for pod %q: %v", ifaceName, podDesc, err)
	} else if pr.CNIConf.DeviceID != "" {
		isVFDevice = true
	}

	if isVFDevice {
		// SR-IOV case: bring down the representor before removing from OVS.
		// The device plugin can re-allocate a VF to a new pod before this
		// CmdDel completes, causing the new pod's CmdAdd shim to run
		// concurrently. By doing LinkSetDown before del-port, we eliminate
		// the window where a racing CmdAdd could have its LinkSetUp or
		// ConfigureOVS undone. ValidateOVSInterfaceConfiguration (called
		// before deletePort) and on the CmdAdd side provide additional
		// protection against operating on a port reclaimed by a new pod.
		if err = util.GetNetLinkOps().LinkSetMTU(link, config.DefaultVFMTU); err != nil {
			klog.Warningf("Failed to reset MTU of pod %q interface %s: %v", podDesc, ifaceName, err)
		}
		if err = util.GetNetLinkOps().LinkSetDown(link); err != nil {
			klog.Warningf("Failed to bring down pod %q interface %s: %v", podDesc, ifaceName, err)
		}
	}

	out, err := ovsExec("del-port", "br-int", ifaceName)
	if err != nil && !strings.Contains(err.Error(), "no port named") {
		// DEL should be idempotent; don't return an error just log it
		klog.Warningf("Failed to delete pod %q OVS port %s: %v\n  %q", podDesc, ifaceName, err, string(out))
	}

	if link != nil && !isVFDevice {
		// Non-SR-IOV case: delete the host-side veth interface after
		// removing the OVS port.
		if err = util.GetNetLinkOps().LinkDelete(link); err != nil {
			klog.Warningf("Failed to delete pod %q interface %s: %v", podDesc, ifaceName, err)
		}
	}
}

func (pr *PodRequest) deletePorts(ifaces []string, podNamespace, podName string) {
	for _, iface := range ifaces {
		pr.deletePort(iface, podNamespace, podName)
	}
}

// setupIngressFilter sets up an ingress filter using nftables to block
// unwanted ICMPv6 Router Advertisement (RA) packets on a specific device.
// It creates a new nftables table, chain, and rule to drop RA packets
// that do not match the specified gateway link-local address (gwLLA) and
// have a Router Advertisement (RA) lifetime not equal to 0.
//
// The nftables rule created by this function looks like:
// `icmpv6 type nd-router-advert ip6 saddr != <gwLLA> @th,48,16 != 0 drop`
//
// Parameters:
//   - ifaceName: The name of the network interface where the ingress filter
//     should be applied.
//   - gwLLA: The gateway link-local address to match against in the RA packets.
//
// Returns:
// - error: An error if the nftables setup fails, otherwise nil.
func setupIngressFilter(ifaceName, gwLLA string) error {
	const (
		tableName   = "ingress_filter" // Name of the nftables table to be created
		rasLifetime = "@th,48,16"      // Offset for RA lifetime field in ICMPv6 packets
	)

	// Initialize a new nftables table for the ingress filter
	nft, err := knftables.New(knftables.NetDevFamily, tableName)
	if err != nil {
		return fmt.Errorf("failed to initialize table: %w", err)
	}

	// Delegate the setup of the filter with the specified table and lifetime matcher
	return setupIngressFilterWithTableAndLifetimeMatcher(nft, ifaceName, gwLLA, rasLifetime)
}

func setupIngressFilterWithTableAndLifetimeMatcher(nft knftables.Interface, ifaceName, gwLLA, rasLifetime string) error {
	const (
		chainName = "input"
	)

	tx := nft.NewTransaction()

	tx.Add(&knftables.Table{})

	tx.Add(&knftables.Chain{
		Name:     chainName,
		Type:     knftables.PtrTo(knftables.FilterType),
		Hook:     knftables.PtrTo(knftables.IngressHook),
		Priority: knftables.PtrTo(knftables.FilterPriority),
		Device:   knftables.PtrTo(ifaceName),
	})

	tx.Add(&knftables.Rule{
		Chain: chainName,
		Rule: knftables.Concat(
			"icmpv6", "type", "nd-router-advert", "ip6", "saddr", "!=", gwLLA, rasLifetime, "!=", "0", "drop",
		),
	})

	// Execute the transaction
	if err := nft.Run(context.Background(), tx); err != nil {
		return fmt.Errorf("could not update netdev nftables: %v", err)
	}

	return nil

}
