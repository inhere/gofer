package wsproto

import (
	"encoding/json"
	"testing"
)

func TestTranscriptTailProtocolV17(t *testing.T) {
	if CurrentProtocolVersion < TranscriptTailMinProtocolVersion {
		t.Fatalf("Current %d must implement the transcript tail floor %d", CurrentProtocolVersion, TranscriptTailMinProtocolVersion)
	}
	if !SupportsTranscriptTail(TranscriptTailMinProtocolVersion) || SupportsTranscriptTail(TranscriptTailMinProtocolVersion-1) {
		t.Fatal("transcript tail must start exactly at its floor")
	}
	raw, err := json.Marshal(TranscriptTailResult{ReqID: "r", OK: true, Data: []byte("{\"a\":1}\n"), Size: 8, Truncated: true})
	if err != nil {
		t.Fatal(err)
	}
	var back TranscriptTailResult
	if err := json.Unmarshal(raw, &back); err != nil || string(back.Data) != "{\"a\":1}\n" || !back.Truncated || back.ReqID != "r" {
		t.Fatalf("round trip = %+v err=%v", back, err)
	}
}
