package worker

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

func previewClient(t *testing.T) *Client {
	t.Helper()
	return New(Config{WorkerID: "w1", URLs: []string{"ws://127.0.0.1:1/v1/workers/connect"}, Token: "t"}, &stubJobs{})
}

func TestPreviewRefusalMatrix(t *testing.T) {
	cl := previewClient(t)
	cl.inflightCreate("job-run")
	cl.inflightCreate("job-done")
	cl.inflightSetStatus("job-done", job.StatusDone)
	cl.inflightCreate("job-cached")
	cl.inflightCacheResult("job-cached", wsproto.Result{Status: job.StatusFailed})

	open := func(mod func(*wsproto.PreviewOpen)) wsproto.PreviewOpen {
		o := wsproto.PreviewOpen{PreviewID: "p", JobID: "job-run", Port: 3000, RelayNonce: "n-" + time.Now().String(), ConnID: "t-1", InstanceID: cl.instanceID}
		mod(&o)
		return o
	}
	cases := []struct {
		name string
		mod  func(*wsproto.PreviewOpen)
		want string
	}{
		{"ok", func(*wsproto.PreviewOpen) {}, ""},
		{"another instance", func(o *wsproto.PreviewOpen) { o.InstanceID = "other" }, wsproto.PreviewCodeInstance},
		{"port zero", func(o *wsproto.PreviewOpen) { o.Port = 0 }, wsproto.PreviewCodeBadPort},
		{"port too large", func(o *wsproto.PreviewOpen) { o.Port = 65536 }, wsproto.PreviewCodeBadPort},
		{"unknown job", func(o *wsproto.PreviewOpen) { o.JobID = "nope" }, wsproto.PreviewCodeJobNotRunning},
		{"terminal job", func(o *wsproto.PreviewOpen) { o.JobID = "job-done" }, wsproto.PreviewCodeJobNotRunning},
		{"job with cached result", func(o *wsproto.PreviewOpen) { o.JobID = "job-cached" }, wsproto.PreviewCodeJobNotRunning},
		{"empty nonce", func(o *wsproto.PreviewOpen) { o.RelayNonce = "" }, wsproto.PreviewCodeNonceReplay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, _ := cl.previewRefusal(open(tc.mod)); got != tc.want {
				t.Fatalf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewNonceReplayRefused(t *testing.T) {
	cl := previewClient(t)
	cl.inflightCreate("job-run")
	o := wsproto.PreviewOpen{PreviewID: "p", JobID: "job-run", Port: 3000, RelayNonce: "once", ConnID: "t-1", InstanceID: cl.instanceID}
	if code, _ := cl.previewRefusal(o); code != "" {
		t.Fatalf("first open refused: %q", code)
	}
	if code, _ := cl.previewRefusal(o); code != wsproto.PreviewCodeNonceReplay {
		t.Fatalf("replay code = %q, want %q", code, wsproto.PreviewCodeNonceReplay)
	}
}

func TestPreviewRelaysCloseByJobAndAll(t *testing.T) {
	var p previewRelays
	pipe := func() (net.Conn, net.Conn) { return net.Pipe() }
	a1, a2 := pipe()
	b1, b2 := pipe()
	defer a2.Close()
	defer b2.Close()
	if !p.add("job-a", a1) || !p.add("job-b", b1) {
		t.Fatal("add refused")
	}
	p.closeJob("job-a")
	if _, err := a1.Write([]byte("x")); err == nil {
		t.Fatal("job-a connection must be closed")
	}
	_ = b2.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := b2.Read(make([]byte, 1)); err == io.EOF {
		t.Fatal("job-b connection must stay open")
	}
	p.closeAll()
	if _, err := b1.Write([]byte("x")); err == nil {
		t.Fatal("closeAll must close job-b")
	}
	p.remove("job-a", a1)
	p.remove("job-b", b1)
	if p.active != 0 {
		t.Fatalf("active = %d, want 0", p.active)
	}
}

func TestPreviewRelaysCapConnections(t *testing.T) {
	var p previewRelays
	var keep []net.Conn
	t.Cleanup(func() {
		for _, c := range keep {
			_ = c.Close()
		}
	})
	for i := 0; i < previewMaxConns; i++ {
		a, b := net.Pipe()
		keep = append(keep, a, b)
		if !p.add("job", a) {
			t.Fatalf("add %d refused below the cap", i)
		}
	}
	a, b := net.Pipe()
	keep = append(keep, a, b)
	if p.add("job", a) {
		t.Fatal("add above the cap must be refused")
	}
}

// A finished job (terminal Result recorded or dropped) cuts the job's preview connections.
func TestJobEndClosesPreviewConnections(t *testing.T) {
	cl := previewClient(t)
	cl.inflightCreate("job-1")
	c1, c2 := net.Pipe()
	defer c2.Close()
	if !cl.previews.add("job-1", c1) {
		t.Fatal("add refused")
	}
	cl.inflightCacheResult("job-1", wsproto.Result{Status: job.StatusDone})
	if _, err := c1.Write([]byte("x")); err == nil {
		t.Fatal("a cached terminal result must close the job's previews")
	}
	d1, d2 := net.Pipe()
	defer d2.Close()
	cl.inflightCreate("job-2")
	cl.previews.add("job-2", d1)
	cl.inflightDrop("job-2")
	if _, err := d1.Write([]byte("x")); err == nil {
		t.Fatal("dropping the job must close its previews")
	}
}
