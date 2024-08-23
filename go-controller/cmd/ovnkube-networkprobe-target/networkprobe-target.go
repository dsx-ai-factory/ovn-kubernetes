package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"k8s.io/klog/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/node/controllers/networkprobe"
)

const (
	DEFAULT_UDP_PORT    = "12345"
	DEFAULT_BUFFER_SIZE = "1024"
)

func getEnvWithDefault(key, defaultValue string) string {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	return value
}

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	udpPort := getEnvWithDefault("UDP_PORT", DEFAULT_UDP_PORT)
	bufferSizeStr := getEnvWithDefault("BUFFER_SIZE", DEFAULT_BUFFER_SIZE)
	bufferSize, err := strconv.Atoi(bufferSizeStr)
	if err != nil {
		klog.Errorf("Invalid BUFFER_SIZE: %v. Using default 1024.", err)
		bufferSize, _ = strconv.Atoi(DEFAULT_BUFFER_SIZE)
	}

	udpAddress := "0.0.0.0:" + udpPort

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	wg.Add(1)
	go runUDPServer(ctx, &wg, udpAddress, bufferSize)

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	klog.Info("Shutting down UDP server...")

	cancel()
	wg.Wait()
}

func runUDPServer(ctx context.Context, wg *sync.WaitGroup, address string, bufferSize int) {
	defer wg.Done()

	pc, err := net.ListenPacket("udp", address)
	if err != nil {
		klog.Errorf("Error listening on UDP %s: %v", address, err)
		return
	}
	defer pc.Close()

	klog.Infof("Listening on UDP %s", address)

	for {
		select {
		case <-ctx.Done():
			klog.Infof("UDP server stopping")
			return
		default:
			buffer := make([]byte, bufferSize)
			if err := pc.SetReadDeadline(time.Now().Add(1 * time.Second)); err != nil {
				klog.Errorf("Error setting read deadline for UDP connection: %v", err)
				continue
			}
			n, addr, err := pc.ReadFrom(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				klog.Errorf("Error reading from UDP: %v", err)
				continue
			}

			receiveTime := time.Now().UnixNano()

			var packet networkprobe.Packet
			if err := json.Unmarshal(buffer[:n], &packet); err != nil {
				klog.Errorf("Error decoding UDP packet: %v", err)
				continue
			}

			packet.ReceiverSideReceiveTime = receiveTime
			packet.ReceiverSideSendTime = time.Now().UnixNano()

			responseBuffer, err := json.Marshal(packet)
			if err != nil {
				klog.Errorf("Error encoding UDP response packet: %v", err)
				continue
			}

			_, err = pc.WriteTo(responseBuffer, addr)
			if err != nil {
				klog.Errorf("Error sending UDP response to %s: %v", addr, err)
				continue
			}

			klog.Infof("Processed and echoed UDP packet %+v from %s", packet, addr)
		}
	}
}
