// Package servicemgr owns the local metadata and coordination primitives used
// by the native Windows and Linux service backends.
package servicemgr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	SpecSchema  = 1
	DefaultName = "gofer-serve"
)

type Backend string

const (
	BackendWindowsTask   Backend = "windows-task"
	BackendSystemdSystem Backend = "systemd-system"
	BackendSystemdUser   Backend = "systemd-user"
)

var serviceName = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// Spec is the durable, non-secret description of one managed serve instance.
// RuntimeDir is recorded separately because an explicit -c puts serve.pid and
// serve.log beside that config file, which can differ from ConfigDir.
type Spec struct {
	SchemaVersion     int          `json:"schema_version"`
	Backend           Backend      `json:"backend"`
	Name              string       `json:"name"`
	Owner             string       `json:"owner"`
	Exe               string       `json:"exe"`
	WorkDir           string       `json:"work_dir"`
	ConfigFile        string       `json:"config_file"`
	ConfigDir         string       `json:"config_dir"`
	RuntimeDir        string       `json:"runtime_dir"`
	RunAs             string       `json:"run_as,omitempty"`
	Elevated          bool         `json:"elevated,omitempty"`
	Serve             ServeOptions `json:"serve"`
	RegisteredVersion string       `json:"registered_version,omitempty"`
}

// ServeOptions is an allowlist of non-secret arguments. In particular there is
// no token or arbitrary argument list in the durable spec.
type ServeOptions struct {
	Addr            string `json:"addr,omitempty"`
	NoWeb           bool   `json:"no_web,omitempty"`
	WebDir          string `json:"web_dir,omitempty"`
	AllowEmptyToken bool   `json:"allow_empty_token,omitempty"`
}

func validName(name string) bool { return serviceName.MatchString(name) }

func (s Spec) Validate() error {
	if s.SchemaVersion != SpecSchema {
		return fmt.Errorf("unsupported service spec schema %d", s.SchemaVersion)
	}
	switch s.Backend {
	case BackendWindowsTask, BackendSystemdSystem, BackendSystemdUser:
	default:
		return fmt.Errorf("unsupported service backend %q", s.Backend)
	}
	if !validName(s.Name) {
		return fmt.Errorf("invalid service name %q", s.Name)
	}
	if s.Owner == "" {
		return errors.New("service owner is required")
	}
	if s.Backend == BackendSystemdSystem && strings.TrimSpace(s.RunAs) == "" {
		return errors.New("systemd system service requires run_as")
	}
	if s.Backend != BackendSystemdSystem && s.RunAs != "" {
		return errors.New("run_as is only valid for systemd system services")
	}
	if s.Backend != BackendWindowsTask && s.Elevated {
		return errors.New("elevated is only valid for Windows tasks")
	}
	if strings.ContainsAny(s.RunAs, " \t\r\n%") || strings.ContainsAny(s.Serve.Addr, "\r\n") {
		return errors.New("invalid service run_as or listen address")
	}
	if s.Serve.NoWeb && s.Serve.WebDir != "" {
		return errors.New("no_web and web_dir cannot both be set")
	}
	if s.Serve.WebDir != "" && (!filepath.IsAbs(s.Serve.WebDir) || filepath.Clean(s.Serve.WebDir) != s.Serve.WebDir) {
		return fmt.Errorf("web_dir must be a clean absolute path: %q", s.Serve.WebDir)
	}
	for _, path := range []struct{ field, value string }{
		{"exe", s.Exe}, {"work_dir", s.WorkDir},
		{"config_file", s.ConfigFile}, {"config_dir", s.ConfigDir},
		{"runtime_dir", s.RuntimeDir},
	} {
		if !filepath.IsAbs(path.value) || filepath.Clean(path.value) != path.value {
			return fmt.Errorf("%s must be a clean absolute path: %q", path.field, path.value)
		}
	}
	return nil
}

// ServerArgs produces only known serve flags; no caller-supplied free-form
// args or credentials can be serialized into an OS task/unit command line.
func (s Spec) ServerArgs() ([]string, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	args := []string{"serve", "-c", s.ConfigFile}
	if s.Serve.Addr != "" {
		args = append(args, "--addr", s.Serve.Addr)
	}
	if s.Serve.NoWeb {
		args = append(args, "--no-web")
	}
	if s.Serve.WebDir != "" {
		args = append(args, "--web-dir", s.Serve.WebDir)
	}
	if s.Serve.AllowEmptyToken {
		args = append(args, "--allow-empty-token")
	}
	return args, nil
}

// CheckFiles is the registration preflight. config.Load intentionally accepts
// a missing explicit -c for config editing; a managed serve cannot use it.
func (s Spec) CheckFiles() error {
	if err := s.Validate(); err != nil {
		return err
	}
	for _, path := range []struct{ field, value string }{
		{"exe", s.Exe}, {"config_file", s.ConfigFile},
	} {
		info, err := os.Stat(path.value)
		if err != nil {
			return fmt.Errorf("%s: %w", path.field, err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory: %s", path.field, path.value)
		}
	}
	if info, err := os.Stat(s.WorkDir); err != nil {
		return fmt.Errorf("work_dir: %w", err)
	} else if !info.IsDir() {
		return fmt.Errorf("work_dir is not a directory: %s", s.WorkDir)
	}
	return nil
}

func LoadSpec(path string) (Spec, error) {
	var spec Spec
	data, err := os.ReadFile(path)
	if err != nil {
		return spec, err
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		return spec, fmt.Errorf("decode service spec: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return spec, err
	}
	return spec, nil
}

func saveSpec(path string, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	return writeJSONAtomic(path, spec)
}
