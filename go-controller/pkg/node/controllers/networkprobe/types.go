package networkprobe

import (
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const (
	// maxRetries is the number of times a object will be retried before it is dropped out of the networkProbeQueue.
	// With the current rate-limiter in use (5ms*2^(maxRetries-1)) the following numbers represent the
	// sequence of delays between successive queuings of an object.
	//
	// 5ms, 10ms, 20ms, 40ms, 80ms, 160ms, 320ms, 640ms, 1.3s, 2.6s, 5.1s, 10.2s, 20.4s, 41s, 82s
	maxRetries = 10

	// Adjust the value accordingly to set the DNSLookup timeout
	DNSLookupTimeOut     = 5 * time.Second
	TCPConnectionTimeout = 5 * time.Second

	// Adjust the value accordingly this sets when the time upto when you want to wait
	DefaultReadTimeout = 5 * time.Second

	// Buffer Size to to receive TCP and UDP packets
	TCPBufferSize = 1500
	UDPBufferSize = 1500

	// MAX PAYLOAD size is 948, accounting for various fields
	// 1440 - 20 bytes for IPv4 header + 20 bytes for TCP header
	// and to account for This includes the size of Sequence (uint32) and SendTime (int64)
	// remember this is important because we are not checking for segmentation and other stuff,
	// these metrics are only packet-level telemetry
	MaxPayloadSize = 948

	// HTTP/HTTPS modes
	ModeGET     = "GET"
	ModeHEAD    = "HEAD"
	ModePOST    = "POST"
	ModeCONNECT = "CONNECT"
	ModeOPTIONS = "OPTIONS"

	networkProbeReadyStatusType = "Ready-In-Node-"
	networkProbeReadyReason     = "Running"
	networkProbeNotReadyReason  = "Failed"
	networkProbeSuspendedReason = "Suspended"
)

type DnsProbe struct {
	dnsLookupName string
	ipAddress     string
	nameServer    string
	interval      string
	packetSpec    pktSpec
}

type HttpProbe struct {
	url        string
	tlsConfig  *TLSConfig
	interval   string
	packetSpec pktSpec
	method     string
	headers    map[string]string // New field for custom headers
}

type TLSConfig struct {
	// ConfigMap containing data to use for the targets.
	CAConfigMap *corev1.ConfigMapKeySelector
	// secret containing data to use for the targets
	CASecret *corev1.SecretKeySelector
	// Disable target certificate validation.
	InsecureSkipVerify *bool
}

type TcpProbe struct {
	host       string
	port       *int32
	interval   string
	packetSpec pktSpec
}

type pktSpec struct {
	dscp int
	// TODO Support higher payload Sizes, current max packet size is 1500.
	payloadSize int
}

type UdpProbe struct {
	host           string
	port           *int32
	interval       string
	packetCount    int
	packetInterval string
	packetSpec     pktSpec
}

type Packet struct {
	Sequence                uint32 `json:"sequence"`
	SenderSideSendTime      int64  `json:"senderSideSendTime"`
	SenderSideReceiveTime   int64  `json:"senderSideReceiveTime"`
	ReceiverSideReceiveTime int64  `json:"receiverSideReceiveTime"`
	ReceiverSideSendTime    int64  `json:"receiverSideSendTime"`
	Payload                 []byte `json:"payload"`
}

type networkProbeSpecInfo struct {
	dnsProbes  []*DnsProbe
	httpProbes []*HttpProbe
	udpProbes  []*UdpProbe
	tcpProbes  []*TcpProbe
	interval   string
}

type NetworkProbeState struct {
	name       string
	namespace  string
	probeMutex sync.Mutex
	stopCh     chan struct{}
	wg         *sync.WaitGroup
	// network probe spec info
	networkProbeSpecInfo
	// isProbeRunningOnThisNode is set when corresponding node label matches
	// network probe node selector label
	isProbeRunningOnThisNode bool
	nodeSelector             labels.Selector
	suspended                bool
}
