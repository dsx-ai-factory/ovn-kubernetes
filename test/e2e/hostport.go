// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	e2enode "k8s.io/kubernetes/test/e2e/framework/node"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
)

// Run against a cluster with pod hostPort enabled and runtime/CNI port
// mapping disabled: OVNKUBE_POD_HOST_PORT_ENABLE=true, focus "Pod hostPort".
var _ = ginkgo.Describe("Pod hostPort", ginkgo.Serial, func() {
	f := wrappedTestFramework("hostport")

	ginkgo.BeforeEach(func() {
		if os.Getenv("OVNKUBE_POD_HOST_PORT_ENABLE") != "true" {
			ginkgo.Skip("requires --enable-pod-host-port on full-mode nodes with shared gateways")
		}
	})

	ginkgo.It("publishes TCP and UDP on the same port and reconciles pod replacement", func() {
		ctx := context.Background()
		nodes, err := e2enode.GetReadySchedulableNodes(ctx, f.ClientSet)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		if len(nodes.Items) < 2 {
			ginkgo.Skip("requires two schedulable nodes")
		}
		node, otherNode := nodes.Items[0].Name, nodes.Items[1].Name
		ipv4, ipv6 := getNodeAddresses(&nodes.Items[0])
		hostIP := ipv4
		if hostIP == "" {
			hostIP = ipv6
		}
		gomega.Expect(hostIP).NotTo(gomega.BeEmpty())

		server := func(name, ip string, protocol v1.Protocol, port int32) *v1.Pod {
			pod, err := createPod(f, name, node, f.Namespace.Name,
				[]string{"/agnhost", "netexec", "--http-port=8080", "--udp-port=8080"}, nil,
				func(p *v1.Pod) {
					p.Spec.Containers[0].Ports = []v1.ContainerPort{{ContainerPort: 8080, HostPort: port, HostIP: ip, Protocol: protocol}}
				})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return pod
		}
		tcp := server("hp-tcp", hostIP, v1.ProtocolTCP, 54324)
		udp := server("hp-udp", hostIP, v1.ProtocolUDP, 54324)
		wild := server("hp-wildcard", "", v1.ProtocolTCP, 54325)
		var loopback *v1.Pod
		if ipv4 != "" {
			loopback = server("hp-loopback", "127.0.0.1", v1.ProtocolTCP, 54324)
		}

		client := func(name, node string, hostNetwork bool) *v1.Pod {
			pod, err := createPod(f, name, node, f.Namespace.Name,
				[]string{"/agnhost", "netexec", "--http-port=18080", "--udp-port=-1"}, nil,
				func(p *v1.Pod) { p.Spec.HostNetwork = hostNetwork })
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			return pod
		}
		localHost := client("hp-local-host", node, true)
		remoteHost := client("hp-remote-host", otherNode, true)
		peer := client("hp-peer", node, false)
		probe := func(from *v1.Pod, destination string, port int, protocol v1.Protocol) (string, error) {
			exec := ForPod(f.Namespace.Name, from.Name, from.Spec.Containers[0].Name)
			if protocol == v1.ProtocolTCP {
				out, err := exec.Exec("curl", "--noproxy", "*", "-fsS", "--max-time", "2", "http://"+net.JoinHostPort(destination, fmt.Sprint(port))+"/hostname")
				return strings.TrimSpace(out), err
			}
			query := url.Values{"host": {destination}, "port": {fmt.Sprint(port)}, "protocol": {"udp"}, "request": {"hostname"}, "tries": {"1"}}
			out, err := exec.Exec("curl", "--noproxy", "*", "-fsS", "--max-time", "5", "http://127.0.0.1:18080/dial?"+query.Encode())
			if err != nil {
				return "", err
			}
			var response struct {
				Responses []string `json:"responses"`
				Errors    []string `json:"errors"`
			}
			if err := json.Unmarshal([]byte(out), &response); err != nil {
				return "", err
			}
			if len(response.Responses) != 1 {
				return "", fmt.Errorf("UDP dial failed: %s", out)
			}
			return strings.TrimSpace(response.Responses[0]), nil
		}
		check := func(from *v1.Pod, destination string, port int, protocol v1.Protocol, name string) {
			gomega.Eventually(func() (string, error) { return probe(from, destination, port, protocol) }, time.Minute, time.Second).Should(gomega.Equal(name))
		}
		for _, from := range []*v1.Pod{localHost, remoteHost, peer} {
			check(from, hostIP, 54324, v1.ProtocolTCP, tcp.Name)
			check(from, hostIP, 54324, v1.ProtocolUDP, udp.Name)
			for _, ip := range []string{ipv4, ipv6} {
				if ip != "" {
					check(from, ip, 54325, v1.ProtocolTCP, wild.Name)
				}
			}
		}
		check(tcp, hostIP, 54324, v1.ProtocolTCP, tcp.Name) // self-hairpin
		if loopback != nil {
			check(localHost, "127.0.0.1", 54324, v1.ProtocolTCP, loopback.Name)
		}

		e2epod.NewPodClient(f).DeleteSync(ctx, tcp.Name, metav1.DeleteOptions{}, e2epod.DefaultPodDeletionTimeout)
		gomega.Eventually(func() error { _, err := probe(remoteHost, hostIP, 54324, v1.ProtocolTCP); return err }, time.Minute, time.Second).Should(gomega.HaveOccurred())
		check(remoteHost, hostIP, 54324, v1.ProtocolUDP, udp.Name)
		replacement := server("hp-tcp-replacement", hostIP, v1.ProtocolTCP, 54324)
		check(remoteHost, hostIP, 54324, v1.ProtocolTCP, replacement.Name)
	})
})
