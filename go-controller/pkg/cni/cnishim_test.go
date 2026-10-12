// SPDX-FileCopyrightText: Copyright The OVN-Kubernetes Contributors
// SPDX-License-Identifier: Apache-2.0

package cni

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/containernetworking/cni/pkg/skel"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-kubernetes/ovn-kubernetes/go-controller/pkg/util"
)

func TestCmdAdd_PrivilegedMode(t *testing.T) {
	// Setup: CNI server returns non-nil Result
	p := &Plugin{}

	// Patch doCNI to return a response with non-nil Result that indicates server wired
	result := &current.Result{Interfaces: []*current.Interface{{Name: "serverWired"}}}

	kubeAuth := &KubeAPIAuth{
		Kubeconfig:       config.Kubernetes.Kubeconfig,
		KubeAPIServer:    config.Kubernetes.APIServer,
		KubeAPIToken:     config.Kubernetes.Token,
		KubeAPITokenFile: config.Kubernetes.TokenFile,
	}
	body, _ := json.Marshal(&Response{
		Result:    result,
		PodIFInfo: &PodInterfaceInfo{},
		KubeAuth:  kubeAuth,
	})
	p.doCNIFunc = func(_ string, _ interface{}) ([]byte, error) {
		return body, nil
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() {
		os.Stdout = oldStdout
	}()

	args := &skel.CmdArgs{
		StdinData:   []byte(`{"cniVersion":"1.1.0","name":"mynet","type":"ovn-k8s-cni-overlay"}`),
		ContainerID: "cid",
		Netns:       "/var/run/netns/test",
		IfName:      "eth0",
	}
	err := p.CmdAdd(args)
	require.NoError(t, err)
	w.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	output := buf.String()
	var got, want map[string]any
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("failed to unmarshal output: %v", err)
	}

	expected := `{
    "cniVersion": "1.1.0",
    "interfaces": [
        {
            "name": "serverWired"
        }
    ]
}`
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatalf("failed to unmarshal expected: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected CmdAdd output:\n got=%v\nwant=%v", got, want)
	}
}

func TestCmdAdd_UnprivilegedMode(t *testing.T) {
	withCNIEnv(t, func() {
		p := &Plugin{}
		err := config.PrepareTestConfig()
		require.NoError(t, err, "failed to prepare test config")
		config.OVNKubernetesFeature.EnableMultiNetwork = true
		config.OVNKubernetesFeature.EnableNetworkSegmentation = true

		podRequestInterfaceOps = &podRequestInterfaceOpsStub{}
		defer func() { podRequestInterfaceOps = &defaultPodRequestInterfaceOps{} }()
		pr := PodRequest{
			PodNamespace: "foo-ns",
			PodName:      "bar-pod",
			timestamp:    time.Time{},
		}
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pr.PodName,
				Namespace: pr.PodNamespace,
				Annotations: map[string]string{
					"k8s.ovn.org/pod-networks": `{"default":{"ip_addresses":["100.10.10.3/24","fd44::33/64"],"mac_address":
"0a:58:fd:98:00:01", "role":"infrastructure-locked"}, "foo-ns/meganet":{"ip_addresses":["10.10.10.30/24","fd10::3/64"],
"mac_address":"02:03:04:05:06:07", "role":"primary"}}`,
				},
			},
		}
		defaultPodNADAnnotation, err := util.UnmarshalPodAnnotation(pod.Annotations, "default")
		require.NoError(t, err)
		udnPodNADAnnotation, err := util.UnmarshalPodAnnotation(pod.Annotations, "foo-ns/meganet")
		require.NoError(t, err)

		// Fake Response: no Result, but PrimaryUDNPodInfo + PrimaryUDNPodRequest populated
		resp := &Response{
			Result: nil,
			PodIFInfo: &PodInterfaceInfo{
				PodAnnotation: *defaultPodNADAnnotation,
				NetName:       "default",
				NADKey:        "foo-ns/default",
				// hack to bypass OVS exec check
				IsDPUHostMode: true,
			},
			PrimaryUDNPodInfo: &PodInterfaceInfo{
				PodAnnotation: *udnPodNADAnnotation,
				NetName:       "tenantred",
				NADKey:        "foo-ns/meganet",
				// hack to bypass OVS exec check
				IsDPUHostMode: true,
			},
			PrimaryUDNPodReq: &PodRequest{
				PodNamespace: "default",
				PodName:      "testpod",
				IfName:       "dummy1",
			},
			KubeAuth: &KubeAPIAuth{},
		}

		body, _ := json.Marshal(resp)

		// Mock doCNI to return our fake response
		p.doCNIFunc = func(_ string, _ interface{}) ([]byte, error) {
			return body, nil
		}

		// Capture stdout (CmdAdd prints)
		oldStdout := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		defer func() {
			os.Stdout = oldStdout
		}()

		args := &skel.CmdArgs{
			StdinData:   []byte(`{"cniVersion":"1.1.0","name":"mynet","type":"ovn-k8s-cni-overlay"}`),
			ContainerID: "cid",
			Netns:       "/var/run/netns/test",
			IfName:      "eth0",
		}
		err = p.CmdAdd(args)
		require.NoError(t, err)

		// Collect stdout
		w.Close()
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		output := buf.String()

		// Unmarshal output for comparison
		var got map[string]any
		if err := json.Unmarshal([]byte(output), &got); err != nil {
			t.Fatalf("failed to unmarshal output: %v", err)
		}

		// Expected output includes both interfaces wired by CNIShim
		expected := `{
  "cniVersion": "1.1.0",
  "interfaces": [
    {
      "name": "host_eth0"
    },
    {
      "name": "eth0",
      "sandbox": "/var/run/netns/test-ns_test-pod"
    },
    {
      "name": "host_dummy1"
    },
    {
      "name": "dummy1",
      "sandbox": "/var/run/netns/default_testpod"
    }
  ],
  "ips": [
    { "address": "100.10.10.3/24", "interface": 1 },
    { "address": "fd44::33/64", "interface": 1 },
    { "address": "10.10.10.30/24", "interface": 3 },
    { "address": "fd10::3/64", "interface": 3 }
  ]
}`

		var want map[string]any
		if err := json.Unmarshal([]byte(expected), &want); err != nil {
			t.Fatalf("failed to unmarshal expected: %v", err)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("unexpected CmdAdd output:\n got=%v\nwant=%v", got, want)
		}
	})
}

func TestCmdDel_PrivilegedMode(t *testing.T) {
	p := &Plugin{}

	// Patch doCNI to return a response with non-nil Result that indicates server handled delete
	result := &current.Result{}

	kubeAuth := &KubeAPIAuth{
		Kubeconfig:       config.Kubernetes.Kubeconfig,
		KubeAPIServer:    config.Kubernetes.APIServer,
		KubeAPIToken:     config.Kubernetes.Token,
		KubeAPITokenFile: config.Kubernetes.TokenFile,
	}
	body, _ := json.Marshal(&Response{
		Result:    result,
		PodIFInfo: &PodInterfaceInfo{},
		KubeAuth:  kubeAuth,
	})

	// Mock a doCNI that succeeds for CmdDel
	p.doCNIFunc = func(_ string, _ interface{}) ([]byte, error) {
		return body, nil
	}

	args := &skel.CmdArgs{
		StdinData:   []byte(`{"cniVersion":"1.1.0","name":"mynet","type":"ovn-k8s-cni-overlay"}`),
		ContainerID: "cid",
		Netns:       "/var/run/netns/test",
		IfName:      "eth0",
	}
	err := p.CmdDel(args)
	require.NoError(t, err)
}

func TestCmdDel_UnprivilegedMode(t *testing.T) {
	withCNIEnv(t, func() {
		p := &Plugin{}
		config.OVNKubernetesFeature.EnableMultiNetwork = true
		config.OVNKubernetesFeature.EnableNetworkSegmentation = true

		stub := &podRequestInterfaceOpsStub{}
		podRequestInterfaceOps = stub
		defer func() { podRequestInterfaceOps = &defaultPodRequestInterfaceOps{} }()

		resp := &Response{
			Result: nil,
			PodIFInfo: &PodInterfaceInfo{
				NetName:       "default",
				NADKey:        "foo-ns/default",
				IsDPUHostMode: true,
			},
			PrimaryUDNPodInfo: &PodInterfaceInfo{
				NetName:       "tenantred",
				NADKey:        "foo-ns/meganet",
				IsDPUHostMode: true,
			},
			PrimaryUDNPodReq: &PodRequest{
				PodNamespace: "default",
				PodName:      "testpod",
				IfName:       "dummy1",
			},
			KubeAuth: &KubeAPIAuth{},
		}

		body, _ := json.Marshal(resp)
		p.doCNIFunc = func(_ string, _ interface{}) ([]byte, error) {
			return body, nil
		}

		args := &skel.CmdArgs{
			StdinData:   []byte(`{"cniVersion":"1.1.0","name":"mynet","type":"ovn-k8s-cni-overlay"}`),
			ContainerID: "cid",
			Netns:       "/var/run/netns/test",
			IfName:      "eth0",
		}
		err := p.CmdDel(args)
		require.NoError(t, err)

		if len(stub.unconfiguredInterfaces) <= 0 {
			t.Fatalf("no unconfigured interfaces found")
		}
	})
}

func TestCmdGC(t *testing.T) {
	p := &Plugin{}
	args := &skel.CmdArgs{
		StdinData: []byte(`{"cniVersion":"1.1.0","name":"mynet","type":"ovn-k8s-cni-overlay"}`),
	}

	err := p.CmdGC(args)
	require.NoError(t, err)
}

func withCNIEnv(t *testing.T, fn func()) {
	t.Helper()

	vars := map[string]string{
		"CNI_COMMAND":     "ADD",
		"CNI_CONTAINERID": "dummy-containerid",
		"CNI_NETNS":       "/var/run/netns/testns",
		"CNI_IFNAME":      "eth0",
		"CNI_PATH":        "/opt/cni/bin",
		"CNI_ARGS":        "K8S_POD_NAME=test-pod;K8S_POD_NAMESPACE=test-ns;K8S_POD_UID=12345",
	}

	old := make(map[string]string, len(vars))
	for k := range vars {
		old[k] = os.Getenv(k)
		_ = os.Setenv(k, vars[k])
	}

	fn()

	for k, v := range old {
		if v == "" {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, v)
		}
	}
}

func TestReadDeviceInfoFile(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	valid := `{"type":"pci","version":"1.1.0","pci":{"pci-address":"0000:00:00.1"}}`

	good := filepath.Join(base, "cni", "net1-device.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(good), 0o755))
	require.NoError(t, os.WriteFile(good, []byte(valid), 0o600))
	secret := filepath.Join(outside, "secret")
	require.NoError(t, os.WriteFile(secret, []byte("not-json-secret-content"), 0o600))
	require.NoError(t, os.Symlink(secret, filepath.Join(base, "link")))
	require.NoError(t, os.Symlink(outside, filepath.Join(base, "dirlink")))
	badJSON := filepath.Join(base, "bad.json")
	require.NoError(t, os.WriteFile(badJSON, []byte("not-json-secret-content"), 0o600))

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "file inside base", path: good},
		{name: "path with dot-dot staying inside", path: filepath.Join(base, "cni", "..", "cni", "net1-device.json")},
		{name: "absolute path outside base", path: secret, wantErr: "outside"},
		{name: "dot-dot escaping base", path: filepath.Join(base, "..", filepath.Base(outside), "secret"), wantErr: "outside"},
		{name: "symlink to file outside base", path: filepath.Join(base, "link"), wantErr: "outside"},
		{name: "symlinked directory outside base", path: filepath.Join(base, "dirlink", "secret"), wantErr: "outside"},
		{name: "base directory itself", path: base, wantErr: "outside"},
		{name: "missing file", path: filepath.Join(base, "missing"), wantErr: "cannot resolve"},
		{name: "invalid JSON does not leak content", path: badJSON, wantErr: "not valid device info JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			di, err := readDeviceInfoFile(base, tc.path)
			if tc.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.wantErr)
				require.NotContains(t, err.Error(), "secret-content")
				return
			}
			require.NoError(t, err)
			require.Equal(t, "0000:00:00.1", di.Pci.PciAddress)
		})
	}

	_, err := readDeviceInfoFile(filepath.Join(base, "nonexistent-base"), good)
	require.Error(t, err)
}

func TestCmdAdd_DeviceInfoFile(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	oldBase := deviceInfoBaseDir
	deviceInfoBaseDir = base
	t.Cleanup(func() { deviceInfoBaseDir = oldBase })

	const validJSON = `{"type":"pci","version":"1.1.0","pci":{"pci-address":"0000:00:00.1"}}`
	valid := filepath.Join(base, "net1-device.json")
	require.NoError(t, os.WriteFile(valid, []byte(validJSON), 0o600))
	badJSON := filepath.Join(base, "bad.json")
	require.NoError(t, os.WriteFile(badJSON, []byte("secret-not-json"), 0o600))
	// Valid device info, so only the directory check can reject it.
	outsideFile := filepath.Join(outside, "device.json")
	require.NoError(t, os.WriteFile(outsideFile, []byte(validJSON), 0o600))

	body, err := json.Marshal(&Response{Result: &current.Result{}, PodIFInfo: &PodInterfaceInfo{}, KubeAuth: &KubeAPIAuth{}})
	require.NoError(t, err)

	run := func(t *testing.T, deviceInfoFile string) (req *Request, err error) {
		t.Helper()
		p := &Plugin{}
		p.doCNIFunc = func(_ string, r interface{}) ([]byte, error) {
			req = r.(*Request)
			return body, nil
		}
		// CmdAdd prints the CNI result to stdout.
		oldStdout := os.Stdout
		r, w, pipeErr := os.Pipe()
		require.NoError(t, pipeErr)
		os.Stdout = w
		defer func() {
			os.Stdout = oldStdout
			w.Close()
			r.Close()
		}()
		stdin, marshalErr := json.Marshal(map[string]any{
			"cniVersion":    "1.1.0",
			"name":          "mynet",
			"type":          "ovn-k8s-cni-overlay",
			"runtimeConfig": map[string]string{"CNIDeviceInfoFile": deviceInfoFile},
		})
		require.NoError(t, marshalErr)
		err = p.CmdAdd(&skel.CmdArgs{StdinData: stdin, ContainerID: "cid", Netns: "/var/run/netns/test", IfName: "eth0"})
		return req, err
	}

	t.Run("device info file inside the directory is forwarded in the request", func(t *testing.T) {
		req, err := run(t, valid)
		require.NoError(t, err)
		require.NotNil(t, req)
		require.NotNil(t, req.Pci)
		require.Equal(t, "0000:00:00.1", req.Pci.PciAddress)
	})

	t.Run("device info file outside the directory fails ADD before the server call", func(t *testing.T) {
		req, err := run(t, outsideFile)
		require.ErrorContains(t, err, "is outside")
		require.Nil(t, req, "the CNI server must not be called")
	})

	t.Run("invalid JSON fails ADD without file content in the error", func(t *testing.T) {
		req, err := run(t, badJSON)
		require.ErrorContains(t, err, "not valid device info JSON")
		// A raw json.Unmarshal error would show a character of the file content.
		require.NotContains(t, err.Error(), "invalid character")
		require.Nil(t, req, "the CNI server must not be called")
	})
}
