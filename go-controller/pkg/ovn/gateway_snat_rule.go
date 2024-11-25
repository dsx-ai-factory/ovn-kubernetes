package ovn

import (
	"fmt"
	"net"

	libovsdbclient "github.com/ovn-org/libovsdb/client"
	libovsdbops "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/libovsdb/ops"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/nbdb"
	addressset "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/ovn/address_set"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"
	utilnet "k8s.io/utils/net"
)

func (gw *GatewayManager) applyGWSnatRule(snatRule *util.GWSNATRule, logicalRouter *nbdb.LogicalRouter, hostSubnets []*net.IPNet, snatMap map[string]bool) error {
	as, err := gw.addressSetFactory.EnsureAddressSet(getSnatAllowedExtAddrSetDbIDs(logicalRouter.Name, gw.controllerName, &snatRule.ExternalIP))
	if err != nil {
		return fmt.Errorf("failed to ensure address set for snat IP %s on router %s: %v", snatRule.ExternalIP.String(), logicalRouter.Name, err)
	}
	if err := as.AddAddresses(util.StringSlice(snatRule.Destinations)); err != nil {
		return fmt.Errorf("failed to add subnets to address set %s: %v", as.GetName(), err)
	}
	// clean stale address from address set
	if err := cleanStaleAddressSetIPs(as, snatRule.Destinations); err != nil {
		return fmt.Errorf("failed to clean stale addresses from %s: %v", as.GetName(), err)
	}

	// TODO: IPv6 support for gateway snat rule
	v4AllowedExtDests, _ := as.GetUuids()
	snats := make([]*nbdb.NAT, 0, len(hostSubnets))
	extIDs := map[string]string{string(libovsdbops.OwnerTypeKey): string(libovsdbops.GatewaySnatRuleType)}
	for _, hostSubnet := range hostSubnets {
		if utilnet.IsIPv6(hostSubnet.IP) {
			klog.Warningf("IPv6 is not supported in gateway snat rule")
			continue
		}
		snat := libovsdbops.BuildSNAT(&snatRule.ExternalIP, hostSubnet, "", extIDs)
		snat.AllowedExtIPs = &v4AllowedExtDests
		snats = append(snats, snat)
		snatMap[simpleNATKey(snat)] = true
	}
	err = libovsdbops.CreateOrUpdateNATs(gw.nbClient, logicalRouter, snats...)
	if err != nil {
		return fmt.Errorf("failed to create SNAT rule for rule %v on router %s: %v", snatRule, logicalRouter.Name, err)
	}
	return nil
}

func (gw *GatewayManager) cleanStaleGwSnatRule(logicalRouter *nbdb.LogicalRouter, desiredNats map[string]bool) error {
	// find stale custom SNATs
	routerNats, err := libovsdbops.GetRouterNATs(gw.nbClient, logicalRouter)
	if err != nil {
		if errors.Is(err, libovsdbclient.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("failed to look up existing nats of router %s: %v", logicalRouter.Name, err)
	}
	staleNats := []*nbdb.NAT{}
	staleExtIPs := []string{}
	for _, nat := range routerNats {
		if nat.ExternalIDs[string(libovsdbops.OwnerTypeKey)] != string(libovsdbops.GatewaySnatRuleType) {
			continue
		}
		if _, found := desiredNats[simpleNATKey(nat)]; !found {
			staleNats = append(staleNats, nat)
			staleExtIPs = append(staleExtIPs, nat.ExternalIP)
		}
	}
	if len(staleNats) > 0 {
		// remove stale SNATs
		if err := libovsdbops.DeleteNATs(gw.nbClient, logicalRouter, staleNats...); err != nil {
			klog.Warningf("Failed to delete stale custom nats: %v", err)
		}
		for _, ip := range staleExtIPs {
			staleExtIP := net.ParseIP(ip)
			if err := gw.addressSetFactory.DestroyAddressSet(getSnatAllowedExtAddrSetDbIDs(logicalRouter.Name, gw.controllerName, &staleExtIP)); err != nil {
				klog.Warningf("Failed to delete alternative snat address set for %s: %v", logicalRouter.Name, err)
			}
		}
	}
	return nil
}

func cleanStaleAddressSetIPs(addrset addressset.AddressSet, desiredAddresses []*net.IPNet) error {
	staleSubnets := []*net.IPNet{}

	desiredAddressesMap := map[string]bool{}
	currentV4Addresses, _ := addrset.GetAddresses()
	for _, ipnet := range desiredAddresses {
		desiredAddressesMap[ipnet.String()] = true
	}
	for _, currAddress := range currentV4Addresses {
		if _, keep := desiredAddressesMap[currAddress]; keep {
			// address is needed, not deleting
			continue
		}
		if ip, ipnet, err := net.ParseCIDR(currAddress); err == nil {
			staleSubnets = append(staleSubnets,
				&net.IPNet{
					IP:   ip,
					Mask: ipnet.Mask,
				})
		} else {
			return fmt.Errorf("invalid address %s", currAddress)
		}
	}
	if len(staleSubnets) > 0 {
		if err := addrset.DeleteAddresses(util.StringSlice(staleSubnets)); err != nil {
			return fmt.Errorf("failed to delete stale subnets: %v", err)
		}
	}
	return nil
}

func simpleNATKey(nat *nbdb.NAT) string {
	key := fmt.Sprintf("%s-%s", nat.ExternalIP, nat.LogicalIP)
	if nat.AllowedExtIPs != nil {
		key += "-" + *nat.AllowedExtIPs
	}
	return key
}

func getSnatAllowedExtAddrSetDbIDs(routerName, controller string, externalIP *net.IP) *libovsdbops.DbObjectIDs {
	return libovsdbops.NewDbObjectIDs(libovsdbops.AddressSetSnatAllowedExtIPs, controller, map[libovsdbops.ExternalIDKey]string{
		libovsdbops.ObjectNameKey: routerName + "-" + externalIP.String(),
	})
}
