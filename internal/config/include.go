package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	yaml "github.com/goccy/go-yaml"
)

// includeKey is the main file's top-level key listing the fragment files.
const includeKey = "include"

// configSources maps every top-level section of a split config to the file it was
// read from (gofer-arsj). The write path uses it to send an edited section back to
// its own file; the console and `config info` show it.
type configSources struct {
	main  string            // absolute path of the main file
	files []string          // fragment files in load order (absolute)
	byKey map[string]string // top-level key -> absolute file (main-file keys included)
}

// SourceFiles lists the files the config was read from: the main file first, then
// the included fragments in load order. A config loaded from one file reports just
// that file; one that was never loaded from disk reports none.
func (c *Config) SourceFiles() []string {
	if c == nil || c.sources == nil {
		return nil
	}
	return append([]string{c.sources.main}, c.sources.files...)
}

// SectionSources maps each top-level section of a split config to the file holding
// it, as a path relative to the main file's directory. Nil for a one-file config.
func (c *Config) SectionSources() map[string]string {
	if c == nil || c.sources == nil || len(c.sources.files) == 0 {
		return nil
	}
	base := filepath.Dir(c.sources.main)
	out := make(map[string]string, len(c.sources.byKey))
	for key, file := range c.sources.byKey {
		if key == includeKey {
			continue
		}
		rel, err := filepath.Rel(base, file)
		if err != nil {
			rel = file
		}
		out[key] = filepath.ToSlash(rel)
	}
	return out
}

// SectionFile returns the absolute file holding top-level section key: its fragment,
// or the main file (also for a section the config does not have yet — that is where
// a new section is written).
func (c *Config) SectionFile(key string) string {
	if c == nil || c.sources == nil {
		return ""
	}
	if f, ok := c.sources.byKey[key]; ok {
		return f
	}
	return c.sources.main
}

// SectionFileOf reads the config at mainPath (with its includes, without defaults or
// validation) and returns the file holding top-level section key — the main file
// when the section is not split out. It is what `config edit --key` opens.
func SectionFileOf(mainPath, key string) (string, error) {
	abs, err := filepath.Abs(mainPath)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	cfg, err := decodeConfig(abs, data)
	if err != nil {
		return "", err
	}
	return cfg.SectionFile(key), nil
}

// applyConfigExtends runs ApplyAgentExtends against the file that holds the agents
// section (the main file's bytes are already in hand).
func applyConfigExtends(cfg *Config, mainPath string, mainData []byte) error {
	raw := mainData
	if f := cfg.SectionFile("agents"); f != "" && f != mainPath {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		raw = b
	}
	return ApplyAgentExtends(cfg.Agents, raw)
}

// topFieldIndex maps a top-level yaml key to its Config field index.
var topFieldIndex = func() map[string]int {
	t := reflect.TypeOf(Config{})
	out := make(map[string]int, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name != "" && name != "-" {
			out[name] = i
		}
	}
	return out
}()

// topKeys returns the top-level keys of a yaml document (nil for an empty one).
func topKeys(file string, data []byte) ([]string, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", file, err)
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// expandIncludes resolves the include list against the main file's directory. A glob
// expands to its matches in name order and may match nothing; a plain path must
// exist. Duplicates (and the main file itself) are dropped.
func expandIncludes(mainPath string, patterns []string) ([]string, error) {
	base := filepath.Dir(mainPath)
	seen := map[string]bool{mainPath: true}
	var out []string
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(base, p)
		}
		var matches []string
		if strings.ContainsAny(p, "*?[") {
			m, err := filepath.Glob(p)
			if err != nil {
				return nil, fmt.Errorf("include %q: %w", p, err)
			}
			sort.Strings(m)
			matches = m
		} else {
			if fi, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("include %q: %w", p, err)
			} else if fi.IsDir() {
				return nil, fmt.Errorf("include %q is a directory (use a glob such as %s)", p, filepath.Join(p, "*.yaml"))
			}
			matches = []string{p}
		}
		for _, m := range matches {
			abs, err := filepath.Abs(m)
			if err != nil {
				return nil, err
			}
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
		}
	}
	return out, nil
}

// decodeConfig decodes the main file and, when it carries `include`, every fragment
// into one Config, recording which file holds each top-level section. Each file is
// probed for removed keys on its own so the error names the file. A section in two
// files, an `include` inside a fragment, or a missing named file fails the load.
func decodeConfig(mainPath string, data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", mainPath, err)
	}
	if err := RejectRemovedKeys(data); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", mainPath, err)
	}
	src := &configSources{main: mainPath, byKey: map[string]string{}}
	cfg.sources = src
	mainKeys, err := topKeys(mainPath, data)
	if err != nil {
		return nil, err
	}
	for _, k := range mainKeys {
		src.byKey[k] = mainPath
	}
	if len(cfg.Include) == 0 {
		return cfg, nil
	}
	files, err := expandIncludes(mainPath, cfg.Include)
	if err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", mainPath, err)
	}
	out := reflect.ValueOf(cfg).Elem()
	for _, file := range files {
		frag, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", file, err)
		}
		if err := RejectRemovedKeys(frag); err != nil {
			return nil, fmt.Errorf("invalid config %s: %w", file, err)
		}
		keys, err := topKeys(file, frag)
		if err != nil {
			return nil, err
		}
		part := &Config{}
		if err := yaml.Unmarshal(frag, part); err != nil {
			return nil, fmt.Errorf("decode config %s: %w", file, err)
		}
		in := reflect.ValueOf(part).Elem()
		for _, k := range keys {
			if k == includeKey {
				return nil, fmt.Errorf("invalid config %s: include is only allowed in the main config file", file)
			}
			if prev, dup := src.byKey[k]; dup {
				return nil, fmt.Errorf("invalid config: top-level section %q is in both %s and %s (a section must live in one file)", k, prev, file)
			}
			src.byKey[k] = file
			if idx, ok := topFieldIndex[k]; ok {
				out.Field(idx).Set(in.Field(idx))
			}
		}
		src.files = append(src.files, file)
	}
	return cfg, nil
}
