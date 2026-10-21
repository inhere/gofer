package wsproto

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestImageSayProtocol(t *testing.T) {
	if ImageSayMinProtocolVersion != 25 || CurrentProtocolVersion != 26 {
		t.Fatalf("image say must ride on v25 without a further bump: min=%d current=%d", ImageSayMinProtocolVersion, CurrentProtocolVersion)
	}
	if SupportsImageSay(ImageSayMinProtocolVersion - 1) {
		t.Fatal("a v24 worker must not be sent image blocks")
	}
	if !SupportsImageSay(CurrentProtocolVersion) {
		t.Fatal("the current protocol must support image say")
	}

	cmd := SessionCommand{JobID: "j1", CmdID: "c1", Action: "say", Prompt: "see", Images: []SessionImage{
		{Mime: "image/png", Data: []byte{1, 2, 3}}, {Mime: "image/jpeg", Data: []byte{4}},
	}}
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var round SessionCommand
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(round, cmd) {
		t.Fatalf("round trip = %+v, want %+v", round, cmd)
	}
	// A text-only say keeps its old wire shape (no images key).
	plain, _ := json.Marshal(SessionCommand{JobID: "j1", CmdID: "c1", Action: "say", Prompt: "x"})
	if string(plain) != `{"job_id":"j1","cmd_id":"c1","action":"say","prompt":"x"}` {
		t.Fatalf("text-only say changed shape: %s", plain)
	}
}
