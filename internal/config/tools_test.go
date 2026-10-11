package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	yaml "github.com/goccy/go-yaml"
)

// TestValidateTools covers the CFG-16 declaration rules: required / unique
// (case-insensitive) / charset / length of the name, desc length, count, and the
// rendered-size cap that names the largest entries.
func TestValidateTools(t *testing.T) {
	long := strings.Repeat("x", ToolDescMaxRunes)
	cases := []struct {
		name  string
		tools []Tool
		want  string // "" = valid; otherwise a substring of the error
	}{
		{"empty", nil, ""},
		{"ok", []Tool{{Name: "ffmpeg", Desc: "transcoder"}, {Name: "plc.sim-2_x", Bin: `D:\tools\sim.exe`}}, ""},
		{"desc at limit (runes, not bytes)", []Tool{{Name: "a", Desc: strings.Repeat("中", ToolDescMaxRunes)}}, ""},
		{"missing name", []Tool{{Desc: "d"}}, "tools[0]: name is required"},
		{"bad charset", []Tool{{Name: "my tool"}}, `name "my tool" may only contain`},
		{"too long name", []Tool{{Name: strings.Repeat("n", ToolNameMaxLen+1)}}, "longer than 64"},
		{"duplicate ignores case", []Tool{{Name: "FFmpeg"}, {Name: "ffmpeg"}}, "tools[1]: name \"ffmpeg\" duplicates [0]"},
		{"desc too long", []Tool{{Name: "a", Desc: long + "y"}}, "desc is 201 characters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTools(tc.tools)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}

	t.Run("too many", func(t *testing.T) {
		tools := make([]Tool, ToolsMaxCount+1)
		for i := range tools {
			tools[i] = Tool{Name: fmt.Sprintf("t%d", i)}
		}
		if err := ValidateTools(tools); err == nil || !strings.Contains(err.Error(), "51 entries, at most 50") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("rendered list over the cap names the largest", func(t *testing.T) {
		tools := make([]Tool, 0, 30)
		for i := 0; i < 30; i++ {
			tools = append(tools, Tool{Name: fmt.Sprintf("tool%02d", i), Desc: strings.Repeat("d", 150)})
		}
		tools[7].Desc = strings.Repeat("w", ToolDescMaxRunes)
		err := ValidateTools(tools)
		if err == nil || !strings.Contains(err.Error(), "over 4096") {
			t.Fatalf("error = %v, want the size cap", err)
		}
		if !strings.Contains(err.Error(), "largest: tool07 (") {
			t.Errorf("error should name the largest entry first: %v", err)
		}
	})
}

// TestToolLine pins the one rendered line the size cap and the prompt share: bin is
// shown only when it differs from the name, desc only when set.
func TestToolLine(t *testing.T) {
	cases := []struct {
		tool Tool
		want string
	}{
		{Tool{Name: "ffmpeg"}, "- `ffmpeg`"},
		{Tool{Name: "ffmpeg", Desc: " transcoder "}, "- `ffmpeg` — transcoder"},
		{Tool{Name: "ffmpeg", Bin: "ffmpeg"}, "- `ffmpeg`"},
		{Tool{Name: "sim", Bin: "/opt/sim", Desc: "simulator"}, "- `sim`（`/opt/sim`）— simulator"},
	}
	for _, tc := range cases {
		if got := ToolLine(tc.tool); got != tc.want {
			t.Errorf("ToolLine(%+v) = %q, want %q", tc.tool, got, tc.want)
		}
	}
}

// TestServerConfigTools proves the server's top-level `tools` decodes, survives
// Clone without sharing its backing array, and that Load rejects a bad declaration.
func TestServerConfigTools(t *testing.T) {
	var cfg Config
	if err := yaml.Unmarshal([]byte("tools:\n  - name: docker\n    desc: containers\n"), &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cfg.Tools) != 1 || cfg.Tools[0].Name != "docker" || cfg.Tools[0].Desc != "containers" {
		t.Fatalf("tools = %+v", cfg.Tools)
	}
	clone := cfg.Clone()
	clone.Tools[0].Name = "changed"
	if cfg.Tools[0].Name != "docker" {
		t.Error("Clone shares the tools backing array with the original")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "projects:\n  demo:\n    host_path: " + filepath.ToSlash(dir) + "\ntools:\n  - name: a\n  - name: A\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicates") {
		t.Fatalf("Load error = %v, want the duplicate-name rejection", err)
	}
}

// TestMissingToolBins reports only the executables this machine cannot resolve.
func TestMissingToolBins(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	got := MissingToolBins([]Tool{
		{Name: "self", Bin: exe},
		{Name: "nope", Bin: "gofer-test-no-such-binary-xyz"},
	})
	if len(got) != 1 || got[0] != "nope (gofer-test-no-such-binary-xyz)" {
		t.Fatalf("missing = %v", got)
	}
}
