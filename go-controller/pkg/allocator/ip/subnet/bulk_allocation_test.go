// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package subnet

import (
	"errors"
	"testing"

	ipam "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/allocator/ip"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
)

func TestBulkAllocationOwnership(t *testing.T) {
	for _, cidr := range []string{"10.0.0.0/29", "fd00::/125"} {
		t.Run(cidr, func(t *testing.T) {
			a := NewAllocator()
			subnet := ovntest.MustParseIPNets(cidr)
			if err := a.AddOrUpdateSubnet(SubnetConfig{Name: "network", Subnets: subnet}); err != nil {
				t.Fatal(err)
			}
			isIPv4 := subnet[0].IP.To4() != nil
			ips, err := a.AllocateIPsByCount("network", "reservation/old", isIPv4, 3)
			if err != nil {
				t.Fatal(err)
			}
			if !a.OwnsIPs("network", "reservation/old", ips) {
				t.Fatal("bulk allocation has no owner")
			}
			if err := a.ReleaseIPs("network", "reservation/new", ips); err != nil {
				t.Fatal(err)
			}
			if !a.OwnsIPs("network", "reservation/old", ips) {
				t.Fatal("another owner released reservation")
			}
			if err := a.ReleaseIPs("network", "reservation/old", ips); err != nil {
				t.Fatal(err)
			}
			for _, ip := range ips {
				if err := a.AllocateIPs("network", "pod", ovntest.MustParseIPNets(ip.String())); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBulkAllocationRollback(t *testing.T) {
	a := NewAllocator()
	if err := a.AddOrUpdateSubnet(SubnetConfig{Name: "network", Subnets: ovntest.MustParseIPNets("10.0.0.0/30", "10.0.1.0/30")}); err != nil {
		t.Fatal(err)
	}
	if ips, err := a.AllocateIPsByCount("network", "failed", true, 5); !errors.Is(err, ipam.ErrFull) || ips != nil {
		t.Fatalf("expected atomic failure, got %v, %v", ips, err)
	}
	ips, err := a.AllocateIPsByCount("network", "reservation", true, 4)
	if err != nil {
		t.Fatalf("partial allocation leaked addresses: %v", err)
	}
	if !a.OwnsIPs("network", "reservation", ips) {
		t.Fatal("successful allocation has wrong ownership")
	}
	if err := a.ReleaseIPs("network", "failed", ips); err != nil {
		t.Fatal(err)
	}
	if !a.OwnsIPs("network", "reservation", ips) {
		t.Fatal("failed allocation left stale ownership")
	}
}

func TestAvailableIPsCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		subnets []string
		want    int64
	}{
		{name: "single subnet", subnets: []string{"10.0.0.0/30"}, want: 2},
		{name: "multiple subnets", subnets: []string{"10.0.0.0/30", "10.0.1.0/30"}, want: 4},
		{name: "dual stack", subnets: []string{"10.0.0.0/30", "fd00::/125", "10.0.1.0/30"}, want: 4},
		{name: "IPv6 only", subnets: []string{"fd00::/125"}, want: 0},
		{name: "no usable addresses", subnets: []string{"10.0.0.0/31", "10.0.1.0/32"}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := NewAllocator()
			if err := a.AddOrUpdateSubnet(SubnetConfig{Name: "network", Subnets: ovntest.MustParseIPNets(tc.subnets...)}); err != nil {
				t.Fatal(err)
			}
			checkCount := func(want int64) {
				t.Helper()
				got, err := a.AvailableIPsCount("network", true)
				if err != nil || got != want {
					t.Fatalf("available IPv4 addresses = %d, %v; want %d, nil", got, err, want)
				}
			}
			checkCount(tc.want)
			for remaining := tc.want; remaining > 0; remaining-- {
				if _, err := a.AllocateIPsByCount("network", "reservation", true, 1); err != nil {
					t.Fatal(err)
				}
				checkCount(remaining - 1)
			}
			if _, err := a.AllocateIPsByCount("network", "reservation", true, 1); !errors.Is(err, ipam.ErrFull) {
				t.Fatalf("expected exhausted IPv4 pool, got %v", err)
			}
			checkCount(0)
		})
	}
}
