// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package node

// Temporary workaround for DPU-host deployments where a Layer3 primary CUDN
// uses an Uplink backed by an SR-IOV VF.
//
// The host's network configuration daemon (netplan) owns the addresses and
// routes of the Uplink VF, while OVN-Kubernetes owns the VRF the VF is
// enslaved to. Neither side coordinates with the other, so:
//
//   - Deleting the VRF detaches the VF while the daemon still believes it is
//     configured, leaving addresses and routes behind that never get
//     reprogrammed into the main table.
//   - Recreating the VRF for the same, or another, CUDN using that VF then
//     starts from a half-configured interface.
//
// Cycling the VF's administrative link state is what makes the daemon withdraw
// and reapply its configuration, so VRF deletion hands the VF back down, and
// VRF creation makes sure it comes up from a withdrawn state.
//
// Setting OVNKUBE_UDN_UPLINK_VF_WORKAROUND to false turns all of this off, for
// a deployment that does not need it or that the link-state cycling harms.
//
// TODO: remove once the host and OVN-Kubernetes no longer share ownership of
// the Uplink VF configuration.

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	uplinkutil "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/uplink"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

const (
	// uplinkVFWorkaroundEnvVar names the environment variable that turns the
	// workaround off when it is set to a false value.
	uplinkVFWorkaroundEnvVar = "OVNKUBE_UDN_UPLINK_VF_WORKAROUND"
	// uplinkVFWithdrawalTimeout bounds how long the VF down/up cycle waits for
	// the host to withdraw the configuration of the downed VF. The cycle
	// continues after the timeout: leaving the VF down would break the network
	// the cycle is preparing.
	uplinkVFWithdrawalTimeout = 30 * time.Second
	// uplinkVFWithdrawalPollInterval is how often the VF is re-read while
	// waiting for the host to withdraw its configuration.
	uplinkVFWithdrawalPollInterval = 500 * time.Millisecond
)

// uplinkVFWorkaroundEnabled reports whether the Uplink VF link-state
// workaround is applied. It is on unless the environment variable is set to an
// explicit false value: a deployment that never needed the workaround keeps
// the untouched behavior of a plain false, while a value that does not parse
// is reported and ignored rather than silently turning a workaround this
// intrusive off.
//
// Callers check it only once they know the workaround would otherwise apply,
// so that its decision is logged for the Uplink networks it concerns and
// nothing else.
func uplinkVFWorkaroundEnabled() bool {
	value, found := os.LookupEnv(uplinkVFWorkaroundEnvVar)
	if !found || value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		klog.Warningf("Ignoring %s=%q: not a boolean value, applying the Uplink VF workaround",
			uplinkVFWorkaroundEnvVar, value)
		return true
	}
	if !enabled {
		klog.Infof("Skipping the Uplink VF workaround: %s=%q", uplinkVFWorkaroundEnvVar, value)
	}
	return enabled
}

// ensureUplinkVFUp brings the Uplink VF up from a withdrawn state before the
// network's VRF is created, so that the VRF is built on host configuration the
// host has just programmed.
//
// A VF the deletion of a previous CUDN VRF left down only needs to be brought
// up. A VF that is up was left configured because its release could not take
// it down, and the host reprograms only what it has withdrawn, so it is taken
// down and waited on first.
//
// A VF that is already enslaved is left untouched: it belongs to a VRF that
// this or another network is using, such as after an ovnkube-node restart, and
// its configuration has already been through the cycle.
//
// It runs before Uplink gateway resolution as well, because resolution
// requires the discovered host addresses that the VF only regains once it is
// up.
func (udng *UserDefinedNetworkGateway) ensureUplinkVFUp() error {
	if !config.IsModeDPUHost() || udng.Uplink() == "" || !uplinkVFWorkaroundEnabled() {
		return nil
	}
	vfName, err := udng.uplinkHostInterfaceName()
	if err != nil {
		return err
	}
	if vfName == "" {
		return nil
	}
	link, err := util.GetNetLinkOps().LinkByName(vfName)
	if err != nil {
		if util.GetNetLinkOps().IsLinkNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("failed to look up Uplink %s interface %s for network %s: %w",
			udng.Uplink(), vfName, udng.GetNetworkName(), err)
	}
	if link.Attrs().MasterIndex != 0 {
		return nil
	}
	if link.Attrs().Flags&net.FlagUp != 0 {
		if err := udng.withdrawUplinkVFConfiguration(link); err != nil {
			return err
		}
	}
	klog.Infof("Setting Uplink %s interface %s up before creating the VRF of network %s",
		udng.Uplink(), vfName, udng.GetNetworkName())
	if err := util.GetNetLinkOps().LinkSetUp(link); err != nil {
		return fmt.Errorf("failed to set Uplink %s interface %s up for network %s: %w",
			udng.Uplink(), vfName, udng.GetNetworkName(), err)
	}
	return nil
}

// withdrawUplinkVFConfiguration takes the Uplink VF down and waits for the
// host to withdraw the addresses and routes it owns from it.
func (udng *UserDefinedNetworkGateway) withdrawUplinkVFConfiguration(vfLink netlink.Link) error {
	vfName := vfLink.Attrs().Name
	klog.Infof("Setting Uplink %s interface %s down for network %s, so that the host withdraws "+
		"its addresses and routes", udng.Uplink(), vfName, udng.GetNetworkName())
	if err := util.GetNetLinkOps().LinkSetDown(vfLink); err != nil {
		return fmt.Errorf("failed to set Uplink %s interface %s down for network %s: %w",
			udng.Uplink(), vfName, udng.GetNetworkName(), err)
	}

	var addresses []netlink.Addr
	var routes []netlink.Route
	err := wait.PollUntilContextTimeout(context.Background(), uplinkVFWithdrawalPollInterval,
		uplinkVFWithdrawalTimeout, true, func(context.Context) (bool, error) {
			var err error
			addresses, routes, err = uplinkVFHostConfiguration(vfLink)
			if err != nil {
				klog.Warningf("Failed to read the configuration of Uplink %s interface %s of network %s: %v",
					udng.Uplink(), vfName, udng.GetNetworkName(), err)
				return false, nil
			}
			return len(addresses) == 0 && len(routes) == 0, nil
		})
	if err != nil {
		// Report the leftovers and continue: the VF still has to be brought
		// back up for the network being created.
		klog.Warningf("Timed out after %v waiting for the host to withdraw the configuration of Uplink %s "+
			"interface %s of network %s; addresses: %v, routes: %v",
			uplinkVFWithdrawalTimeout, udng.Uplink(), vfName, udng.GetNetworkName(), addresses, routes)
		return nil
	}
	klog.Infof("Host withdrew the configuration of Uplink %s interface %s of network %s",
		udng.Uplink(), vfName, udng.GetNetworkName())
	return nil
}

// setUplinkVFDown hands the Uplink VF back to the host by taking it down, once
// the VRF that held it has been deleted. The host withdraws the configuration
// it owns asynchronously; the next network to claim the VF waits for that in
// ensureUplinkVFUp.
//
// A VF that is still enslaved after the deletion belongs to another VRF, so it
// is left alone: it is carrying the traffic of the network that claimed it.
func (udng *UserDefinedNetworkGateway) setUplinkVFDown() error {
	if !config.IsModeDPUHost() || udng.Uplink() == "" || !uplinkVFWorkaroundEnabled() {
		return nil
	}
	vfName, err := udng.uplinkHostInterfaceName()
	if err != nil {
		return err
	}
	if vfName == "" {
		return nil
	}
	link, err := util.GetNetLinkOps().LinkByName(vfName)
	if err != nil {
		if util.GetNetLinkOps().IsLinkNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("failed to look up Uplink %s interface %s for network %s: %w",
			udng.Uplink(), vfName, udng.GetNetworkName(), err)
	}
	if link.Attrs().MasterIndex != 0 {
		klog.Infof("Leaving Uplink %s interface %s up after deleting the VRF of network %s: "+
			"it is enslaved to another VRF", udng.Uplink(), vfName, udng.GetNetworkName())
		return nil
	}
	klog.Infof("Setting Uplink %s interface %s down after deleting the VRF of network %s",
		udng.Uplink(), vfName, udng.GetNetworkName())
	if err := util.GetNetLinkOps().LinkSetDown(link); err != nil {
		return fmt.Errorf("failed to set Uplink %s interface %s down for network %s: %w",
			udng.Uplink(), vfName, udng.GetNetworkName(), err)
	}
	return nil
}

// uplinkVFHostConfiguration returns the host-owned addresses of the Uplink VF
// and the main table routes it is the output interface of. Only a VF that is
// not enslaved goes through the withdrawal wait, so the main table is the one
// holding its routes.
//
// Link-local addresses and routes are left out: they are kernel-owned, and a
// VF that carried a VRF has keep_addr_on_down set from its enslavement, so its
// IPv6 link-local address and subnet route survive the interface going down.
func uplinkVFHostConfiguration(vfLink netlink.Link) ([]netlink.Addr, []netlink.Route, error) {
	addresses, err := util.GetFilteredInterfaceAddrs(vfLink, true, true)
	if err != nil {
		return nil, nil, err
	}
	// Filtered by table only and narrowed down to the VF in code, like the VRF
	// manager does when it migrates routes: RT_FILTER_OIF compares the
	// top-level link index, which is 0 for the multipath routes a multi-gateway
	// uplink gets.
	tableFilter := &netlink.Route{Table: unix.RT_TABLE_MAIN}
	tableRoutes, err := util.GetNetLinkOps().RouteListFiltered(netlink.FAMILY_ALL, tableFilter,
		netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list main table routes: %w", err)
	}
	var routes []netlink.Route
	for _, route := range util.FilterRoutesByIfIndex(tableRoutes, vfLink.Attrs().Index) {
		if route.Dst != nil && route.Dst.IP.IsLinkLocalUnicast() {
			continue
		}
		routes = append(routes, route)
	}
	return addresses, routes, nil
}

// uplinkHostInterfaceName returns the Linux device of the network's Uplink on
// this node, or an empty name when it cannot be determined yet.
//
// The published UplinkState host interface name is used when the gateway has
// not resolved the interface itself yet, because it is available even while
// discovery fails: with the VF down and stripped of its addresses, discovery
// cannot complete, which is exactly the state the VF must be brought up from.
func (udng *UserDefinedNetworkGateway) uplinkHostInterfaceName() (string, error) {
	if udng.gwInterfaceName != "" {
		return udng.gwInterfaceName, nil
	}
	state, err := uplinkutil.GetState(udng.uplinkStateLister, udng.Uplink(), udng.node.Name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get UplinkState %s for network %s: %w",
			uplinkutil.StateName(udng.Uplink(), udng.node.Name), udng.GetNetworkName(), err)
	}
	return string(state.Status.HostInterfaceName), nil
}
