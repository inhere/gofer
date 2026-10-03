//go:build windows

package proctree

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestJobLimitFlagsAllowExplicitBreakawayAndKillOnClose(t *testing.T) {
	if jobLimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE == 0 {
		t.Fatal("job object must kill descendants when its handle closes")
	}
	if jobLimitFlags&windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK == 0 {
		t.Fatal("job object must allow explicit daemon breakaway")
	}
}
