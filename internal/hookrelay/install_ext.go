package hookrelay

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/hooks"
)

// ompExtensionFile is the extension file gofer owns inside omp's extensions dir.
const ompExtensionFile = "gofer-relay.ts"

// OmpExtensionMarker is the first-line tag that proves the file is gofer's.
const OmpExtensionMarker = "@gofer-managed-omp-extension"

// installOmp writes (or removes) the omp extension shim. The file is owned by
// gofer: an existing file without the marker is somebody else's and is left
// alone unless force is set.
func installOmp(path string, remove, force bool) (InstallResult, error) {
	res := InstallResult{Path: path}
	cur, err := os.ReadFile(path)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return res, fmt.Errorf("read %s: %w", path, err)
	}
	ours := exists && bytes.Contains(cur, []byte(OmpExtensionMarker))
	if exists && !ours && !force {
		if remove {
			res.Notes = append(res.Notes, path+" is not a gofer-managed extension; left untouched")
			return res, nil
		}
		return res, fmt.Errorf("%s exists and is not a gofer-managed extension; move it or pass --force to replace it", path)
	}
	if remove {
		if !exists {
			res.Notes = append(res.Notes, "nothing to remove: file does not exist")
			return res, nil
		}
		if err := os.Remove(path); err != nil {
			return res, fmt.Errorf("remove %s: %w", path, err)
		}
		res.Removed = 1
		return res, nil
	}
	if ours || exists {
		res.Replaced = 1
	} else {
		res.Created = true
	}
	res.Added = 1
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("mkdir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, hooks.OmpExtension, 0o644); err != nil {
		return res, fmt.Errorf("write %s: %w", path, err)
	}
	return res, nil
}

// jcodeHookEvents are the [hooks] keys gofer fills, in write order. pre_tool is a
// blocking policy gate and is never ours.
var jcodeHookEvents = []string{"session_start", "turn_start", "turn_end", "post_tool", "session_end"}

const jcodeCommand = "gofer hook jcode"

func isOurJcodeCommand(v string) bool {
	v = strings.TrimSpace(v)
	return v == jcodeCommand || strings.HasPrefix(v, jcodeCommand+" ")
}

// jcodeHookLine parses a `key = "value"` line of the [hooks] table. Comments and
// other shapes report ok=false.
func jcodeHookLine(line string) (key, val string, ok bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
		return "", "", false
	}
	k, v, found := strings.Cut(t, "=")
	if !found {
		return "", "", false
	}
	v = strings.TrimSpace(v)
	if len(v) < 2 || (v[0] != '"' && v[0] != '\'') || v[len(v)-1] != v[0] {
		return strings.TrimSpace(k), "", true // non-string value (numbers etc.)
	}
	return strings.TrimSpace(k), v[1 : len(v)-1], true
}

func isJcodeHooksHeader(line string) bool {
	return strings.TrimSpace(line) == "[hooks]"
}

func isTableHeader(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "[")
}

// installJcode merges gofer's commands into the [hooks] table of jcode's
// config.toml with a line-level edit (no TOML round-trip, so comments, ordering
// and every other table survive byte for byte). jcode takes ONE command per
// event: an event that already holds a user command is left alone and reported;
// an empty string counts as unset.
func installJcode(path string, remove, _ bool) (InstallResult, error) {
	res := InstallResult{Path: path}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		if remove {
			res.Notes = append(res.Notes, "nothing to remove: file does not exist")
			return res, nil
		}
		res.Created = true
	default:
		return res, fmt.Errorf("read %s: %w", path, err)
	}
	text := string(raw)
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}

	start, end := -1, len(lines) // [hooks] body is lines[start+1:end]
	for i, l := range lines {
		if start < 0 {
			if isJcodeHooksHeader(l) {
				start = i
			}
			continue
		}
		if isTableHeader(l) {
			end = i
			break
		}
	}

	want := map[string]bool{}
	for _, e := range jcodeHookEvents {
		want[e] = true
	}
	handled := map[string]bool{}
	var out []string
	if start >= 0 {
		out = append(out, lines[:start+1]...)
		for _, l := range lines[start+1 : end] {
			k, v, ok := jcodeHookLine(l)
			if !ok || !want[k] {
				out = append(out, l)
				continue
			}
			handled[k] = true
			switch {
			case isOurJcodeCommand(v):
				if remove {
					res.Removed++
					continue
				}
				res.Replaced++
				out = append(out, fmt.Sprintf("%s = %q", k, jcodeCommand))
				res.Added++
			case strings.TrimSpace(v) == "":
				if remove {
					out = append(out, l)
					continue
				}
				out = append(out, fmt.Sprintf("%s = %q", k, jcodeCommand))
				res.Added++
			default:
				out = append(out, l)
				if !remove {
					res.Notes = append(res.Notes, fmt.Sprintf("[hooks] %s already runs %q (kept; jcode takes one command per event, so gofer does not report it)", k, v))
				}
			}
		}
		if !remove {
			var add []string
			for _, e := range jcodeHookEvents {
				if !handled[e] {
					add = append(add, fmt.Sprintf("%s = %q", e, jcodeCommand))
					res.Added++
				}
			}
			out = append(out, add...)
		}
		out = append(out, lines[end:]...)
	} else {
		out = append(out, lines...)
		if !remove {
			if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
				out = append(out, "")
			}
			out = append(out, "[hooks]")
			for _, e := range jcodeHookEvents {
				out = append(out, fmt.Sprintf("%s = %q", e, jcodeCommand))
				res.Added++
			}
		}
	}
	// A dropped table that ended up empty keeps its header: harmless, and jcode
	// itself writes `[hooks]` with pre_tool_timeout_ms by default.
	newText := strings.Join(out, nl)
	if len(out) > 0 {
		newText += nl
	}
	if newText == text {
		return res, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("mkdir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(newText), 0o644); err != nil {
		return res, fmt.Errorf("write %s: %w", path, err)
	}
	return res, nil
}
