// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package hostport

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

// deleteUDPConntrack accepts a single destination or a prefix, so wildcard
// hostPorts can cover all of 127/8 without enumerating millions of addresses.
func deleteUDPConntrack(ctx context.Context, destination string, port int32) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var network *net.IPNet
	if ip := net.ParseIP(destination); ip != nil {
		network = netlink.NewIPNet(ip)
	} else {
		var err error
		_, network, err = net.ParseCIDR(destination)
		if err != nil {
			return fmt.Errorf("parse conntrack destination %q: %w", destination, err)
		}
	}
	filter := &netlink.ConntrackFilter{}
	if err := filter.AddProtocol(unix.IPPROTO_UDP); err != nil {
		return err
	}
	if err := filter.AddPort(netlink.ConntrackOrigDstPort, uint16(port)); err != nil {
		return err
	}
	if err := filter.AddIPNet(netlink.ConntrackOrigDstIP, network); err != nil {
		return err
	}
	family := netlink.FAMILY_V6
	if network.IP.To4() != nil {
		family = netlink.FAMILY_V4
	}
	_, err := util.GetNetLinkOps().ConntrackDeleteFilters(netlink.ConntrackTable, netlink.InetFamily(family),
		&cancellableConntrackFilter{ctx: ctx, filter: filter})
	// Keep this tuple pending if cancellation interrupted its cleanup.
	return errors.Join(err, ctx.Err())
}

type cancellableConntrackFilter struct {
	ctx    context.Context
	filter *netlink.ConntrackFilter
}

func (f *cancellableConntrackFilter) MatchConntrackFlow(flow *netlink.ConntrackFlow) bool {
	// Netlink cannot cancel an in-flight request, but checking each flow stops
	// issuing further deletes as soon as the current request returns.
	return f.ctx.Err() == nil && f.filter.MatchConntrackFlow(flow)
}
