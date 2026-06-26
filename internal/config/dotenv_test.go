package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigDir_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	want, _ := filepath.Abs(dir)
	if got != want {
		t.Fatalf("ConfigDir = %q, want %q", got, want)
	}
}

func TestConfigDir_Default(t *testing.T) {
	t.Setenv(EnvConfigDir, "")

	got, err := ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir: %v", err)
	}
	if filepath.Base(got) != DefaultConfigDirName {
		t.Fatalf("ConfigDir = %q, want it to end in %q", got, DefaultConfigDirName)
	}
}

func TestUserConfigPath_UsesConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)

	got, err := UserConfigPath()
	if err != nil {
		t.Fatalf("UserConfigPath: %v", err)
	}
	want := filepath.Join(dir, "config.yaml")
	if got != want {
		t.Fatalf("UserConfigPath = %q, want %q", got, want)
	}
}

func TestUserWorkerConfigPath_UsesConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)

	got, err := UserWorkerConfigPath()
	if err != nil {
		t.Fatalf("UserWorkerConfigPath: %v", err)
	}
	want := filepath.Join(dir, WorkerConfigFileName)
	if got != want {
		t.Fatalf("UserWorkerConfigPath = %q, want %q", got, want)
	}
}

func TestRuntimeFilePath_UsesConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)

	got := RuntimeFilePath("run", "serve.pid")
	want := filepath.Join(dir, "run", "serve.pid")
	if got != want {
		t.Fatalf("RuntimeFilePath = %q, want %q", got, want)
	}
}

// TestLoadDotenv_PrecedenceAndOrder verifies: cwd/.env overrides cfgDir/.env, a
// key only in one file keeps its value, and an exported OS env wins over .env.
func TestLoadDotenv_PrecedenceAndOrder(t *testing.T) {
	cfgDir := t.TempDir()
	cwdDir := t.TempDir()

	writeFile(t, filepath.Join(cfgDir, ".env"),
		"AB_DOTENV_A=cfg\nAB_DOTENV_B=cfg\nAB_DOTENV_OSWIN=fromfile\n")
	writeFile(t, filepath.Join(cwdDir, ".env"),
		"AB_DOTENV_B=cwd\nAB_DOTENV_C=cwd\n")

	t.Setenv(EnvConfigDir, cfgDir)
	t.Setenv("AB_DOTENV_OSWIN", "osval") // exported before load → must survive
	t.Chdir(cwdDir)

	loaded, err := LoadDotenv()
	if err != nil {
		t.Fatalf("LoadDotenv: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("loaded files = %v, want 2", loaded)
	}

	cases := map[string]string{
		"AB_DOTENV_A":     "cfg", // only in cfgDir/.env
		"AB_DOTENV_B":     "cwd", // cwd overrides cfgDir
		"AB_DOTENV_C":     "cwd", // only in cwd/.env
		"AB_DOTENV_OSWIN": "osval",
	}
	for k, want := range cases {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadDotenv_NoFilesIsNoError(t *testing.T) {
	t.Setenv(EnvConfigDir, t.TempDir()) // empty dir, no .env
	t.Chdir(t.TempDir())                // empty cwd, no .env

	loaded, err := LoadDotenv()
	if err != nil {
		t.Fatalf("LoadDotenv: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("loaded files = %v, want none", loaded)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
