package commands

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
)

func TestDoctorBundleRedactsEveryMember(t *testing.T) {
	const token = "sk-live-9876543210abcdefghij"
	const password = "pass-long-secret-12345"
	const webhook = "bot-secret-path-1234"
	const prompt = "private conversation do not include"
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, root)
	t.Setenv(config.EnvRunMode, config.RunModeServer)
	t.Setenv(config.EnvConfigPath, "")
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv("GOFER_SERVER_TOKEN", "")
	cfg := filepath.Join(root, "config.yaml")
	body := "server:\n  token: " + token + "\n  addr: 127.0.0.1:1\n  notification:\n    webhooks:\n      - url: https://user:" + password + "@example.invalid/bot/" + webhook + "?key=" + token + "\nroles:\n  helper:\n    system_prompt: " + prompt + "\n"
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	log := config.RuntimeFilePath("run", "serve.log")
	if err := os.MkdirAll(filepath.Dir(log), 0o755); err != nil {
		t.Fatal(err)
	}
	logs := "normal startup\nauthorization=Bearer " + token + "\nwebhook https://user:" + password + "@example.invalid/bot/" + webhook + "?key=" + token + "\nmessage: " + prompt + "\ntoken=sk-live-log-only-secret\n"
	if err := os.WriteFile(log, []byte(logs), 0o600); err != nil {
		t.Fatal(err)
	}
	old := config.InputCfgFile
	config.InputCfgFile = cfg
	t.Cleanup(func() { config.InputCfgFile = old })
	out := filepath.Join(root, "diagnostics.zip")
	files, manifest := collectDiagnostics(buildinfo.Info{Version: "test"}, 4)
	if len(files) == 0 || manifest.Mode != config.RunModeServer {
		t.Fatalf("unexpected bundle: %v %+v", files, manifest)
	}
	var gotLog string
	for _, f := range files {
		if f.Name == "logs/server.log" {
			gotLog = string(f.data)
		}
	}
	if !strings.Contains(gotLog, "[payload omitted]") {
		t.Fatalf("log must omit message bodies: %q", gotLog)
	}
	if err := writeDiagnosticZip(out, files, []byte(`{"format":1}`)); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	for _, f := range z.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, readErr := io.ReadAll(r)
		r.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, forbidden := range []string{token, password, webhook, prompt, "sk-live-log-only-secret"} {
			if bytes.Contains(b, []byte(forbidden)) {
				t.Errorf("%s contains forbidden value %q", f.Name, forbidden)
			}
		}
		if f.Name == "config.json" && !bytes.Contains(b, []byte(config.TokenMask(token))) {
			t.Error("config should retain only the token mask")
		}
	}
}

func TestDoctorWorkerModeAndClientWorkerList(t *testing.T) {
	const workerToken = "sk-worker-real-123456789123456789"
	root := t.TempDir()
	t.Setenv(config.EnvConfigDir, root)
	t.Setenv(config.EnvConfigPath, "")
	t.Setenv(config.EnvRunMode, config.RunModeWorker)
	workerConfig := filepath.Join(root, "worker.yaml")
	if err := os.WriteFile(workerConfig, []byte("worker_id: node-one\nserver_link:\n  token: "+workerToken+"\n  urls: [\"wss://user:secret@host.example/connect?key="+workerToken+"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldOpts := doctorOpts
	oldConfig := config.InputCfgFile
	doctorOpts.workerConfig = workerConfig
	config.InputCfgFile = ""
	t.Cleanup(func() { doctorOpts = oldOpts; config.InputCfgFile = oldConfig })
	files, _ := collectDiagnostics(buildinfo.Info{}, 5)
	found := false
	for _, f := range files {
		if f.Name == "worker-config.json" {
			found = true
			if bytes.Contains(f.data, []byte(workerToken)) || bytes.Contains(f.data, []byte("secret")) {
				t.Fatalf("worker config leaked a credential: %s", f.data)
			}
		}
	}
	if !found {
		t.Fatal("worker config missing")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/meta":
			_ = json.NewEncoder(w).Encode(map[string]any{"workers": []map[string]any{{"id": "node-one", "connected": true}}})
		case "/v1/agents":
			_ = json.NewEncoder(w).Encode(map[string]any{"agents": []map[string]any{{"key": "exec", "type": "exec", "available": true}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvRunMode, config.RunModeClient)
	jobConnOpts.server, jobConnOpts.token = srv.URL, ""
	t.Cleanup(func() { jobConnOpts.server, jobConnOpts.token = "", "" })
	files, _ = collectDiagnostics(buildinfo.Info{}, 3)
	for _, f := range files {
		if f.Name == "workers.json" && !bytes.Contains(f.data, []byte(`"connected": true`)) {
			t.Errorf("client worker status missing: %s", f.data)
		}
	}
}

func TestDoctorPrintDoesNotWriteZip(t *testing.T) {
	root := t.TempDir()
	t.Setenv(config.EnvRunMode, config.RunModeClient)
	t.Setenv(config.EnvConfigDir, root)
	t.Setenv("GOFER_SERVER_ADDR", "")
	t.Setenv(config.EnvConfigPath, "")
	old := doctorOpts
	defer func() { doctorOpts = old }()
	cmd := bindCmd(NewDoctorCmd(buildinfo.Info{Version: "test"}))
	doctorOpts.print = true
	doctorOpts.output = filepath.Join(root, "should-not-exist.zip")
	if err := runDoctor(cmd, buildinfo.Info{Version: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(doctorOpts.output); !os.IsNotExist(err) {
		t.Fatalf("--print wrote a zip: %v", err)
	}
}
