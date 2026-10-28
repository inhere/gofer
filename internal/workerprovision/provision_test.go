package workerprovision

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
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
	if len(args) >= 2 && args[0] == "sh" && args[1] == "-c" {
		return "/home/test", nil
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

func TestRemoteInstallPathAndCommands(t *testing.T) {
	for _, tc := range []struct {
		name, goos, home, want string
	}{
		{"linux", "linux", "/home/alice", "/home/alice/.local/bin/gofer"},
		{"wsl", "linux", "/root", "/root/.local/bin/gofer"},
		{"windows", "windows", `C:\Users\alice\AppData\Local`, `C:/Users/alice/AppData/Local/gofer/bin/gofer.exe`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &pathExec{out: tc.home}
			got, err := resolveRemotePath(context.Background(), f, tc.goos)
			if err != nil || got != tc.want {
				t.Fatalf("path=%q err=%v want=%q", got, err, tc.want)
			}
			cmd := installDirCommand(tc.goos, got[:strings.LastIndex(got, "/")])
			joined := strings.Join(cmd, " ")
			if tc.goos != "windows" && !strings.Contains(joined, "local/bin") {
				t.Fatalf("directory command=%v", cmd)
			}
			if tc.goos == "windows" && !strings.Contains(joined, "New-Item") {
				t.Fatalf("directory command=%v", cmd)
			}
		})
	}
}

type pathExec struct{ out string }

func (f *pathExec) Run(context.Context, []string, io.Reader) (string, error) { return f.out, nil }
func (f *pathExec) Copy(context.Context, string, string) error               { return nil }

func TestWSLCommandArgsDoNotDuplicatePrefix(t *testing.T) {
	got := wslCommandArgs("Ubuntu", []string{"sh", "-c", `printf %s "$HOME"`})
	want := []string{"-d", "Ubuntu", "--", "sh", "-c", `printf %s "$HOME"`}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("got=%v want=%v", got, want)
	}
}

func TestRemoteSSHArgsPreserveShellArguments(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local sh simulation is unavailable on Windows")
	}
	args := remoteSSHArgs([]string{"sh", "-c", `printf '%s|%s' "$HOME" "a b"`}, false)
	cmd := exec.Command("sh", "-c", args[0])
	cmd.Env = append(os.Environ(), "HOME=/home/test")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "/home/test|a b" {
		t.Fatalf("output=%q", out)
	}
}

func TestRemoteSSHWindowsEncodedCommand(t *testing.T) {
	got := remoteSSHArgs([]string{"powershell", "-NoProfile", "-Command", `Write-Output 'a b'`}, true)
	if len(got) != 5 || got[0] != "powershell" || got[3] != "-EncodedCommand" {
		t.Fatalf("args=%v", got)
	}
	data, err := base64.StdEncoding.DecodeString(got[4])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	script := string(utf16.Decode(units))
	if !strings.Contains(script, "a b") || !strings.Contains(script, "Write-Output") {
		t.Fatalf("script=%q", script)
	}
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
	f := &fakeExec{probe: "Linux x86_64", failAt: 6}
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
	if len(f.calls) != 6 || f.inputs[4] != "secret-token\n" {
		t.Fatalf("calls=%v inputs=%q", f.calls, f.inputs)
	}
}

func TestRunStartsWorkerAfterCopy(t *testing.T) {
	f := &fakeExec{probe: "Linux x86_64"}
	got, err := Run(context.Background(), Options{ID: "w1", Server: "https://hub", Token: "secret-token", Binary: "gofer-linux-amd64", Executor: f})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.OS != "linux" || got.Arch != "amd64" || len(f.calls) != 7 {
		t.Fatalf("result=%+v calls=%v", got, f.calls)
	}
	if strings.Join(f.calls[5], " ") != "/home/test/.local/bin/gofer worker init --server https://hub --id w1 --token-stdin --yes" {
		t.Fatalf("init args=%v", f.calls[3])
	}
	if strings.Join(f.calls[6], " ") != "/home/test/.local/bin/gofer worker --daemon" {
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
