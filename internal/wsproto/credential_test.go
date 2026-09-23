package wsproto

import "testing"

// TestJobCredentialProtocolFloor pins the SEC-01 v11 capability gate: the job
// credential rides the dispatch frame, and a worker that predates the field would
// ignore it — the job would then run with no credential of its own (it can read
// nothing back), so the hub must be able to tell the two fleets apart by version alone
// and skip the issuance instead (recording job.credential_skipped).
func TestJobCredentialProtocolFloor(t *testing.T) {
	if JobCredentialMinProtocolVersion != 11 {
		t.Fatalf("JobCredentialMinProtocolVersion = %d, want 11", JobCredentialMinProtocolVersion)
	}
	if CurrentProtocolVersion != 11 {
		t.Fatalf("CurrentProtocolVersion = %d, want 11 (the job credential is the only v11 addition)", CurrentProtocolVersion)
	}
	if SupportsJobCredential(JobCredentialMinProtocolVersion - 1) {
		t.Fatalf("a v%d worker must not be sent a job credential", JobCredentialMinProtocolVersion-1)
	}
	if !SupportsJobCredential(11) || !SupportsJobCredential(12) {
		t.Fatal("SupportsJobCredential(11/12) = false, want true")
	}
	// Raising the protocol must never drop a capability an older floor already gave.
	if !SupportsSkills(CurrentProtocolVersion) || !SupportsFileXfer(CurrentProtocolVersion) || !SupportsVerify(CurrentProtocolVersion) {
		t.Fatalf("protocol v%d must still carry the older capabilities", CurrentProtocolVersion)
	}
}
