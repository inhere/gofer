package workerprovision

import (
	"context"
	"io"
	"strings"
	"testing"
)

type fakeExec struct {
	calls  [][]string
	inputs []string
	probe  string
	failAt int
}

func (f *fakeExec) Run(_ context.Context, args []string, stdin io.Reader) (string, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.inputs = append(f.inputs, string(b))
	} else {
		f.inputs = append(f.inputs, "")
	}
	if f.failAt == len(f.calls) {
		return "", io.ErrUnexpectedEOF
	}
	if len(f.calls) == 1 {
		return f.probe, nil
	}
	return "", nil
}
func (f *fakeExec) Copy(_ context.Context, _, _ string) error {
	f.calls = append(f.calls, []string{"copy"})
	if f.failAt == len(f.calls) {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func TestParsePlatform(t *testing.T) {
	for _, tc := range []struct{ in, wantOS, wantArch string }{
		{"Linux x86_64", "linux", "amd64"},
		{"Microsoft Windows 11 AMD64", "windows", "amd64"},
		{"Darwin arm64", "darwin", "arm64"},
	} {
		gotOS, gotArch, err := ParsePlatform(tc.in)
		if err != nil || gotOS != tc.wantOS || gotArch != tc.wantArch {
			t.Errorf("ParsePlatform(%q)=(%q,%q,%v)", tc.in, gotOS, gotArch, err)
		}
	}
}

func TestRunStopsOnFailureAndKeepsTokenOutOfArgs(t *testing.T) {
	f := &fakeExec{probe: "Linux x86_64", failAt: 4}
	_, err := Run(context.Background(), Options{ID: "w1", Server: "https://hub", Token: "secret-token", Binary: "gofer", Executor: f})
	if err == nil || !strings.Contains(err.Error(), "initialize") {
		t.Fatalf("err=%v", err)
	}
	for _, call := range f.calls {
		for _, arg := range call {
			if strings.Contains(arg, "secret-token") {
				t.Fatalf("secret leaked in args: %v", call)
			}
		}
	}
	if len(f.calls) != 4 || f.inputs[2] != "secret-token\n" {
		t.Fatalf("calls=%v inputs=%q", f.calls, f.inputs)
	}
}

func TestRunStartsWorkerAfterCopy(t *testing.T) {
	f := &fakeExec{probe: "Linux x86_64"}
	got, err := Run(context.Background(), Options{ID: "w1", Server: "https://hub", Token: "secret-token", Binary: "gofer-linux-amd64", Executor: f})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.OS != "linux" || got.Arch != "amd64" || len(f.calls) != 5 {
		t.Fatalf("result=%+v calls=%v", got, f.calls)
	}
	if strings.Join(f.calls[3], " ") != "/tmp/gofer-worker-w1 worker init --server https://hub --id w1 --token-stdin --yes" {
		t.Fatalf("init args=%v", f.calls[3])
	}
	if strings.Join(f.calls[4], " ") != "/tmp/gofer-worker-w1 worker --daemon" {
		t.Fatalf("start args=%v", f.calls[4])
	}
}
func TestRunDryRunDoesNotCallExecutor(t *testing.T) {
	f := &fakeExec{}
	var out strings.Builder
	_, err := Run(context.Background(), Options{ID: "w1", Server: "https://hub", Token: "secret-token", DryRun: true, Executor: f, Output: &out})
	if err != nil || len(f.calls) != 0 {
		t.Fatalf("err=%v calls=%v", err, f.calls)
	}
	if strings.Contains(out.String(), "secret-token") {
		t.Fatal("dry-run leaked token")
	}
}
