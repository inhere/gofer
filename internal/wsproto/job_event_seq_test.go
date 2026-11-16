package wsproto

import (
	"encoding/json"
	"testing"
)

func TestSupportsJobEventSeq(t *testing.T) {
	for proto, want := range map[int]bool{0: false, 29: false, 30: true, 31: true} {
		if got := SupportsJobEventSeq(proto); got != want {
			t.Errorf("SupportsJobEventSeq(%d) = %v, want %v", proto, got, want)
		}
	}
	if CurrentProtocolVersion < JobEventSeqMinProtocolVersion {
		t.Fatalf("CurrentProtocolVersion = %d is below JobEventSeqMinProtocolVersion", CurrentProtocolVersion)
	}
	if MinProtocolVersion != 2 {
		t.Fatalf("MinProtocolVersion = %d: event replay must not evict old workers", MinProtocolVersion)
	}
}

// TestJobEventSeqWireShape pins that the v30 fields are additive: zero values stay off
// the wire, so an older peer sees exactly the frames it always did.
func TestJobEventSeqWireShape(t *testing.T) {
	b, _ := json.Marshal(JobEvent{JobID: "j", Type: "t", TS: 1})
	if got := string(b); got != `{"job_id":"j","type":"t","ts":1}` {
		t.Fatalf("unnumbered event = %s", got)
	}
	b, _ = json.Marshal(ResumeJob{JobID: "j", StdoutOff: 3})
	if got := string(b); got != `{"job_id":"j","stdout_off":3}` {
		t.Fatalf("old-shape resume = %s", got)
	}
	var back ResumeJob
	if err := json.Unmarshal([]byte(`{"job_id":"j","event_seq":7,"acp_off":9}`), &back); err != nil || back.EventSeq != 7 || back.AcpOff != 9 {
		t.Fatalf("v30 resume decode = %+v, %v", back, err)
	}
	var ev JobEvent
	if err := json.Unmarshal([]byte(`{"job_id":"j","type":"t","ts":1,"seq":4}`), &ev); err != nil || ev.Seq != 4 {
		t.Fatalf("v30 event decode = %+v, %v", ev, err)
	}
}
