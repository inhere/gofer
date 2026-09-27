package worker

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

func TestUncommittedWorkerFrameRoundTrip(t *testing.T) {
	want := job.JobResult{UncommittedFiles: []string{"a.go", "tools/gofer/b.go"}, UncommittedCount: 2}
	frame, send := outcomeFrame("remote-job", want)
	if !send {
		t.Fatal("uncommitted-only outcome must be sent")
	}
	data, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	var decoded wsproto.Outcome
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.UncommittedCount != 2 || !reflect.DeepEqual(decoded.UncommittedFiles, want.UncommittedFiles) {
		t.Fatalf("round trip = %+v", decoded)
	}
}
