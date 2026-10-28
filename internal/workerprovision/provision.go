package workerprovision

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/inhere/gofer/internal/procattr"
)

// Executor is the small seam around the system ssh/scp (or wsl.exe) commands.
// Implementations must never put secrets in argv; stdin is used for credentials.
type Executor interface {
	Run(ctx context.Context, args []string, stdin io.Reader) (string, error)
	Copy(ctx context.Context, source, destination string) error
}

// SystemExecutor uses only the operating system's ssh/scp or wsl.exe.
type SystemExecutor struct {
	SSH      string
	WSL      string
	TargetOS string
}

func (e SystemExecutor) Run(ctx context.Context, args []string, stdin io.Reader) (string, error) {
	cmdArgs := append([]string(nil), args...)
	var name string
	if e.WSL != "" {
		name = "wsl.exe"
		cmdArgs = append([]string{"-d", e.WSL, "--"}, cmdArgs...)
	} else {
		name = "ssh"
		cmdArgs = append([]string{"-o", "BatchMode=yes", e.SSH}, remoteSSHArgs(args, e.TargetOS == "windows")...)
	}
	cmd := exec.CommandContext(ctx, name, cmdArgs...)
	procattr.Background(cmd)
	cmd.Stdin = stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", name, strings.Join(cmdArgs, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func (e SystemExecutor) Copy(ctx context.Context, source, destination string) error {
	if e.WSL != "" {
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		_, err = e.Run(ctx, []string{"sh", "-c", "cat > " + shellQuote(destination)}, strings.NewReader(string(data)))
		return err
	}
	cmd := exec.CommandContext(ctx, "scp", "-o", "BatchMode=yes", source, e.SSH+":"+destination)
	procattr.Background(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("scp %s: %w: %s", e.SSH, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func remoteSSHArgs(args []string, windows bool) []string {
	if !windows {
		return []string{strings.Join(mapQuote(args), " ")}
	}
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, "'"+strings.ReplaceAll(arg, "'", "''")+"'")
	}
	script := "& " + strings.Join(parts, " ")
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], u)
	}
	return []string{"powershell", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(buf)}
}

func mapQuote(args []string) []string {
	out := make([]string, len(args))
	for i, arg := range args {
		out[i] = shellQuote(arg)
	}
	return out
}

// Options describes one remote worker installation.
type Options struct {
	ID         string
	Server     string
	Token      string
	Binary     string
	BinaryURL  string
	RemotePath string
	DryRun     bool
	Timeout    time.Duration
	Executor   Executor
	Output     io.Writer
	// ResolveURL / ResolveBinary let callers reuse their configured release mirror.
	// They are called only after the remote platform has been probed.
	ResolveURL    func(context.Context, string, string) (string, error)
	ResolveBinary func(context.Context, string, string) (string, error)
}

type Result struct {
	OS, Arch, RemotePath string
	UsedDownload         bool
}

// Run probes, installs, initializes and starts a worker. It stops at the first
// failed step. The token is sent to worker init over stdin and is never an argv value.
func Run(ctx context.Context, o Options) (Result, error) {
	if o.ID == "" || o.Server == "" {
		return Result{}, errors.New("worker id and server are required")
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	if o.Executor == nil {
		return Result{}, errors.New("worker provision executor is nil")
	}
	if o.Output == nil {
		o.Output = io.Discard
	}
	if o.DryRun {
		fmt.Fprintf(o.Output, "probe: uname -sm (fallback: PowerShell)\ninstall: resolve user-writable absolute path at execution time, create its directory, then remote release download or scp fallback\npath: <resolved remote home>/.local/bin/gofer (Windows: <resolved LOCALAPPDATA>/gofer/bin/gofer.exe)\ninit: worker init --server %s --id %s --token-stdin --yes (token via stdin)\nstart: worker --daemon\n", o.Server, o.ID)
		return Result{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	probe, err := o.Executor.Run(ctx, []string{"uname", "-sm"}, nil)
	if err != nil {
		if se, ok := o.Executor.(SystemExecutor); ok {
			se.TargetOS = "windows"
			o.Executor = se
		}
		probe, err = o.Executor.Run(ctx, []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.OS + ' ' + $env:PROCESSOR_ARCHITECTURE"}, nil)
		if err != nil {
			return Result{}, stepError("probe remote OS/architecture", err)
		}
	}
	goos, goarch, err := ParsePlatform(probe)
	if err != nil {
		return Result{}, stepError("parse remote OS/architecture", err)
	}
	if se, ok := o.Executor.(SystemExecutor); ok {
		se.TargetOS = goos
		o.Executor = se
	}
	path := o.RemotePath
	if path == "" {
		path, err = resolveRemotePath(ctx, o.Executor, goos)
		if err != nil {
			return Result{}, stepError("resolve remote install path", err)
		}
	}
	if _, err := o.Executor.Run(ctx, installDirCommand(goos, filepath.Dir(path)), nil); err != nil {
		return Result{}, stepError("create remote install directory", err)
	}
	result := Result{OS: goos, Arch: goarch, RemotePath: path}
	if o.BinaryURL == "" && o.ResolveURL != nil {
		if resolved, resolveErr := o.ResolveURL(ctx, goos, goarch); resolveErr == nil {
			o.BinaryURL = resolved
		} else {
			fmt.Fprintf(o.Output, "resolve remote release URL failed; falling back to upload: %v\n", resolveErr)
		}
	}
	if o.BinaryURL != "" {
		args := []string{"curl", "-fL", "--tlsv1.2", "--create-dirs", "-o", path, o.BinaryURL}
		if _, err := o.Executor.Run(ctx, args, nil); err == nil {
			result.UsedDownload = true
		} else {
			fmt.Fprintf(o.Output, "remote download failed; falling back to scp: %v\n", err)
		}
	}
	if !result.UsedDownload {
		binary := o.Binary
		if binary == "" && o.ResolveBinary != nil {
			binary, err = o.ResolveBinary(ctx, goos, goarch)
		}
		if binary == "" && err == nil {
			binary, err = localBinary("", goos, goarch)
		}
		if err != nil {
			return Result{}, stepError("resolve matching local gofer binary", err)
		}
		if err := o.Executor.Copy(ctx, binary, path); err != nil {
			return Result{}, stepError("copy gofer binary", err)
		}
	}
	if goos != "windows" {
		if _, err := o.Executor.Run(ctx, []string{"chmod", "+x", path}, nil); err != nil {
			return Result{}, stepError("make remote gofer executable", err)
		}
	}
	initArgs := []string{path, "worker", "init", "--server", o.Server, "--id", o.ID, "--token-stdin", "--yes"}
	startArgs := []string{path, "worker", "--daemon"}
	if goos == "windows" {
		initArgs = powershellExec(path, "worker", "init", "--server", o.Server, "--id", o.ID, "--token-stdin", "--yes")
		startArgs = powershellExec(path, "worker", "--daemon")
	}
	if _, err := o.Executor.Run(ctx, initArgs, strings.NewReader(o.Token+"\n")); err != nil {
		return Result{}, stepError("initialize remote worker", err)
	}
	if _, err := o.Executor.Run(ctx, startArgs, nil); err != nil {
		return Result{}, stepError("start remote worker", err)
	}
	return result, nil
}

func resolveRemotePath(ctx context.Context, e Executor, goos string) (string, error) {
	if se, ok := e.(SystemExecutor); ok && se.WSL != "" {
		out, err := e.Run(ctx, []string{"sh", "-c", `printf %s "$HOME"`}, nil)
		if err != nil {
			return "", err
		}
		base := strings.TrimSpace(out)
		if base == "" || !strings.HasPrefix(base, "/") {
			return "", fmt.Errorf("remote HOME is not absolute: %q", base)
		}
		return path.Join(base, ".local", "bin", "gofer"), nil
	}
	if goos == "windows" {
		out, err := e.Run(ctx, []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "[Environment]::GetFolderPath('LocalApplicationData')"}, nil)
		if err != nil {
			return "", err
		}
		base := strings.TrimSpace(out)
		if base == "" {
			return "", errors.New("remote LOCALAPPDATA is empty")
		}
		base = strings.ReplaceAll(base, "\\", "/")
		return path.Join(base, "gofer", "bin", "gofer.exe"), nil
	}
	out, err := e.Run(ctx, []string{"sh", "-c", `printf %s "$HOME"`}, nil)
	if err != nil {
		return "", err
	}
	base := strings.TrimSpace(out)
	if base == "" || (!filepath.IsAbs(base) && !strings.HasPrefix(base, "/")) {
		return "", fmt.Errorf("remote HOME is not absolute: %q", base)
	}
	return path.Join(base, ".local", "bin", "gofer"), nil
}

func installDirCommand(goos, dir string) []string {
	if goos == "windows" {
		return []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "New-Item -ItemType Directory -Force -Path '" + strings.ReplaceAll(dir, "'", "''") + "' | Out-Null"}
	}
	return []string{"mkdir", "-p", dir}
}

func powershellExec(path string, args ...string) []string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, "& '"+strings.ReplaceAll(path, "'", "''")+"'")
	for _, arg := range args {
		parts = append(parts, "'"+strings.ReplaceAll(arg, "'", "''")+"'")
	}
	return []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", strings.Join(parts, " ")}
}

func localBinary(explicit, goos, goarch string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return os.Executable()
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "gofer-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	candidate := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("target is %s/%s but no matching package at %s; build or download it and pass --binary", goos, goarch, candidate)
	}
	return candidate, nil
}

func stepError(step string, err error) error { return fmt.Errorf("%s: %w", step, err) }

func safeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func ParsePlatform(s string) (string, string, error) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) < 2 {
		return "", "", fmt.Errorf("unexpected probe output %q", s)
	}
	lower := strings.ToLower(s)
	osName, arch := strings.ToLower(fields[0]), strings.ToLower(fields[1])
	if strings.Contains(lower, "windows") || strings.Contains(lower, "microsoft") {
		osName = "windows"
		arch = strings.ToLower(fields[len(fields)-1])
	}
	switch arch {
	case "x86_64", "amd64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	case "i386", "i686":
		arch = "386"
	}
	return osName, arch, nil
}

// DryRunCommand returns a redacted human-readable command for diagnostics.
func DryRunCommand(target string, args []string) string {
	return "ssh -o BatchMode=yes " + target + " " + strings.Join(args, " ") + " <token via stdin>"
}
