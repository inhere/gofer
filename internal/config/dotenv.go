package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gookit/goutil/envutil"
)

// EnvFileName is the dotenv file name loaded from the config dir and the cwd.
const EnvFileName = ".env"

// LoadDotenv loads dotenv files BEFORE config resolution so env-driven settings
// (GOFER_CONFIG, GOFER_TOKEN, the per-runner *_token envs, ...) can
// come from a file during local dev. GOFER_CONFIG_DIR is read directly from
// the OS env (it selects WHERE the global .env lives, so it cannot come from one).
//
// Files load in this order, with later files overriding earlier ones:
//  1. <config-dir>/.env  — global (config-dir = $GOFER_CONFIG_DIR or
//     ~/.config/gofer)
//  2. ./.env             — current working dir, project-local; wins over global
//
// Precedence: an exported OS env var always wins over any .env file. goutil's
// Dotenv unconditionally os.Setenv's parsed keys (upper-cased), so we snapshot
// the OS env up front and, for every key the dotenv touched that already existed
// in the OS env, restore the original value. Missing files are skipped. Returns
// the list of files actually loaded (for optional --verbose logging).
func LoadDotenv() ([]string, error) {
	preset := osEnvSnapshot()

	files := make([]string, 0, 2)
	if dir, err := ConfigDir(); err == nil && dir != "" {
		files = append(files, filepath.Join(dir, EnvFileName))
	}
	files = append(files, EnvFileName) // current working dir

	dot := envutil.NewDotenv()
	dot.IgnoreNotExist = true
	if err := dot.LoadFiles(files...); err != nil {
		return nil, err
	}

	// Restore OS-preset values so an explicitly exported env wins over .env.
	// Iterate only over keys the dotenv actually set (already upper-cased) to
	// avoid resurrecting unrelated env vars.
	for key := range dot.LoadedData() {
		if orig, ok := preset[key]; ok {
			_ = os.Setenv(key, orig)
		}
	}
	return dot.LoadedFiles(), nil
}

// osEnvSnapshot captures the current process env as key->value, preserving the
// original key case so a restore writes back the exact same variable.
func osEnvSnapshot() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[kv[:i]] = kv[i+1:]
		}
	}
	return out
}
