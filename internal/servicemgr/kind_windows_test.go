//go:build windows

package servicemgr

import (
	"strings"
	"testing"
)

// TestWorkerTaskXMLRunsWorkerSupervisor: a worker registration's scheduled task
// starts the copied supervisor in worker mode, keeps the logon trigger and stays
// at the lowest privilege level.
func TestWorkerTaskXMLRunsWorkerSupervisor(t *testing.T) {
	m, spec := workerSpecFixture(t)
	xmlText, err := buildTaskXML(spec, m.SupervisorPath(), m.SpecPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"InteractiveToken", "LeastPrivilege", "LogonTrigger", "serve supervise --spec"} {
		if !strings.Contains(xmlText, want) {
			t.Errorf("worker task XML missing %q: %s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, "HighestAvailable") {
		t.Fatalf("worker task may not be elevated: %s", xmlText)
	}
	if err := verifyOwnedTask(xmlText, spec, m.SupervisorPath(), m.SpecPath()); err != nil {
		t.Fatal(err)
	}
}
