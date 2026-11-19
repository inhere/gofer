package job

import "testing"

func TestStampSessionResume(t *testing.T) {
	var req JobRequest
	if StampSessionResume(&req, false, "s1", "j1") || req.SessionID != "" {
		t.Fatal("an agent that cannot load a session must start fresh")
	}
	if StampSessionResume(&req, true, "", "j1") || StampSessionResume(&req, true, "s1", "") {
		t.Fatal("no session id or source job must start fresh")
	}
	if !StampSessionResume(&req, true, " s1 ", "j1") || req.SessionID != "s1" || req.ResumedFrom != "j1" {
		t.Fatalf("stamp = %+v", req)
	}
}
