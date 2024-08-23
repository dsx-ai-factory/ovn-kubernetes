package networkprobe

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	networkprobe "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1"
	fakeNetworkProbeclient "github.com/ovn-org/ovn-kubernetes/go-controller/pkg/crd/networkprobe/v1beta1/apis/clientset/versioned/fake"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/factory"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/metrics"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func init() {
	config.IPv4Mode = true
	config.IPv6Mode = false
	config.OVNKubernetesFeature.EnableNetworkProbe = true
}

var (
	watchFactory *factory.WatchFactory
	stopChan     chan (struct{})

	fakeKubeClient         *fake.Clientset
	fakeNetworkProbeClient *fakeNetworkProbeclient.Clientset
	wg                     *sync.WaitGroup

	networkProbeName      = "networkprobe-1"
	networkProbeNamespace = "ovn-kubernetes"
)

func TestNetworkProbesWithoutIC(t *testing.T) {
	config.OVNKubernetesFeature.EnableInterconnect = false
	RegisterFailHandler(Fail)
	RunSpecs(t, "NetworkProbe Controller")
}

var _ = Describe("NetworkProbe Controller", func() {
	var (
		dnsServer     *dns.Server
		httpServer    *httptest.Server
		stopTCPServer func()
		stopUDPServer func()
	)

	BeforeEach(func() {
		ns0 := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: networkProbeNamespace,
				Labels: map[string]string{
					"app": "client",
				},
			},
		}

		nodeObj := &corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node1",
				Labels: map[string]string{
					"kubernetes.io/hostname": "node1",
					"networkprobe":           "enable",
				},
			},
		}

		initEnv([]runtime.Object{ns0, nodeObj}, []runtime.Object{})
		initNetworkProbeController()
	})

	AfterEach(func() {
		if dnsServer != nil {
			_ = dnsServer.Shutdown()
			dnsServer = nil
		}

		if httpServer != nil {
			httpServer.Close()
			httpServer = nil
		}

		if stopTCPServer != nil {
			stopTCPServer()
			stopTCPServer = nil
		}

		if stopUDPServer != nil {
			stopUDPServer()
			stopUDPServer = nil
		}

		shutdownController()
	})

	Context("Test DNS probes", func() {
		It("DNS metrics value should increase on successful DNS lookup", func() {
			dnsServer = startMockDnsServer()
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					DNSProbes: []networkprobe.DNSProbe{
						{
							Interval:   "1s",
							NameServer: "127.0.0.1",
							LookupName: "example.org",
						},
					},
				},
			}

			metrics.MetricDNSAttemptsTotal.Reset()
			metrics.MetricDNSCompletedTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricDNSAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "127.0.0.1", "example.org"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricDNSCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "127.0.0.1", "example.org"), 1.0)
		})

		It("DNS error metric value should increase after DNS lookup failure", func() {
			dnsServer = startMockDnsServer()
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					DNSProbes: []networkprobe.DNSProbe{
						{
							Interval:   "1s",
							NameServer: "127.0.0.1",
							LookupName: "nonexistent.org",
						},
					},
				},
			}

			metrics.MetricDNSAttemptsTotal.Reset()
			metrics.MetricDNSErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricDNSAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "127.0.0.1", "nonexistent.org"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricDNSErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "127.0.0.1", "nonexistent.org", "NXDOMAIN"), 1.0)
		})

		It("DNS error metric value should increase after DNS timeout failure", func() {
			dnsServer = startMockDnsServer()
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					DNSProbes: []networkprobe.DNSProbe{
						{
							Interval:   "6s",
							NameServer: "127.0.0.1",
							LookupName: "timeout.com",
						},
					},
				},
			}

			metrics.MetricDNSAttemptsTotal.Reset()
			metrics.MetricDNSErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricDNSAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "127.0.0.1", "timeout.com"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricDNSErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "127.0.0.1", "timeout.com", "TIMEOUT"), 1.0)
		})
	})

	Context("Test HTTP probes", func() {
		It("HTTP metrics value should increase on a successful connection", func() {
			httpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(50 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte("OK"))
				Expect(err).NotTo(HaveOccurred())
			}))

			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					HTTPProbes: []networkprobe.HTTPProbe{
						{
							URL:      httpServer.URL,
							Interval: "1s",
							Method:   "GET",
						},
					},
				},
			}

			metrics.MetricHttpAttemptsTotal.Reset()
			metrics.MetricHttpCompletedTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricHttpCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
		})

		It("HTTP error metric value should increase on http client error", func() {
			httpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(50 * time.Millisecond)
				http.Error(w, "Client Error", http.StatusBadRequest)
			}))

			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					HTTPProbes: []networkprobe.HTTPProbe{
						{
							URL:      httpServer.URL,
							Interval: "1s",
							Method:   "GET",
						},
					},
				},
			}
			metrics.MetricHttpAttemptsTotal.Reset()
			metrics.MetricHttpErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricHttpErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET", "CLIENT_ERROR"), 1.0)
		})

		It("HTTP error metric value should increase on http internal server error", func() {
			httpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(50 * time.Millisecond)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}))

			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					HTTPProbes: []networkprobe.HTTPProbe{
						{
							URL:      httpServer.URL,
							Interval: "1s",
							Method:   "GET",
						},
					},
				},
			}
			metrics.MetricHttpAttemptsTotal.Reset()
			metrics.MetricHttpErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricHttpErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET", "SERVER_ERROR"), 1.0)
		})

		It("HTTP error metric value should increase on http timeout error", func() {
			httpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(100 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte("OK"))
				Expect(err).NotTo(HaveOccurred())
			}))

			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					HTTPProbes: []networkprobe.HTTPProbe{
						{
							URL:      httpServer.URL,
							Interval: "20ms",
							Method:   "GET",
						},
					},
				},
			}
			metrics.MetricHttpAttemptsTotal.Reset()
			metrics.MetricHttpErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricHttpErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET", "TIMEOUT"), 1.0)
		})
	})

	Context("Test TCP probes", func() {
		It("TCP metrics value should increase after successful connection", func() {
			var tcpPort int32 = 12345
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					TCPProbes: []networkprobe.TCPProbe{
						{
							Host:     "localhost",
							Port:     &tcpPort,
							Interval: "30ms",
						},
					},
				},
			}

			stopTCPServer = StartMockTCPServer("12345", func(conn net.Conn) {
				defer conn.Close()
			})
			metrics.MetricTCPCompletedTotal.Reset()
			metrics.MetricTCPAttemptsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			eventuallyEvaluateMetric(metrics.MetricTCPCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12345"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricTCPAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12345"), 1.0)

		})

		It("TCP error metric value should increase after a connection failure", func() {
			var tcpErrorPort int32 = 12346
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					TCPProbes: []networkprobe.TCPProbe{
						{
							Host:     "localhost",
							Port:     &tcpErrorPort,
							Interval: "1s",
						},
					},
				},
			}

			metrics.MetricTCPAttemptsTotal.Reset()
			metrics.MetricTCPErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricTCPAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12346"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricTCPErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12346", "CONNECTION_REFUSED"), 1.0)

		})

		It("TCP error metric value should increase after a connection timeout", func() {
			var tcpTimeoutPort int32 = 12346
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					TCPProbes: []networkprobe.TCPProbe{
						{
							Host:     "10.1.128.10",
							Port:     &tcpTimeoutPort,
							Interval: "5s",
						},
					},
				},
			}

			metrics.MetricTCPAttemptsTotal.Reset()
			metrics.MetricTCPErrorsTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricTCPAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "10.1.128.10", "12346"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricTCPErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "10.1.128.10", "12346", "TIMEOUT"), 1.0)

		})
	})

	Context("Test UDP probes", func() {
		var udpPort int32 = 12350
		var udpPacketCount int32 = 5

		It("UDP metrics value should increase after successful connection", func() {
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					UDPStreamProbes: []networkprobe.UDPStreamProbe{
						{
							Host:           "localhost",
							Port:           &udpPort,
							PacketCount:    &udpPacketCount,
							PacketInterval: "30ms",
							Interval:       "10s",
						},
					},
				},
			}

			metrics.MetricUDPCompletedTotal.Reset()
			metrics.MetricUDPAttemptsTotal.Reset()
			stopUDPServer = StartMockUDPServer(":12350")
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			eventuallyEvaluateMetric(metrics.MetricUDPCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12350"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricUDPAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12350"), 1.0)

		})

		It("UDP error metrics should increase after connection timeout", func() {
			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					UDPStreamProbes: []networkprobe.UDPStreamProbe{
						{
							Host:           "localhost",
							Port:           &udpPort,
							PacketCount:    &udpPacketCount,
							PacketInterval: "30ms",
							Interval:       "5s",
						},
					},
				},
			}

			metrics.MetricUDPCompletedTotal.Reset()
			metrics.MetricUDPAttemptsTotal.Reset()
			// listening on 12351 port instead of 12350
			stopUDPServer = StartMockUDPServer(":12351")
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())

			eventuallyEvaluateMetric(metrics.MetricUDPErrorsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12350", "TIMEOUT_ERROR"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricUDPAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, "localhost", "12350"), 1.0)

		})

	})

	Context("Test Suspend functionality for network probes", func() {
		It("probe metrics should not increase after the probe is suspended", func() {
			httpServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				time.Sleep(50 * time.Millisecond)
				w.WriteHeader(http.StatusOK)
				_, err := w.Write([]byte("OK"))
				Expect(err).NotTo(HaveOccurred())
			}))

			networkProbeObj := &networkprobe.NetworkProbe{
				TypeMeta: metav1.TypeMeta{
					Kind:       "NetworkProbe",
					APIVersion: "k8s.ovn.org/v1beta1",
				},
				ObjectMeta: metav1.ObjectMeta{
					Name:      networkProbeName,
					Namespace: networkProbeNamespace,
				},
				Spec: networkprobe.NetworkProbeSpec{
					NodeSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"networkprobe": "enable",
						},
					},
					Suspend: false,
					HTTPProbes: []networkprobe.HTTPProbe{
						{
							URL:      httpServer.URL,
							Interval: "1s",
							Method:   "GET",
						},
					},
				},
			}

			metrics.MetricHttpAttemptsTotal.Reset()
			metrics.MetricHttpCompletedTotal.Reset()
			_, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Create(context.TODO(), networkProbeObj, metav1.CreateOptions{})
			Expect(err).NotTo(HaveOccurred())
			eventuallyEvaluateMetric(metrics.MetricHttpCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
			eventuallyEvaluateMetric(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"), 1.0)
			// suspend the probe
			updateNetworkProbeObj, err := fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Get(context.TODO(), networkProbeName, metav1.GetOptions{})
			Expect(err).NotTo(HaveOccurred())
			updateNetworkProbeObj.Spec.Suspend = true
			updateNetworkProbeObj.ResourceVersion = time.Now().String()
			_, err = fakeNetworkProbeClient.K8sV1beta1().NetworkProbes(networkProbeNamespace).Update(context.TODO(), updateNetworkProbeObj, metav1.UpdateOptions{})
			Expect(err).NotTo(HaveOccurred())

			metricCompletedValue := testutil.ToFloat64(metrics.MetricHttpCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"))
			Consistently(func() bool {
				metricValue := testutil.ToFloat64(metrics.MetricHttpCompletedTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"))
				return metricValue == metricCompletedValue
			}).WithTimeout(5 * time.Second).WithPolling(500 * time.Millisecond).Should(BeTrue())

			metricAttemptedValue := testutil.ToFloat64(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"))
			Consistently(func() bool {
				metricValue := testutil.ToFloat64(metrics.MetricHttpAttemptsTotal.WithLabelValues(networkProbeName, networkProbeNamespace, httpServer.URL, "GET"))
				return metricValue == metricAttemptedValue
			}).WithTimeout(5 * time.Second).WithPolling(500 * time.Millisecond).Should(BeTrue())

		})
	})
})

// StartMockTCPServer starts a mock TCP server on the given port.
// It accepts a handler function to process each connection.
// Returns a function to stop the server.
func StartMockTCPServer(port string, handler func(conn net.Conn)) func() {
	ln, err := net.Listen("tcp", ":"+port)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error listening on TCP address: %v", err))

	tcpStopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	// Server loop
	go func() {
		defer func() {
			GinkgoRecover()
			wg.Done()
		}()

		for {
			select {
			case <-tcpStopCh:
				return
			default:
				conn, err := ln.Accept()
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error accepting a TCP connection: %v", err))
				handler(conn)
			}
		}
	}()

	// function to stop the server
	return func() {
		close(tcpStopCh)
		wg.Wait()
		ln.Close()
	}
}

// handleDNSRequest handles the DNS requests
func handleDNSRequest(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)

	for _, q := range r.Question {
		if q.Name == "example.org." && q.Qtype == dns.TypeA {
			aRecord, _ := net.ResolveIPAddr("ip", "1.2.3.4")
			rr := &dns.A{
				Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
				A:   aRecord.IP,
			}
			m.Answer = append(m.Answer, rr)
		} else if q.Name == "timeout.com." {
			// simulate timeout as DNSLookupTimeOut is 5 sec
			time.Sleep(6 * time.Second)
			return
		} else {
			// Return NXDOMAIN for unknown queries
			m.Rcode = dns.RcodeNameError
		}
	}
	_ = w.WriteMsg(m)
}

func startMockDnsServer() *dns.Server {
	dns.HandleFunc(".", handleDNSRequest)
	// Define the DNS server listening on port 53
	dnsServer := &dns.Server{
		Addr:    "127.0.0.1:53",
		Net:     "udp",
		Handler: dns.DefaultServeMux,
	}

	go func() {
		defer GinkgoRecover()
		err := dnsServer.ListenAndServe()
		Expect(err).NotTo(HaveOccurred())
	}()
	return dnsServer
}

// StartMockUDPServer starts a UDP server that echoes back received packets with a timestamp.
func StartMockUDPServer(addr string) func() {

	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error resolving UDP address: %v", err))

	conn, err := net.ListenUDP("udp", udpAddr)
	Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error starting UDP server: %v", err))

	var wg sync.WaitGroup
	udpStopCh := make(chan struct{})
	wg.Add(1)
	go func() {

		defer func() {
			wg.Done()
			GinkgoRecover()
		}()

		for {
			select {
			case <-udpStopCh:
				return
			default:
				buffer := make([]byte, 2048)
				err = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error setting read deadline: %v", err))
				n, clientAddr, err := conn.ReadFromUDP(buffer)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}
					Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error reading from UDP buffer: %v", err))
				}

				receiveTime := time.Now().UnixNano()

				var packet Packet
				err = json.Unmarshal(buffer[:n], &packet)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error decoding UDP packet: %v", err))

				packet.ReceiverSideReceiveTime = receiveTime
				packet.ReceiverSideSendTime = time.Now().UnixNano()

				responseBuffer, err := json.Marshal(packet)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error encoding UDP packet: %v", err))

				_, err = conn.WriteToUDP(responseBuffer, clientAddr)
				Expect(err).NotTo(HaveOccurred(), fmt.Sprintf("error writing to UDP buffer: %v", err))
			}
		}
	}()

	// Signal goroutine to exit
	closeFunc := func() {
		close(udpStopCh)
		wg.Wait()
		err := conn.Close()
		Expect(err).NotTo(HaveOccurred(), "error closing UDP connection")
	}
	return closeFunc
}

func initEnv(k8sObjects []runtime.Object, networkProbeObjects []runtime.Object) {
	var err error
	stopChan = make(chan struct{})
	wg = &sync.WaitGroup{}
	nodeNames := []string{"node1"}
	fakeKubeClient = fake.NewSimpleClientset(k8sObjects...)
	fakeNetworkProbeClient = fakeNetworkProbeclient.NewSimpleClientset(networkProbeObjects...)
	watchFactory, err = factory.NewNodeWatchFactory(
		&util.OVNNodeClientset{
			KubeClient:         fakeKubeClient,
			NetworkProbeClient: fakeNetworkProbeClient,
		},
		nodeNames)
	Expect(err).NotTo(HaveOccurred())
}

func initNetworkProbeController() {
	networkProbeController, err := NewController(
		fakeNetworkProbeClient,
		stopChan,
		watchFactory.NetworkProbeInformer(),
		watchFactory.NodeCoreInformer(),
		watchFactory.ConfigMapCoreInformer(),
		watchFactory.SecretCoreInformer(), "node1", util.EventRecorder(fakeKubeClient))

	Expect(err).NotTo(HaveOccurred())
	err = watchFactory.Start()
	Expect(err).NotTo(HaveOccurred())
	err = networkProbeController.Run(wg, 1)
	Expect(err).NotTo(HaveOccurred())
}

func shutdownController() {
	if watchFactory != nil {
		watchFactory.Shutdown()
		watchFactory = nil
	}
	if stopChan != nil {
		close(stopChan)
		stopChan = nil
	}
}

func eventuallyEvaluateMetric(metric prometheus.Counter, threshold float64) {
	Eventually(func() bool {
		metricValue := testutil.ToFloat64(metric)
		return metricValue > threshold
	}).WithTimeout(20 * time.Second).WithPolling(500 * time.Millisecond).Should(BeTrue())
}
