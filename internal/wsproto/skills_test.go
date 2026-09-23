package wsproto

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSkillsProtocolFloor pins the v10 capability gate: the skills mount rides the
// SAME upload channel as XFER-01, but on a destination OUTSIDE the job's cwd — a
// worker that predates the base field would place a skill in the working tree, so
// the hub must be able to tell the two fleets apart by version alone.
func TestSkillsProtocolFloor(t *testing.T) {
	if CurrentProtocolVersion != 10 {
		t.Fatalf("CurrentProtocolVersion = %d, want 10", CurrentProtocolVersion)
	}
	if SkillsMinProtocolVersion != 10 {
		t.Fatalf("SkillsMinProtocolVersion = %d, want 10", SkillsMinProtocolVersion)
	}
	if SupportsSkills(SkillsMinProtocolVersion - 1) {
		t.Fatalf("a v%d worker must not be sent a skills mount", SkillsMinProtocolVersion-1)
	}
	if !SupportsSkills(10) || !SupportsSkills(11) {
		t.Fatal("SupportsSkills(10/11) = false, want true")
	}
	// Raising the protocol must never drop a capability an older floor already gave.
	if !SupportsFileXfer(CurrentProtocolVersion) || !SupportsVerify(CurrentProtocolVersion) {
		t.Fatalf("protocol v%d must still carry the older capabilities", CurrentProtocolVersion)
	}
}

// TestXferUploadBaseRoundTrip: `base` is additive — absent means the job's cwd,
// exactly the pre-v10 reading, so an old frame decodes unchanged; present, it names
// the directory the destination is relative to.
func TestXferUploadBaseRoundTrip(t *testing.T) {
	b, err := json.Marshal(XferUpload{XferID: "xf-1", Dest: "skills/a/SKILL.md", Base: "result_dir"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"base":"result_dir"`) {
		t.Fatalf("marshalled = %s, want the base field", b)
	}
	var back XferUpload
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Base != "result_dir" || back.Dest != "skills/a/SKILL.md" {
		t.Fatalf("round trip = %+v", back)
	}

	old, err := json.Marshal(XferUpload{XferID: "xf-2", Dest: "in.txt"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(old), "base") {
		t.Fatalf("marshalled = %s, want base omitted when empty (an old worker reads it as cwd)", old)
	}
}

// TestDispatchCarriesResolvedSkills: the dispatch names the skills the hub resolved,
// so a worker mounts exactly those and never re-derives the binding from its own
// config (which may name a different library).
func TestDispatchCarriesResolvedSkills(t *testing.T) {
	b, err := json.Marshal(Dispatch{JobID: "j1", Runner: "local", Skills: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Dispatch
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Skills) != 2 || back.Skills[0] != "a" || back.Skills[1] != "b" {
		t.Fatalf("skills = %v, want [a b]", back.Skills)
	}

	empty, err := json.Marshal(Dispatch{JobID: "j2", Runner: "local"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(empty), "skills") {
		t.Fatalf("marshalled = %s, want skills omitted for a job with none", empty)
	}
}
