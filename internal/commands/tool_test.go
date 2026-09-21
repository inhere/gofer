package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
)

// tool_test.go exercises `gofer tool cp` / `tool xfer` against httptest stubs, so
// nothing here needs a real server, a config file or the network. The cp tests
// drive runCpTransfer — the same function the CLI callback calls — with a client
// pointed at the stub, so parsing, progress, polling and the verify-then-rename
// sequence are the real ones.

func TestToolCpParsesRemoteSpec(t *testing.T) {
	specs := []struct {
		name    string
		in      string
		remote  bool
		want    remoteSpec
		wantErr string
	}{
		{name: "plain relative path is local", in: "./firmware.bin"},
		{name: "windows drive letter is local", in: `C:\work\firmware.bin`},
		{name: "colon inside a local path is local", in: "logs/2026-09-21T10:00:00.log"},
		{name: "empty operand", in: "", wantErr: "empty path"},
		{name: "worker target", in: "w-1:proj/tmp/in/a.bin", remote: true,
			want: remoteSpec{runner: "w-1", project: "proj", path: "tmp/in/a.bin"}},
		{name: "server alias is the local runner", in: "server:demo/tmp/x.tar", remote: true,
			want: remoteSpec{runner: "local", project: "demo", path: "tmp/x.tar"}},
		{name: "local alias is the local runner", in: "local:demo/tmp/x.tar", remote: true,
			want: remoteSpec{runner: "local", project: "demo", path: "tmp/x.tar"}},
		{name: "windows backslashes in a remote path", in: `w-1:proj\tmp\in\a.bin`, remote: true,
			want: remoteSpec{runner: "w-1", project: "proj", path: "tmp/in/a.bin"}},
		{name: "colon after the first segment belongs to the path", in: "w-1:proj/logs/10:00.log", remote: true,
			want: remoteSpec{runner: "w-1", project: "proj", path: "logs/10:00.log"}},
		{name: "missing project segment", in: "w-1:proj", wantErr: "the project is the first path segment"},
		{name: "empty project segment", in: "w-1:/proj/a.bin", wantErr: "the project is the first path segment"},
		{name: "empty remote path", in: "w-1:proj/", wantErr: "the remote path is empty"},
	}
	for _, tc := range specs {
		t.Run(tc.name, func(t *testing.T) {
			got, remote, err := parseRemoteSpec(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseRemoteSpec(%q) = %+v, remote=%v; want error containing %q", tc.in, got, remote, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseRemoteSpec(%q) error = %q; want it to contain %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRemoteSpec(%q): %v", tc.in, err)
			}
			if remote != tc.remote {
				t.Fatalf("parseRemoteSpec(%q) remote = %v, want %v", tc.in, remote, tc.remote)
			}
			if got != tc.want {
				t.Fatalf("parseRemoteSpec(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}

	pairs := []struct {
		name      string
		src, dst  string
		wantErr   string
		wantPush  bool
		wantLocal string
		wantSpec  remoteSpec
	}{
		{name: "push", src: "./firmware.bin", dst: "w-1:proj/tmp/in/firmware.bin",
			wantPush: true, wantLocal: "./firmware.bin",
			wantSpec: remoteSpec{runner: "w-1", project: "proj", path: "tmp/in/firmware.bin"}},
		{name: "pull", src: "server:demo/tmp/out/report.csv", dst: `out\report.csv`,
			wantPush: false, wantLocal: `out\report.csv`,
			wantSpec: remoteSpec{runner: "local", project: "demo", path: "tmp/out/report.csv"}},
		{name: "local to local", src: "./a.bin", dst: "./b.bin", wantErr: "neither SRC nor DST is remote"},
		{name: "both remote", src: "w-1:proj/a.bin", dst: "w-2:proj/b.bin", wantErr: "both SRC and DST are remote"},
	}
	for _, tc := range pairs {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := parseCpPair(tc.src, tc.dst)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("parseCpPair(%q, %q) = %+v; want error containing %q", tc.src, tc.dst, plan, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseCpPair(%q, %q) error = %q; want it to contain %q", tc.src, tc.dst, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCpPair(%q, %q): %v", tc.src, tc.dst, err)
			}
			if plan.push != tc.wantPush || plan.local != tc.wantLocal || plan.remote != tc.wantSpec {
				t.Fatalf("parseCpPair(%q, %q) = %+v; want push=%v local=%q remote=%+v",
					tc.src, tc.dst, plan, tc.wantPush, tc.wantLocal, tc.wantSpec)
			}
		})
	}
}

func TestToolCpPushFlow(t *testing.T) {
	payload := bytes.Repeat([]byte("gofer-xfer-"), 300)
	wantSum := sha256.Sum256(payload)
	wantDigest := hex.EncodeToString(wantSum[:])
	dir := t.TempDir()
	local := filepath.Join(dir, "firmware.bin")
	if err := os.WriteFile(local, payload, 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}

	var (
		meta      map[string]any
		gotFile   []byte
		fieldSeq  []string
		polls     int32
		createdID = "x1"
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/xfer":
			mr, err := r.MultipartReader()
			if err != nil {
				t.Errorf("multipart reader: %v", err)
				http.Error(w, "bad multipart", http.StatusBadRequest)
				return
			}
			// The meta field carries everything the server validates the target and
			// the size cap with, so it must arrive BEFORE the payload.
			first, err := mr.NextPart()
			if err != nil {
				t.Errorf("first part: %v", err)
				return
			}
			fieldSeq = append(fieldSeq, first.FormName())
			raw, _ := io.ReadAll(first)
			if err := json.Unmarshal(raw, &meta); err != nil {
				t.Errorf("meta is not JSON: %v (%s)", err, raw)
			}
			second, err := mr.NextPart()
			if err != nil {
				t.Errorf("file part: %v", err)
				return
			}
			fieldSeq = append(fieldSeq, second.FormName())
			if gotFile, err = io.ReadAll(second); err != nil {
				t.Errorf("read file part: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": createdID, "state": "staged"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/xfer/"+createdID:
			states := []string{"staged", "dispatched", "done"}
			n := int(atomic.AddInt32(&polls, 1)) - 1
			if n >= len(states) {
				n = len(states) - 1
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": createdID, "op": "put", "runner": "w-1", "project": "proj",
				"path": "tmp/a.bin", "size": len(payload), "sha256": wantDigest,
				"state": states[n], "created_at": 1758000000,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cli := client.New(srv.URL, "test-token")
	cmd := bindCmd(findSub(t, NewToolCmd(), "cp"))
	plan, err := parseCpPair(local, "w-1:proj/tmp/a.bin")
	if err != nil {
		t.Fatalf("parseCpPair: %v", err)
	}

	var out string
	out = captureOutput(t, func() {
		if err := runCpTransfer(context.Background(), cmd, cli, plan, true); err != nil {
			t.Fatalf("push: %v", err)
		}
	})

	if len(fieldSeq) != 2 || fieldSeq[0] != "meta" || fieldSeq[1] != "file" {
		t.Errorf("multipart fields = %v; want [meta file]", fieldSeq)
	}
	want := map[string]any{
		"op": "put", "runner": "w-1", "project": "proj", "path": "tmp/a.bin",
		"sha256": wantDigest, "size": float64(len(payload)), "force": true,
	}
	for k, v := range want {
		if meta[k] != v {
			t.Errorf("meta[%s] = %v (%T); want %v (%T)", k, meta[k], meta[k], v, v)
		}
	}
	if !bytes.Equal(gotFile, payload) {
		t.Errorf("file part = %d bytes, want the %d local bytes", len(gotFile), len(payload))
	}
	if got := atomic.LoadInt32(&polls); got < 3 {
		t.Errorf("status polls = %d; want the flow to poll through staged/dispatched/done", got)
	}
	if !strings.Contains(out, "100%") {
		t.Errorf("no progress output; got:\n%s", out)
	}
	if !strings.Contains(out, "dispatched") || !strings.Contains(out, "done:") {
		t.Errorf("missing state/done lines; got:\n%s", out)
	}
}

// xferPullStub answers the three calls a pull makes: the JSON create, the status
// poll (staged -> done) and the content download, which reports shaHeader as
// X-Gofer-Sha256. polls counts the status calls.
func xferPullStub(t *testing.T, payload []byte, shaHeader string) (*httptest.Server, *int32) {
	t.Helper()
	var polls int32
	sum := sha256.Sum256(payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/xfer":
			var meta map[string]any
			if err := json.NewDecoder(r.Body).Decode(&meta); err != nil {
				t.Errorf("pull meta: %v", err)
				return
			}
			want := map[string]any{"op": "get", "runner": "w-1", "project": "proj", "path": "tmp/out/report.csv"}
			for k, v := range want {
				if meta[k] != v {
					t.Errorf("pull meta[%s] = %v; want %v", k, meta[k], v)
				}
			}
			if _, ok := meta["sha256"]; ok {
				t.Errorf("a pull must not declare a digest, got %v", meta["sha256"])
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "x9", "state": "staged"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/xfer/x9":
			state := "staged"
			if atomic.AddInt32(&polls, 1) > 1 {
				state = "done"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "x9", "op": "get", "runner": "w-1", "project": "proj",
				"path": "tmp/out/report.csv", "size": len(payload), "sha256": hex.EncodeToString(sum[:]),
				"state": state, "created_at": 1758000000,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/xfer/x9/content":
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Gofer-Sha256", shaHeader)
			w.Header().Set("X-Gofer-Size", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, &polls
}

func TestToolCpPullFlow(t *testing.T) {
	payload := []byte("collected,report\nrow,1\n")
	sum := sha256.Sum256(payload)

	t.Run("downloads into the destination and drops the part file", func(t *testing.T) {
		srv, polls := xferPullStub(t, payload, hex.EncodeToString(sum[:]))
		defer srv.Close()
		dest := filepath.Join(t.TempDir(), "report.csv")

		cli := client.New(srv.URL, "test-token")
		cmd := bindCmd(findSub(t, NewToolCmd(), "cp"))
		plan, err := parseCpPair("w-1:proj/tmp/out/report.csv", dest)
		if err != nil {
			t.Fatalf("parseCpPair: %v", err)
		}

		out := captureOutput(t, func() {
			if err := runCpTransfer(context.Background(), cmd, cli, plan, false); err != nil {
				t.Fatalf("pull: %v", err)
			}
		})

		got, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("destination missing: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Errorf("destination = %q; want %q", got, payload)
		}
		if _, err := os.Stat(dest + cpPartSuffix); !os.IsNotExist(err) {
			t.Errorf("the %s temp file was left behind (err=%v)", cpPartSuffix, err)
		}
		if got := atomic.LoadInt32(polls); got < 2 {
			t.Errorf("status polls = %d; want the flow to poll until done", got)
		}
		if !strings.Contains(out, "done:") {
			t.Errorf("missing the done line; got:\n%s", out)
		}
	})

	t.Run("a bad digest leaves neither the destination nor the part file", func(t *testing.T) {
		srv, _ := xferPullStub(t, payload, strings.Repeat("0", 64))
		defer srv.Close()
		dest := filepath.Join(t.TempDir(), "report.csv")

		cli := client.New(srv.URL, "test-token")
		cmd := bindCmd(findSub(t, NewToolCmd(), "cp"))
		plan, err := parseCpPair("w-1:proj/tmp/out/report.csv", dest)
		if err != nil {
			t.Fatalf("parseCpPair: %v", err)
		}

		var runErr error
		_ = captureOutput(t, func() {
			runErr = runCpTransfer(context.Background(), cmd, cli, plan, false)
		})
		if runErr == nil {
			t.Fatal("a sha256 mismatch must fail the pull (non-zero exit)")
		}
		if !strings.Contains(runErr.Error(), "sha256 mismatch") {
			t.Errorf("error = %q; want it to name the mismatch", runErr)
		}
		assertToolExit(t, runErr)
		if _, err := os.Stat(dest); !os.IsNotExist(err) {
			t.Errorf("the destination exists after a failed pull (err=%v)", err)
		}
		if _, err := os.Stat(dest + cpPartSuffix); !os.IsNotExist(err) {
			t.Errorf("the %s temp file survived a failed pull (err=%v)", cpPartSuffix, err)
		}
	})
}

func TestToolXferListShowRm(t *testing.T) {
	const id = "xf-1"
	created := int64(1758000000)
	record := map[string]any{
		"id": id, "op": "put", "runner": "local", "project": "proj", "path": "tmp/a.bin",
		"size": 12, "sha256": "abc123", "state": "done", "caller_id": "cli",
		"created_at": created, "finished_at": created + 5, "expires_at": created + 86400,
	}
	var (
		deletes  int32
		lsRunner string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/xfer":
			lsRunner = r.URL.Query().Get("runner")
			if got := r.URL.Query().Get("state"); got != "done" {
				t.Errorf("state filter = %q; want done", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"xfers": []any{record}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/xfer/"+id:
			_ = json.NewEncoder(w).Encode(record)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/xfer/"+id:
			atomic.AddInt32(&deletes, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cli := client.New(srv.URL, "test-token")
	xferCmd := findSub(t, NewToolCmd(), "xfer")

	// `ls` prints the row — and canonicalises the runner filter, because `server`
	// is the CLI's alias for the wire's `local` (the spelling the server stores).
	out := captureOutput(t, func() {
		cmd := bindCmd(findSub(t, xferCmd, "ls"))
		if err := runXferLs(cmd, cli, "done", "server"); err != nil {
			t.Fatalf("xfer ls: %v", err)
		}
	})
	if lsRunner != "local" {
		t.Errorf("runner filter = %q; want the canonical %q", lsRunner, "local")
	}
	for _, want := range []string{id, "done", "put", "local", "proj:tmp/a.bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("xfer ls output is missing %q; got:\n%s", want, out)
		}
	}

	out = captureOutput(t, func() {
		cmd := bindCmd(findSub(t, xferCmd, "show"))
		if err := runXferShow(cmd, cli, id); err != nil {
			t.Fatalf("xfer show: %v", err)
		}
	})
	for _, want := range []string{id, "state:       done", "runner:      local", "sha256:      abc123", "path:        tmp/a.bin"} {
		if !strings.Contains(out, want) {
			t.Errorf("xfer show output is missing %q; got:\n%s", want, out)
		}
	}

	out = captureOutput(t, func() {
		cmd := bindCmd(findSub(t, xferCmd, "rm"))
		if err := runXferRemove(cmd, cli, id); err != nil {
			t.Fatalf("xfer rm: %v", err)
		}
	})
	if !strings.Contains(out, "removed transfer "+id) {
		t.Errorf("xfer rm output = %q", out)
	}
	if got := atomic.LoadInt32(&deletes); got != 1 {
		t.Errorf("DELETE calls = %d; want exactly 1", got)
	}
}

// assertToolExit pins the failure contract: a failed `tool` command must carry the
// coded exit 1 (gcli derives the process exit code from errorx.ErrorCoder), so a
// script can gate on it instead of reading stderr.
func assertToolExit(t *testing.T, err error) {
	t.Helper()
	var coder errorx.ErrorCoder
	if !errors.As(err, &coder) {
		t.Fatalf("error %T is not coded: gcli would exit 2, not %d", err, toolExitErr)
	}
	if coder.Code() != toolExitErr {
		t.Fatalf("coded exit = %d; want %d", coder.Code(), toolExitErr)
	}
}

// TestToolCpReportsTheReason pins the other half of the failure contract: the text
// the user sees is the SERVER's own reason (a failed transfer's `error` field, or
// the {error,detail} of a rejected request), never a generic "transfer failed".
func TestToolCpReportsTheReason(t *testing.T) {
	t.Run("a failed transfer echoes the record's error verbatim", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/xfer" {
				_, _ = io.Copy(io.Discard, r.Body)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "x5", "state": "staged"})
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/v1/xfer/x5" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "x5", "op": "get", "runner": "w-1", "project": "proj",
					"path": "tmp/out/report.csv", "state": "failed", "error": "worker offline",
				})
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()

		cli := client.New(srv.URL, "test-token")
		cmd := bindCmd(findSub(t, NewToolCmd(), "cp"))
		plan, err := parseCpPair("w-1:proj/tmp/out/report.csv", filepath.Join(t.TempDir(), "report.csv"))
		if err != nil {
			t.Fatalf("parseCpPair: %v", err)
		}
		var runErr error
		_ = captureOutput(t, func() {
			runErr = runCpTransfer(context.Background(), cmd, cli, plan, false)
		})
		if runErr == nil {
			t.Fatal("a failed transfer must fail the command")
		}
		if runErr.Error() != "worker offline" {
			t.Errorf("error = %q; want the record's own text verbatim", runErr)
		}
		assertToolExit(t, runErr)
	})

	t.Run("a rejected create surfaces the server's error and detail", func(t *testing.T) {
		dir := t.TempDir()
		local := filepath.Join(dir, "a.bin")
		if err := os.WriteFile(local, []byte("payload"), 0o644); err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "exists", "detail": "the target already exists; pass --force to replace it",
			})
		}))
		defer srv.Close()

		cli := client.New(srv.URL, "test-token")
		cmd := bindCmd(findSub(t, NewToolCmd(), "cp"))
		plan, err := parseCpPair(local, "w-1:proj/tmp/a.bin")
		if err != nil {
			t.Fatalf("parseCpPair: %v", err)
		}
		var runErr error
		_ = captureOutput(t, func() {
			runErr = runCpTransfer(context.Background(), cmd, cli, plan, false)
		})
		if runErr == nil {
			t.Fatal("a rejected push must fail the command")
		}
		for _, want := range []string{"exists", "pass --force"} {
			if !strings.Contains(runErr.Error(), want) {
				t.Errorf("error = %q; want it to contain the server's %q", runErr, want)
			}
		}
		assertToolExit(t, runErr)
	})
}
