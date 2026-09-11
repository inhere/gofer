package wsproto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSupportsTunnel(t *testing.T) {
	tests := []struct {
		name  string
		proto int
		want  bool
	}{
		{name: "v2", proto: 2, want: false},
		{name: "v4", proto: 4, want: false},
		{name: "v5", proto: 5, want: true},
		{name: "v6", proto: 6, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SupportsTunnel(tt.proto); got != tt.want {
				t.Fatalf("SupportsTunnel(%d) = %v, want %v", tt.proto, got, tt.want)
			}
		})
	}
}

func TestTunnelOpenEncodeDecodeAs(t *testing.T) {
	want := TunnelOpen{TunnelID: "t-0123456789ab", Target: "127.0.0.1:8080", RelayNonce: "0123456789abcdef", Network: "tcp"}
	b, err := EncodeFrame(TypeTunnelOpen, "", want)
	if err != nil {
		t.Fatalf("EncodeFrame: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatalf("wire JSON: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(wire["payload"], &payload); err != nil {
		t.Fatalf("payload JSON: %v", err)
	}
	for _, key := range []string{"tunnel_id", "target", "relay_nonce", "network"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("payload missing JSON field %q", key)
		}
	}
	if _, ok := payload["TunnelID"]; ok {
		t.Error("payload unexpectedly contains Go field name TunnelID")
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Type != TypeTunnelOpen {
		t.Fatalf("envelope type = %q, want %q", env.Type, TypeTunnelOpen)
	}
	got, err := As[TunnelOpen](env)
	if err != nil {
		t.Fatalf("As[TunnelOpen]: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}
