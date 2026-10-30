// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package ipreserv

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/allocator/ip/subnet"
	ipreservation "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1"
	ipresvlisters "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/crd/ipreservation/v1beta1/apis/listers/ipreservation/v1beta1"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/networkmanager"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/syncmap"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	ovntypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

func TestRestoreAndReleaseReservation(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		name := "restore"
		if conflict {
			name = "other owner conflict"
		}
		t.Run(name, func(t *testing.T) {
			netInfo := util.NewReconcilableNetInfo(&util.DefaultNetInfo{})
			network := netInfo.GetNetworkName()
			a := subnet.NewAllocator()
			if err := a.AddOrUpdateSubnet(subnet.SubnetConfig{Name: network, Subnets: ovntest.MustParseIPNets("10.0.0.0/29")}); err != nil {
				t.Fatal(err)
			}
			reservation := &ipreservation.IPReservation{
				ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "reservation", UID: "old-uid"},
				Spec:       ipreservation.IPReservationSpec{NetworkAttachmentName: "ns/nad", IPFamily: ipreservation.IPv4Protocol, Count: 2},
				Status:     ipreservation.IPReservationStatus{Status: ovntypes.OvnK8sStatusSucceeded, ReservedIPs: []string{"10.0.0.1/29", "10.0.0.2/29"}},
			}
			ips := ovntest.MustParseIPNets(reservation.Status.ReservedIPs...)
			if conflict {
				if err := a.AllocateIPs(network, "pod", ips[:1]); err != nil {
					t.Fatal(err)
				}
			}
			indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
			if err := indexer.Add(reservation); err != nil {
				t.Fatal(err)
			}
			c := &Controller{
				ReconcilableNetInfo: netInfo, allocator: a,
				ipresvLister:   ipresvlisters.NewIPReservationLister(indexer),
				ipresvCache:    syncmap.NewSyncMap[*ipReservationState](),
				networkManager: &networkmanager.FakeNetworkManager{NADNetworks: map[string]util.NetInfo{"ns/nad": netInfo}},
				recorder:       record.NewFakeRecorder(10),
			}
			err := c.syncIPReservations()
			if conflict {
				if err == nil {
					t.Fatal("accepted another owner's reservation")
				}
				if !a.OwnsIPs(network, "pod", ips[:1]) {
					t.Fatal("changed conflicting owner's allocation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := c.syncIPReservations(); err != nil {
				t.Fatalf("same-owner restoration failed: %v", err)
			}
			owner := reservationOwner(reservation)
			if !a.OwnsIPs(network, owner, ips) {
				t.Fatal("restored allocations have wrong owner")
			}
			state, ok := c.ipresvCache.Load("ns/reservation")
			if !ok {
				t.Fatal("missing cached reservation")
			}
			if err := c.deleteIPReservation(state); err != nil {
				t.Fatal(err)
			}
			reservation.UID = "new-uid"
			newOwner := reservationOwner(reservation)
			for _, ip := range ips {
				if err := a.AllocateIPs(network, newOwner, ovntest.MustParseIPNets(ip.String())); err != nil {
					t.Fatalf("reservation was not released: %v", err)
				}
			}
			if err := c.deleteIPReservation(state); err != nil {
				t.Fatal(err)
			}
			if !a.OwnsIPs(network, newOwner, ips) {
				t.Fatal("stale deletion released recreated reservation")
			}
		})
	}
}
