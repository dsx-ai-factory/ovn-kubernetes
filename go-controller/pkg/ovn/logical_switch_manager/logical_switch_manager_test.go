package logicalswitchmanager

import (
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	ovntest "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/testing"
	ovntypes "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/types"
	"github.com/urfave/cli/v2"
	"k8s.io/klog/v2"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
)

type testNodeSubnetData struct {
	nodeName string
	subnets  []string //IP subnets in string format e.g. 10.1.1.0/24
}

var _ = ginkgo.Describe("OVN Logical Switch Manager operations", func() {
	var (
		app       *cli.App
		fexec     *ovntest.FakeExec
		lsManager *LogicalSwitchManager
	)

	ginkgo.BeforeEach(func() {
		// Restore global default values before each testcase
		config.PrepareTestConfig()

		app = cli.NewApp()
		app.Name = "test"
		app.Flags = config.Flags
		lsManager = NewLogicalSwitchManager()
	})

	ginkgo.Context("when adding node", func() {
		ginkgo.It("creates IPAM for each subnet and reserves IPs correctly", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				expectedIPs := []string{"10.1.1.3", "2000::3"}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ips, err := lsManager.AllocateNextIPs(testNode.nodeName)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for i, ip := range ips {
					gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
				}

				// run the test for hybrid overlay enabled case
				testHONode := testNodeSubnetData{
					nodeName: "testNode2",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}
				config.HybridOverlay.Enabled = true
				expectedIPs = []string{"10.1.1.4", "2000::4"}
				err = lsManager.AddNode(testHONode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ips, err = lsManager.AllocateNextIPs(testHONode.nodeName)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for i, ip := range ips {
					gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
				}

				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("manages no host subnet nodes correctly", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets:  []string{},
				}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				noHostSubnet := lsManager.IsNonHostSubnetSwitch(testNode.nodeName)
				gomega.Expect(noHostSubnet).To(gomega.BeTrue())
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("handles updates to the host subnets correctly", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				expectedIPs := []string{"10.1.1.3", "2000::3"}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ips, err := lsManager.AllocateNextIPs(testNode.nodeName)
				for i, ip := range ips {
					gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
				}
				testNode.subnets = []string{"10.1.2.0/24"}
				expectedIPs = []string{"10.1.2.3"}
				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				ips, err = lsManager.AllocateNextIPs(testNode.nodeName)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for i, ip := range ips {
					gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
				}
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

	})

	ginkgo.Context("when allocating IP addresses", func() {
		ginkgo.It("IPAM for each subnet allocates IPs contiguously", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				expectedIPAllocations := [][]string{
					{"10.1.1.3", "2000::3"},
					{"10.1.1.4", "2000::4"},
				}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, expectedIPs := range expectedIPAllocations {
					ips, err := lsManager.AllocateNextIPs(testNode.nodeName)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					for i, ip := range ips {
						gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
					}
				}
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("IPAM allocates, releases, and reallocates IPs correctly", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
					},
				}

				expectedIPAllocations := [][]string{
					{"10.1.1.3"},
					{"10.1.1.4"},
				}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, expectedIPs := range expectedIPAllocations {
					ips, err := lsManager.AllocateNextIPs(testNode.nodeName)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					for i, ip := range ips {
						gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
					}
					err = lsManager.ReleaseIPs(testNode.nodeName, ips)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					err = lsManager.AllocateIPs(testNode.nodeName, ips)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("releases IPs for other host subnet nodes when any host subnets allocation fails", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"10.1.2.0/29",
					},
				}
				config.HybridOverlay.Enabled = true
				expectedIPAllocations := [][]string{
					{"10.1.1.4", "10.1.2.4"},
					{"10.1.1.5", "10.1.2.5"},
					{"10.1.1.6", "10.1.2.6"},
				}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				// exhaust valid ips in second subnet
				for _, expectedIPs := range expectedIPAllocations {
					ips, err := lsManager.AllocateNextIPs(testNode.nodeName)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					for i, ip := range ips {
						gomega.Expect(ip.IP.String()).To(gomega.Equal(expectedIPs[i]))
					}
				}
				// now try one more allocation and expect it to fail
				ips, err := lsManager.AllocateNextIPs(testNode.nodeName)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(len(ips)).To(gomega.Equal(0))
				return nil
			}
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("fails correctly when trying to block a previously allocated IP", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: "testNode1",
					subnets: []string{
						"10.1.1.0/24",
						"2000::/64",
					},
				}

				allocatedIPs := []string{
					"10.1.1.2/24",
					"2000::2/64",
				}
				allocatedIPNets := ovntest.MustParseIPNets(allocatedIPs...)
				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = lsManager.AllocateIPs(testNode.nodeName, allocatedIPNets)
				klog.Errorf("Error: %v", err)
				gomega.Expect(err).To(gomega.HaveOccurred())
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
					nodeName: ovntypes.OVNLocalnetSwitch,
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
				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, allocatedIP := range allocatedIPs {
					allocatedIPNets := ovntest.MustParseIPNets(allocatedIP)
					err = lsManager.AllocateIPs(testNode.nodeName, allocatedIPNets)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}
				gomega.Expect(lsManager.AvailableIPsCount(testNode.nodeName, true)).Should(gomega.Equal(int64(247)))

				return nil
			}
			lsManager = NewLocalnetSwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("fails while counting available number of IPs for IPv6", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/24",
						"2000::/120",
					},
				}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				_, err = lsManager.AvailableIPsCount(testNode.nodeName, false)
				gomega.Expect(err).To(gomega.HaveOccurred())
				return nil
			}
			lsManager = NewLocalnetSwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("counts correctly when no IPs have been allocated", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: ovntypes.OVNLocalnetSwitch,
					subnets: []string{
						"10.1.1.0/24",
					},
				}

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(lsManager.AvailableIPsCount(testNode.nodeName, true)).Should(gomega.Equal(int64(254)))

				return nil
			}
			lsManager = NewLocalnetSwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("correctly reserve requested number of IPs", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: ovntypes.OVNLocalnetSwitch,
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

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, allocatedIP := range allocatedIPs {
					allocatedIPNets := ovntest.MustParseIPNets(allocatedIP)
					err = lsManager.AllocateIPs(testNode.nodeName, allocatedIPNets)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}

				// count of available IP now should be 246; reserve 5 IPs
				ipnets, err := lsManager.AllocateIPsByCount(testNode.nodeName, true, 5)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(ipnets)).Should(gomega.Equal(5))
				for i, ipnet := range ipnets {
					gomega.Expect(ipnet.String()).Should(gomega.Equal(expectedIPs[i]))
				}
				// check that the count now is 241
				gomega.Expect(lsManager.AvailableIPsCount(testNode.nodeName, true)).Should(gomega.Equal(int64(241)))
				return nil
			}
			lsManager = NewLocalnetSwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		ginkgo.It("correctly releases partially allocated IPs on failure", func() {
			app.Action = func(ctx *cli.Context) error {
				_, err := config.InitConfig(ctx, fexec, nil)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				testNode := testNodeSubnetData{
					nodeName: ovntypes.OVNLocalnetSwitch,
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

				err = lsManager.AddNode(testNode.nodeName, "", ovntest.MustParseIPNets(testNode.subnets...))
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				for _, allocatedIP := range allocatedIPs {
					allocatedIPNets := ovntest.MustParseIPNets(allocatedIP)
					err = lsManager.AllocateIPs(testNode.nodeName, allocatedIPNets)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
				}

				// count of available IP now should be 3;
				gomega.Expect(lsManager.AvailableIPsCount(testNode.nodeName, true)).Should(gomega.Equal(int64(3)))

				// reserve 5 IPs and it should fail and all the partially allocated IPs must be released
				// total available IP to be 3
				ipnets, err := lsManager.AllocateIPsByCount(testNode.nodeName, true, 5)
				gomega.Expect(err).To(gomega.HaveOccurred())
				gomega.Expect(ipnets).Should(gomega.BeNil())
				gomega.Expect(lsManager.AvailableIPsCount(testNode.nodeName, true)).Should(gomega.Equal(int64(3)))

				// reserve 3 IPs and it should pass
				ipnets, err = lsManager.AllocateIPsByCount(testNode.nodeName, true, 3)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(len(ipnets)).Should(gomega.Equal(3))
				for i, ipnet := range ipnets {
					gomega.Expect(ipnet.String()).Should(gomega.Equal(expectedIPs[i]))
				}
				// check that the count now is 0
				gomega.Expect(lsManager.AvailableIPsCount(testNode.nodeName, true)).Should(gomega.Equal(int64(0)))
				return nil
			}
			lsManager = NewLocalnetSwitchManager()
			err := app.Run([]string{app.Name})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})
	})

})
