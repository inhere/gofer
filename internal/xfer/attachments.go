package xfer

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
)

// attachments.go is the executing machine's half of the file-delivery path: where an
// attachment is placed inside a project, and how the copies it left behind are pruned.
//
// The two sides that place files (the server's own LocalRunner and the worker's
// transfer code) share these rules deliberately: the directory name, the base it hangs
// under and the retention are ONE contract, and a worker that pruned a different
// directory than the one the server writes to would either never clean up or delete
// something else.

// AttachmentsDirName is the directory used by the legacy exchange-based attachment
// layout. The current default is <project root>/.gofer/attachments; explicit
// exchange_subdir projects retain <exchange>/gofer-attachments.
const AttachmentsDirName = "gofer-attachments"

// AttachmentRetention is how long a placed attachment's date directory survives on the
// executing machine: a placement prunes the ones older than this (the design's 7 days).
// There is no separate timer — the cleanup rides on the next placement.
const AttachmentRetention = 7 * 24 * time.Hour

// attachmentDateLayout is the date subdirectory name a placement uses (YYYYMMDD).
// Pruning only ever looks at entries in THIS shape, so an operator's own directory under
// the attachments dir is never touched.
const attachmentDateLayout = "20060102"

// OwnAttachmentsDirName is the attachment directory an executing machine keeps its OWN
// copies in — the ones that belong to no project of that machine:
// <config-dir>/run/attachments, with the same <date> subdirectories and the same
// AttachmentRetention as a project's AttachmentsDirName. A session with no project (a
// terminal session on a remote worker, say) has no project root to place into, so the
// executing machine qualifies its own runtime directory instead.
const OwnAttachmentsDirName = "attachments"

// OwnAttachmentsDir returns the absolute attachment directory of the machine running
// this process: <config-dir>/run/attachments. The config dir is resolved per call (a test
// can move it through GOFER_CONFIG_DIR), and every placement built from it is re-checked
// by OwnAttachmentsPath before anything is written.
func OwnAttachmentsDir() string {
	return config.RuntimeFilePath("run", OwnAttachmentsDirName)
}

// OwnAttachmentsPath resolves a transfer path RELATIVE to the machine's own attachment
// directory, refusing anything that escapes it (the same boundary a project transfer
// obeys). The base is re-read per call so a moved config dir is honoured.
func OwnAttachmentsPath(rel string) (string, error) {
	base := OwnAttachmentsDir()
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("xfer: empty attachment path")
	}
	dst, err := project.SafeJoin(base, rel)
	if err != nil {
		return "", fmt.Errorf("attachment path escapes the attachment dir: %w", err)
	}
	return dst, nil
}

// PruneOwnAttachmentsAfterPut is the post-placement step for a put that landed in the
// machine's OWN attachment directory: the date subdirectories older than
// AttachmentRetention are deleted. Like PruneAttachmentsAfterPut it is BEST EFFORT — the
// file is already in place, so a failure is only logged. component names the side doing
// it ("server" / "worker"), exactly like the project variant.
func PruneOwnAttachmentsAfterPut(dst, component string) {
	dir := OwnAttachmentsDir()
	if !IsAttachmentPath(dir, dst) {
		return // an ordinary transfer destination: not ours to clean
	}
	removed, err := PruneAttachments(dir, time.Now())
	if err != nil {
		slog.Warn("xfer.attachments_prune_failed", "event", "xfer.attachments_prune_failed", "component", component,
			"dir", dir, "err", err)
		return
	}
	if len(removed) > 0 {
		slog.Info("xfer.attachments_pruned", "event", "xfer.attachments_pruned", "component", component,
			"dir", dir, "count", len(removed), "removed", removed)
	}
}

// AttachmentsDir returns the absolute attachment directory of a project. The default is
// <project root>/.gofer/attachments; an explicit exchange_subdir keeps the historical
// <exchange dir>/gofer-attachments location.
func AttachmentsDir(cfg *config.Config, proj config.ProjectConfig) (string, error) {
	root := cfg.ExecPath(proj)
	if root == "" {
		return "", fmt.Errorf("project has no execution path")
	}
	var dir string
	if cfg.HasExplicitExchangeSubdir(proj) {
		exchange, err := project.ExchangeDir(cfg, proj)
		if err != nil {
			return "", err
		}
		dir = filepath.Join(exchange, AttachmentsDirName)
	} else {
		dir = filepath.Join(root, ".gofer", "attachments")
	}
	if !IsAttachmentPath(root, dir) {
		return "", fmt.Errorf("attachments dir %q is outside the project root %q", dir, root)
	}
	return dir, nil
}

// LegacyAttachmentsDir returns the old exchange-based attachment directory so cleanup
// can age out files created before the default moved.
func LegacyAttachmentsDir(cfg *config.Config, proj config.ProjectConfig) (string, error) {
	exchange, err := project.ExchangeDir(cfg, proj)
	if err != nil {
		return "", err
	}
	return filepath.Join(exchange, AttachmentsDirName), nil
}

// IsAttachmentPath reports whether path lies strictly inside dir (the directory itself
// is not "inside" it: a prune must never act on the base).
func IsAttachmentPath(dir, path string) bool {
	if dir == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// PruneAttachments deletes the DATE subdirectories of dir older than AttachmentRetention
// and returns their names. It only ever looks one level deep, only at directories whose
// name parses as YYYYMMDD, and never follows an entry out of dir — so a file the
// operator dropped in there, or a differently named directory, survives.
//
// A missing dir is not an error: nothing was ever placed on this machine.
func PruneAttachments(dir string, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("xfer: read attachments dir %s: %w", dir, err)
	}
	// The cut is the local midnight AttachmentRetention ago: a directory dated exactly
	// that day is still "today minus 7 days" and stays; anything dated earlier goes.
	cut := now.Add(-AttachmentRetention)
	cut = time.Date(cut.Year(), cut.Month(), cut.Day(), 0, 0, 0, 0, time.Local)
	var removed []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		day, perr := time.ParseInLocation(attachmentDateLayout, e.Name(), time.Local)
		if perr != nil {
			continue // not a date directory: not ours to delete
		}
		if !day.Before(cut) {
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(dir, e.Name())); rerr != nil {
			return removed, fmt.Errorf("xfer: remove attachment dir %s: %w", e.Name(), rerr)
		}
		removed = append(removed, e.Name())
	}
	return removed, nil
}

// PruneAttachmentsAfterPut is the post-placement step both executing sides run: when dst
// landed under the project's attachment directory, the date subdirectories older than
// AttachmentRetention are deleted.
//
// It is BEST EFFORT by contract: the file is already in place, so a prune failure (or a
// project whose paths do not resolve) must never turn a successful placement into a
// failed transfer. The only report is a log line; component names the side doing it
// ("server" / "worker") so an operator can tell the two machines apart.
func PruneAttachmentsAfterPut(cfg *config.Config, proj config.ProjectConfig, dst, component string) {
	if cfg == nil {
		return
	}
	primary, err := AttachmentsDir(cfg, proj)
	if err != nil {
		return // no attachment dir for this project: nothing was placed there either
	}
	legacy, legacyErr := LegacyAttachmentsDir(cfg, proj)
	dirs := []string{primary}
	if legacyErr == nil && legacy != primary {
		dirs = append(dirs, legacy)
	}
	inside := false
	for _, dir := range dirs {
		if IsAttachmentPath(dir, dst) {
			inside = true
			break
		}
	}
	if !inside {
		return // an ordinary transfer destination: not ours to clean
	}
	if err := project.EnsureGoferGitignore(cfg.ExecPath(proj)); err != nil {
		slog.Warn("xfer.gofer_gitignore_failed", "event", "xfer.gofer_gitignore_failed", "component", component,
			"project_root", cfg.ExecPath(proj), "err", err)
	}
	for _, dir := range dirs {
		removed, err := PruneAttachments(dir, time.Now())
		if err != nil {
			slog.Warn("xfer.attachments_prune_failed", "event", "xfer.attachments_prune_failed", "component", component,
				"dir", dir, "err", err)
			continue
		}
		if len(removed) > 0 {
			slog.Info("xfer.attachments_pruned", "event", "xfer.attachments_pruned", "component", component,
				"dir", dir, "count", len(removed), "removed", removed)
		}
	}
}
