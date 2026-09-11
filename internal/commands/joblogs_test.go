package commands

import (
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
)

func TestJobLogsResolveOpts(t *testing.T) {
	cases := []struct {
		name       string
		stream     string
		stderr     bool
		lines      int
		head, tail bool
		want       client.LogOpts
		wantErr    bool
	}{
		{"default keeps byte tail", "", false, 0, false, false, client.LogOpts{Stream: "stdout"}, false},
		{"explicit stream", "stderr", false, 0, false, false, client.LogOpts{Stream: "stderr"}, false},
		{"--stderr shortcut", "", true, 0, false, false, client.LogOpts{Stream: "stderr"}, false},
		{"--stderr with --stream stderr", "stderr", true, 0, false, false, client.LogOpts{Stream: "stderr"}, false},
		{"--stderr conflicts with --stream stdout", "stdout", true, 0, false, false, client.LogOpts{}, true},
		{"-n alone is a tail", "", false, 50, false, false, client.LogOpts{Stream: "stdout", Lines: 50}, false},
		{"--head defaults to 20", "", false, 0, true, false, client.LogOpts{Stream: "stdout", Lines: 20, Head: true}, false},
		{"--tail defaults to 20", "", false, 0, false, true, client.LogOpts{Stream: "stdout", Lines: 20}, false},
		{"--head -n on stderr", "", true, 10, true, false, client.LogOpts{Stream: "stderr", Lines: 10, Head: true}, false},
		{"--head and --tail", "", false, 0, true, true, client.LogOpts{}, true},
		{"negative -n", "", false, -1, false, false, client.LogOpts{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLogOpts(tc.stream, tc.stderr, tc.lines, tc.head, tc.tail)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestWaitTerminalDeadline(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	if !waitDeadline(now, 0).IsZero() || !waitDeadline(now, -5).IsZero() {
		t.Fatal("unknown timeout must not cap the wait")
	}
	if got, want := waitDeadline(now, 1800), now.Add(1800*time.Second+waitGrace); !got.Equal(want) {
		t.Fatalf("deadline=%v want %v", got, want)
	}
}

func TestJobLogsShouldPrintStderr(t *testing.T) {
	cases := []struct {
		res  job.JobResult
		want bool
	}{
		{job.JobResult{Status: job.StatusDone, ExitCode: 0}, false},
		{job.JobResult{Status: job.StatusDone, ExitCode: 1}, true},
		{job.JobResult{Status: job.StatusFailed}, true},
		{job.JobResult{Status: job.StatusTimeout}, true},
		{job.JobResult{Status: job.StatusCancelled}, true},
	}
	for _, tc := range cases {
		if got := shouldPrintJobStderr(tc.res); got != tc.want {
			t.Fatalf("shouldPrintJobStderr(%s, exit %d) = %v, want %v", tc.res.Status, tc.res.ExitCode, got, tc.want)
		}
	}
}
