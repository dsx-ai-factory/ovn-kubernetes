package logicalswitchmanager

import (
	"net"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/urfave/cli/v2"

	utilnet "k8s.io/utils/net"

	ipallocator "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/allocator/ip"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	ovntest "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/testing"
	ovntypes "github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/types"
)

// test function that returns if an IP address is allocated
func (manager *LogicalSwitchManager) isAllocatedIP(switchName, ip string) bool {
	return manager.AllocateIPs(switchName, []*net.IPNet{ovntest.MustParseIPNet(ip)}) == ipallocator.ErrAllocated
}

// AllocateNextIPv4s will allocate the next IPv4 addresses from each of the host subnets
// for a given switch
func (manager *LogicalSwitchManager) AllocateNextIPv4s(switchName string) ([]*net.IPNet, error) {
	ips, err := manager.AllocateNextIPs(switchName)
	if err != nil {
		return nil, err
	}
	var ipv4s []*net.IPNet
	var ipv6s []*net.IPNet
	for _, ip := range ips {
		if utilnet.IsIPv6(ip.IP) {
			ipv6s = append(ipv6s, ip)
		} else {
			ipv4s = append(ipv4s, ip)
		}
	}
	err = manager.ReleaseIPs(switchName, ipv6s)
	if err != nil {
		return nil, err
	}
	return ipv4s, nil
}

type testNodeSubnetData struct {
	switchName string
	subnets    []string //IP subnets in string format e.g. 10.1.1.0/24
}

var _ = ginkgo.Describe("OVN Logical Switch Manager operations", func() {
	var (
		app       *cli.App
		fexec     *ovntest.FakeExec
		lsManager *LogicalSwitchManager
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		gomega.Expect(config.PrepareTestConfig()).To(gomega.Succeed())

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags
		lsManager = NewLogicalSwitchManager()
	})

	ginkgo.Context("when adding node", func() {
		ginkgo.It("creates IPAM for each subnet and reserves IPs correctly when HybridOverlay is enabled and address is passed", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				testNode := testNodeSubnetData{
					switchName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}
				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				allocatedHybridOverlayDRIP, err := lsManager.AllocateHybridOverlay(testNode.switchName, []string{"10.1.1.53"})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(net.ParseIP("10.1.1.53").To4()).To(gomega.Equal(allocatedHybridOverlayDRIP[0].IP))
				gomega.Expect(true).To(gomega.Equal(lsManager.isAllocatedIP(testNode.switchName, "10.1.1.53/32")))

				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		ginkgo.It("creates IPAM for each subnet and reserves the .3 address for Hybrid Overlay by default", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				testNode := testNodeSubnetData{
					switchName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				allocatedHybridOverlayDRIP, err := lsManager.AllocateHybridOverlay(testNode.switchName, []string{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(net.ParseIP("10.1.1.3").To4()).To(gomega.Equal(allocatedHybridOverlayDRIP[0].IP))

				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(true).To(gomega.Equal(lsManager.isAllocatedIP(testNode.switchName, "10.1.1.3/32")))

				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
		ginkgo.It("creates IPAM for each subnet and reserves a non-default IP address for hybrid overlay", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				testNode := testNodeSubnetData{
					switchName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = lsManager.AllocateIPs(testNode.switchName, []*net.IPNet{
					{IP: net.ParseIP("10.1.1.3").To4(), Mask: net.CIDRMask(32, 32)},
				})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				allocatedHybridOverlayDRIP, err := lsManager.AllocateHybridOverlay(testNode.switchName, []string{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// 10.1.1.4 is the next ip address
				gomega.Expect("10.1.1.4").To(gomega.Equal(allocatedHybridOverlayDRIP[0].IP.String()))

				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(true).To(gomega.Equal(lsManager.isAllocatedIP(testNode.switchName, "10.1.1.3/32")))

				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})

	ginkgo.Context("when reserving IP addresses", func() {
		ginkgo.It("counts correctly available number of IPs for IPv4", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					switchName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				allocatedIPs := []string{
					"10.1.1.2/24",
					"10.1.1.3/24",
					"10.1.1.4/24",
					"10.1.1.5/24",
					"10.1.1.6/24",
					"10.1.1.7/24",
					"10.1.1.8/24",
					"2000::2/64",
				}
				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, allocatedIP := range allocatedIPs {
					allocatedIPNets := ovntest.MustParseIPNets(allocatedIP)
					err = lsManager.AllocateIPs(testNode.switchName, allocatedIPNets)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				gomega.Expect(lsManager.AvailableIPsCount(testNode.switchName, true)).Should(gomega.Equal(int64(247)))

				return nil
			}
			lsManager = NewL2SwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("fails while counting available number of IPs for IPv6", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					switchName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/24",
						"2000::/120",
					},
				}

				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = lsManager.AvailableIPsCount(testNode.switchName, false)
				gomega.Expect(err).To(gomega.HaveOccurred())
				return nil
			}
			lsManager = NewL2SwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("counts correctly when no IPs have been allocated", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					switchName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/24",
					},
				}

				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(lsManager.AvailableIPsCount(testNode.switchName, true)).Should(gomega.Equal(int64(254)))

				return nil
			}
			lsManager = NewL2SwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("correctly reserve requested number of IPs", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					switchName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/24",
					},
				}

				allocatedIPs := []string{
					"10.1.1.1/24",
					"10.1.1.2/24",
					"10.1.1.3/24",
					"10.1.1.4/24",
					"10.1.1.5/24",
					"10.1.1.6/24",
					"10.1.1.7/24",
					"10.1.1.8/24",
				}

				expectedIPs := []string{
					"10.1.1.9/24",
					"10.1.1.10/24",
					"10.1.1.11/24",
					"10.1.1.12/24",
					"10.1.1.13/24",
				}

				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, allocatedIP := range allocatedIPs {
					allocatedIPNets := ovntest.MustParseIPNets(allocatedIP)
					err = lsManager.AllocateIPs(testNode.switchName, allocatedIPNets)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}

				// count of available IP now should be 246; reserve 5 IPs
				ipnets, err := lsManager.AllocateIPsByCount(testNode.switchName, true, 5)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(ipnets).Should(gomega.HaveLen(5))
				for i, ipnet := range ipnets {
					gomega.Expect(ipnet.String()).Should(gomega.Equal(expectedIPs[i]))
				}
				// check that the count now is 241
				gomega.Expect(lsManager.AvailableIPsCount(testNode.switchName, true)).Should(gomega.Equal(int64(241)))
				return nil
			}
			lsManager = NewL2SwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("correctly releases partially allocated IPs on failure", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					switchName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/29",
					},
				}

				allocatedIPs := []string{
					"10.1.1.1/29",
					"10.1.1.2/29",
					"10.1.1.3/29",
				}

				expectedIPs := []string{
					"10.1.1.4/29",
					"10.1.1.5/29",
					"10.1.1.6/29",
				}

				err = lsManager.AddOrUpdateSwitch(testNode.switchName, ovntest.MustParseIPNets(testNode.subnets...), nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, allocatedIP := range allocatedIPs {
					allocatedIPNets := ovntest.MustParseIPNets(allocatedIP)
					err = lsManager.AllocateIPs(testNode.switchName, allocatedIPNets)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}

				// count of available IP now should be 3;
				gomega.Expect(lsManager.AvailableIPsCount(testNode.switchName, true)).Should(gomega.Equal(int64(3)))

				// reserve 5 IPs and it should fail and all the partially allocated IPs must be released
				// total available IP to be 3
				ipnets, err := lsManager.AllocateIPsByCount(testNode.switchName, true, 5)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(ipnets).Should(gomega.BeNil())
				gomega.Expect(lsManager.AvailableIPsCount(testNode.switchName, true)).Should(gomega.Equal(int64(3)))

				// reserve 3 IPs and it should pass
				ipnets, err = lsManager.AllocateIPsByCount(testNode.switchName, true, 3)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(ipnets).Should(gomega.HaveLen(3))
				for i, ipnet := range ipnets {
					gomega.Expect(ipnet.String()).Should(gomega.Equal(expectedIPs[i]))
				}
				// check that the count now is 0
				gomega.Expect(lsManager.AvailableIPsCount(testNode.switchName, true)).Should(gomega.Equal(int64(0)))
				return nil
			}
			lsManager = NewL2SwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})
})

var _ = ginkgo.Describe("OVN Logical Switch Manager operations for layer2 user defined networks", func() {
	const (
		switchName = "testSwitch"
		ipv4Subnet = "192.168.200.0/24"
		ipv6Subnet = "fd12:1500::0/64"
	)
	var lsManager *LogicalSwitchManager

	ginkgo.BeforeEach(func() {
		lsManager = NewL2SwitchManagerForUserDefinedPrimaryNetwork(nil, nil)
		gomega.Expect(lsManager.AddOrUpdateSwitch(switchName, ovntest.MustParseIPNets(ipv4Subnet, ipv6Subnet), nil)).NotTo(gomega.HaveOccurred())
		gomega.Expect(lsManager.isAllocatedIP(switchName, "192.168.200.1/24")).To(gomega.BeTrue())
		gomega.Expect(lsManager.isAllocatedIP(switchName, "192.168.200.2/24")).To(gomega.BeTrue())
		gomega.Expect(lsManager.isAllocatedIP(switchName, "fd12:1500::1/64")).To(gomega.BeTrue())
		gomega.Expect(lsManager.isAllocatedIP(switchName, "fd12:1500::2/64")).To(gomega.BeTrue())
	})

	ginkgo.It("the first allocatable address for the workloads is the .3 IP", func() {
		allocatedIP, err := lsManager.AllocateNextIPs(switchName)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(allocatedIP).To(
			gomega.ConsistOf(
				&net.IPNet{
					IP:   net.ParseIP("192.168.200.3"),
					Mask: net.CIDRMask(24, 32),
				},
				&net.IPNet{
					IP:   net.ParseIP("fd12:1500::3"),
					Mask: net.CIDRMask(64, 128),
				},
			),
		)

		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(lsManager.isAllocatedIP(switchName, "192.168.200.3/24")).To(gomega.BeTrue())
		gomega.Expect(lsManager.isAllocatedIP(switchName, "fd12:1500::3/64")).To(gomega.BeTrue())
	})
})
