package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

const configDirToken = "{config_dir}"

var pathVariable = regexp.MustCompile(`\{[^{}]+\}`)

// ResolveLocalPath expands the single supported local path variable. A path
// without a variable is returned unchanged, including its relative semantics.
func ResolveLocalPath(field, value string) (string, error) {
	if value == "" || !strings.ContainsAny(value, "{}") {
		return value, nil
	}
	if !strings.HasPrefix(value, configDirToken) {
		if variable := pathVariable.FindString(value); variable != "" {
			return "", fmt.Errorf("%s: unsupported path variable %s (only %s at the root is supported)", field, variable, configDirToken)
		}
		return "", fmt.Errorf("%s: invalid path variable in %q", field, value)
	}
	rest := strings.TrimPrefix(value, configDirToken)
	if rest != "" && !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, "\\") {
		return "", fmt.Errorf("%s: %s must be a path root prefix", field, configDirToken)
	}
	if variable := pathVariable.FindString(rest); variable != "" {
		return "", fmt.Errorf("%s: unsupported path variable %s", field, variable)
	}
	if strings.ContainsAny(rest, "{}") {
		return "", fmt.Errorf("%s: invalid path variable in %q", field, value)
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", fmt.Errorf("%s: resolve config directory: %w", field, err)
	}
	return filepath.Join(dir, filepath.FromSlash(strings.TrimLeft(strings.ReplaceAll(rest, "\\", "/"), "/"))), nil
}

// ValidateLocalPaths checks only paths owned by the local server. It does not
// rewrite the Config, so Save and the config write API retain authored templates.
func (c *Config) ValidateLocalPaths() error {
	paths := []struct{ field, value string }{
		{"server.web_dir", c.Server.WebDir},
		{"log.file", c.Log.File}, {"log.dir", c.Log.Dir},
		{"storage.root", c.Storage.Root}, {"storage.db_path", c.Storage.DBPath},
	}
	if c.Server.TLS != nil {
		paths = append(paths, struct{ field, value string }{"server.tls.cert_file", c.Server.TLS.CertFile}, struct{ field, value string }{"server.tls.key_file", c.Server.TLS.KeyFile})
	}
	for _, path := range paths {
		if _, err := ResolveLocalPath(path.field, path.value); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) ResolveStorageRoot() (string, error) {
	return ResolveLocalPath("storage.root", c.Storage.Root)
}

func (c *Config) ResolveDBPathChecked() (string, error) {
	if p := strings.TrimSpace(c.Storage.DBPath); p != "" {
		return ResolveLocalPath("storage.db_path", p)
	}
	if root := strings.TrimSpace(c.Storage.Root); root != "" {
		resolved, err := c.ResolveStorageRoot()
		if err != nil {
			return "", err
		}
		return filepath.Join(resolved, DBFileName), nil
	}
	dir, err := ConfigDir()
	if err != nil || dir == "" {
		return DBFileName, nil
	}
	return filepath.Join(dir, DBFileName), nil
}
