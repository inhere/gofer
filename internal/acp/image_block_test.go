package acp

import (
	"encoding/json"
	"testing"
)

// A text block must marshal byte-for-byte as it did before the image fields existed.
func TestContentBlockTextWireUnchanged(t *testing.T) {
	raw, err := json.Marshal(TextBlock("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"text","text":"hello"}`; string(raw) != want {
		t.Fatalf("text block = %s, want %s", raw, want)
	}
	raw, err = json.Marshal(PromptParams{SessionID: "s1", Prompt: []ContentBlock{TextBlock("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}`; string(raw) != want {
		t.Fatalf("prompt params = %s, want %s", raw, want)
	}
}

func TestContentBlockImageRoundTrip(t *testing.T) {
	in := ImageBlock("image/png", []byte{0x89, 'P', 'N', 'G'})
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"image","data":"iVBORw==","mimeType":"image/png"}`; string(raw) != want {
		t.Fatalf("image block = %s, want %s", raw, want)
	}
	var out ContentBlock
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	var withURI ContentBlock
	if err := json.Unmarshal([]byte(`{"type":"image","data":"AA==","mimeType":"image/gif","uri":"file:///a.gif"}`), &withURI); err != nil || withURI.URI != "file:///a.gif" {
		t.Fatalf("uri not parsed: %+v err=%v", withURI, err)
	}
}

func TestPromptImageCapabilityParsing(t *testing.T) {
	cases := []struct {
		name string
		caps string
		want bool
	}{
		{"true", `{"promptCapabilities":{"image":true,"audio":false}}`, true},
		{"false", `{"promptCapabilities":{"image":false}}`, false},
		{"absent image", `{"promptCapabilities":{"audio":true}}`, false},
		{"absent block", `{"loadSession":true}`, false},
		{"malformed", `{"promptCapabilities":"nope"}`, false},
	}
	for _, c := range cases {
		var caps AgentCapabilities
		if err := json.Unmarshal([]byte(c.caps), &caps); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := caps.PromptImage(); got != c.want {
			t.Errorf("%s: PromptImage() = %v, want %v", c.name, got, c.want)
		}
	}
}
