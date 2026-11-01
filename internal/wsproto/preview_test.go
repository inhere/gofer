package wsproto

import (
	"encoding/json"
	"testing"
)

func TestSupportsPreview(t *testing.T) {
	for proto, want := range map[int]bool{2: false, 27: false, 28: false, 29: true, 30: true} {
		if got := SupportsPreview(proto); got != want {
			t.Errorf("SupportsPreview(%d) = %v, want %v", proto, got, want)
		}
	}
}

func TestPreviewDoesNotRaiseRegistrationFloor(t *testing.T) {
	if MinProtocolVersion != 2 {
		t.Fatalf("MinProtocolVersion = %d: shipping preview must not evict old workers", MinProtocolVersion)
	}
	if CurrentProtocolVersion != PreviewMinProtocolVersion {
		t.Fatalf("CurrentProtocolVersion = %d, PreviewMinProtocolVersion = %d", CurrentProtocolVersion, PreviewMinProtocolVersion)
	}
	if SupportsPreview(CurrentProtocolVersion - 1) {
		t.Fatal("a v28 worker must not be treated as preview capable")
	}
}

func TestPreviewOpenEncodeDecodeAs(t *testing.T) {
	want := PreviewOpen{PreviewID: "p-1", JobID: "j-1", Port: 3000, RelayNonce: "n-1", ConnID: "t-1", InstanceID: "i-1"}
	b, err := EncodeFrame(TypePreviewOpen, want.JobID, want)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Payload map[string]json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"preview_id", "job_id", "port", "relay_nonce", "conn_id", "instance_id"} {
		if _, ok := wire.Payload[k]; !ok {
			t.Errorf("payload missing %q", k)
		}
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != TypePreviewOpen || env.JobID != "j-1" {
		t.Fatalf("envelope = %+v", env)
	}
	got, err := As[PreviewOpen](env)
	if err != nil || got != want {
		t.Fatalf("round trip = %+v, %v; want %+v", got, err, want)
	}
}

func TestPreviewOpenIgnoresUnknownFields(t *testing.T) {
	var got PreviewOpen
	if err := json.Unmarshal([]byte(`{"preview_id":"p","job_id":"j","port":1,"relay_nonce":"n","conn_id":"c","instance_id":"i","future":true}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.Port != 1 || got.ConnID != "c" {
		t.Fatalf("got %+v", got)
	}
}

func TestPreviewHelloJSON(t *testing.T) {
	b, _ := json.Marshal(PreviewHello{PreviewID: "p", JobID: "j", ConnID: "c", RelayNonce: "n"})
	if string(b) != `{"preview_id":"p","job_id":"j","conn_id":"c","relay_nonce":"n"}` {
		t.Fatalf("hello = %s", b)
	}
}
