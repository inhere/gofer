package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/wsproto"
)

// missingBin is an executable name no test machine has on PATH.
const missingBin = "gofer-test-no-such-tool-bin"

// TestWorkerCapsReportsDeclaredTools (CFG-16): worker.yaml's tools reach the job
// service's config (workerConfigToConfig) and the capability report built from it,
// so what the hub shows is what the worker's jobs are told.
func TestWorkerCapsReportsDeclaredTools(t *testing.T) {
	wc := &config.WorkerConfig{WorkerID: "w1", Tools: []config.Tool{
		{Name: "ffmpeg", Desc: "transcoder"},
		{Name: "sim", Bin: "/opt/sim"},
	}}
	t.Setenv(config.EnvConfigDir, t.TempDir())
	if cfg := workerConfigToConfig(wc); len(cfg.Tools) != 2 || cfg.Tools[1].Bin != "/opt/sim" {
		t.Fatalf("workerConfigToConfig tools = %+v", cfg.Tools)
	}
	caps := capsFor(t, wc, &fakeDetector{res: map[string]agent.DetectResult{}})
	want := []wsproto.ToolBrief{{Name: "ffmpeg", Desc: "transcoder"}, {Name: "sim", Bin: "/opt/sim"}}
	if len(caps.Tools) != 2 || caps.Tools[0] != want[0] || caps.Tools[1] != want[1] {
		t.Fatalf("caps.Tools = %+v, want %+v", caps.Tools, want)
	}
	if none := capsFor(t, &config.WorkerConfig{WorkerID: "w1"}, &fakeDetector{}); none.Tools != nil {
		t.Fatalf("a worker without tools must report nil, got %+v", none.Tools)
	}
}

// TestLoadWorkerConfigRejectsBadTools: a malformed declaration fails the load
// (so startup / reload / validate all refuse it) and names the offending entry.
func TestLoadWorkerConfigRejectsBadTools(t *testing.T) {
	path := writeRawConfig(t, "worker_id: w1\ntools:\n  - name: ok\n  - name: \"bad name\"\n")
	_, err := loadWorkerConfig(path)
	if err == nil || !strings.Contains(err.Error(), `tools[1]: name "bad name"`) {
		t.Fatalf("loadWorkerConfig error = %v, want the tools[1] rejection", err)
	}
}

// TestConfigValidateToolsMissingBinWarns: a declared tool whose executable this
// machine cannot find is a WARNING in both validate targets — never a failure.
func TestConfigValidateToolsMissingBinWarns(t *testing.T) {
	t.Run("worker", func(t *testing.T) {
		host := t.TempDir()
		cfgPath := writeRawConfig(t, "worker_id: w1\n"+
			"server_link:\n  urls: [ws://127.0.0.1:LIVE-PORT/v1/workers/connect]\n  token: tok\n"+
			"projects:\n  w1:\n    host_path: "+host+"\n    allowed_runners: [local]\n"+
			"tools:\n  - name: nope\n    bin: "+missingBin+"\n")
		c := bindCmd(NewConfigCmd().Subs[0])
		c.Arg("target").WithValue("worker")
		config.InputCfgFile = cfgPath
		t.Cleanup(func() { config.InputCfgFile = "" })
		var err error
		out := captureOutput(t, func() { err = runConfigValidate(c, nil) })
		if err != nil {
			t.Fatalf("a missing tool bin must not fail validation: %v\n%s", err, out)
		}
		if !strings.Contains(out, "[WARN] worker/tools") || !strings.Contains(out, "nope ("+missingBin+")") {
			t.Fatalf("output = %q, want a tools WARN naming the missing bin", out)
		}
	})
	t.Run("server", func(t *testing.T) {
		t.Setenv(config.EnvWorkspace, filepath.Join(t.TempDir(), "missing-workspace"))
		cfgPath := writeRawConfig(t, "tools:\n  - name: nope\n    bin: "+missingBin+"\n")
		c := bindCmd(NewConfigCmd().Subs[0])
		config.InputCfgFile = cfgPath
		t.Cleanup(func() { config.InputCfgFile = "" })
		var err error
		out := captureOutput(t, func() { err = runConfigValidate(c, nil) })
		if err != nil {
			t.Fatalf("a missing tool bin must not fail validation: %v\n%s", err, out)
		}
		if !strings.Contains(out, "[WARN] tools") || !strings.Contains(out, missingBin) {
			t.Fatalf("output = %q, want a tools WARN naming the missing bin", out)
		}
	})
}

// TestDoctorToolsRow: no declaration passes, a resolvable one passes with its
// count, a missing executable is a WARN naming it (never a FAIL).
func TestDoctorToolsRow(t *testing.T) {
	if row := doctorToolsRow(&config.WorkerConfig{}); row.Status != doctorPass || row.Check != "tools" {
		t.Fatalf("no tools row = %+v, want PASS", row)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	ok := doctorToolsRow(&config.WorkerConfig{Tools: []config.Tool{{Name: "self", Bin: exe}}})
	if ok.Status != doctorPass || !strings.Contains(ok.Detail, "1") {
		t.Fatalf("resolvable tools row = %+v, want PASS with the count", ok)
	}
	warn := doctorToolsRow(&config.WorkerConfig{Tools: []config.Tool{{Name: "self", Bin: exe}, {Name: "nope", Bin: missingBin}}})
	if warn.Status != doctorWarn || !strings.Contains(warn.Detail, "nope ("+missingBin+")") {
		t.Fatalf("missing-bin tools row = %+v, want WARN naming it", warn)
	}
}

// TestWorkerShowPrintsTools: `worker show` lists the declared tool names on one
// line, and -v adds each tool's executable and description.
func TestWorkerShowPrintsTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"worker_id": "w1", "connected": true,
			"worker": map[string]any{
				"protocol_version": 21,
				"tools": []map[string]any{
					{"name": "ffmpeg", "desc": "transcoder"},
					{"name": "sim", "bin": "/opt/sim"},
				},
			},
		})
	}))
	defer srv.Close()
	clientNode(t, srv.URL)
	show := func(verbose bool) string {
		return captureOutput(t, func() {
			c := bindCmd(NewWorkerShowCmd())
			workerShowOpts.verbose = verbose // after bindCmd, which binds the default
			c.Arg("id").Set("w1")
			if err := runWorkerShow(c, nil); err != nil {
				t.Fatalf("worker show: %v", err)
			}
		})
	}
	if out := show(false); !strings.Contains(out, "tools: ffmpeg, sim\n") {
		t.Fatalf("output = %q, want the tools line", out)
	}
	out := show(true)
	if !strings.Contains(out, "  - ffmpeg: transcoder\n") || !strings.Contains(out, "  - sim (/opt/sim)\n") {
		t.Fatalf("verbose output = %q, want one line per tool with bin/desc", out)
	}
}
