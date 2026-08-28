package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	conchcrypto "github.com/newo-ether/conch/crypto"
)

func TestListDevicesOmitsAvailableWithoutDroppingConfiguredDevices(t *testing.T) {
	keyPair, err := conchcrypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	server := NewServerWithUnavailable(
		map[string]*Transport{"healthy": {serverPubKey: keyPair.PublicKey}},
		map[string]DeviceConfig{
			"healthy": {URL: "http://healthy", Description: "reachable"},
			"offline": {URL: "http://offline", Description: "sleeping"},
		},
		map[string]string{"offline": "connection refused"},
	)

	result := server.doListDevices()
	if result.IsError || len(result.Content) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	text := result.Content[0].Text
	var rawDevices []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &rawDevices); err != nil {
		t.Fatalf("decode raw devices: %v", err)
	}
	for _, device := range rawDevices {
		if _, exists := device["available"]; exists {
			t.Fatalf("list_devices must omit available: %s", text)
		}
	}

	var devices []struct {
		Name  string `json:"name"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(text), &devices); err != nil {
		t.Fatalf("decode devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices, want 2", len(devices))
	}
	if devices[0].Name != "healthy" {
		t.Fatalf("healthy device missing: %#v", devices)
	}
	if devices[1].Name != "offline" || devices[1].Error != "connection refused" {
		t.Fatalf("offline state missing: %#v", devices)
	}
}

func TestResolveDeviceReportsConfiguredUnavailableReason(t *testing.T) {
	server := NewServerWithUnavailable(
		map[string]*Transport{"healthy": {}},
		map[string]DeviceConfig{"healthy": {}, "offline": {}},
		map[string]string{"offline": "timeout"},
	)

	transport, message := server.resolveDevice(map[string]interface{}{"device": "offline"})
	if transport != nil {
		t.Fatal("unexpected transport for unavailable device")
	}
	if !strings.Contains(message, "unavailable") || !strings.Contains(message, "timeout") {
		t.Fatalf("unexpected message: %q", message)
	}
}
