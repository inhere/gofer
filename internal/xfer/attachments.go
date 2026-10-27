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

// AttachmentsDirName is the directory placed attachments live in, under a project's
// EXCHANGE directory (the same base a job's result dir is built from): the default
// location is therefore <project root>/tmp/gofer-attachments, and a project that moved
// its exchange_subdir keeps its attachments next to its transfer exchange dir.
const AttachmentsDirName = "gofer-attachments"

// AttachmentRetention is how long a placed attachment's date directory survives on the
// executing machine: a placement prunes the ones older than this (the design's 7 days).
// There is no separate timer — the cleanup rides on the next placement.
const AttachmentRetention = 7 * 24 * time.Hour

// attachmentDateLayout is the date subdirectory name a placement uses (YYYYMMDD).
// Pruning only ever looks at entries in THIS shape, so an operator's own directory under
// the attachments dir is never touched.
const attachmentDateLayout = "20060102"

// ServerAttachmentsDirName is the directory under the gofer config dir's run/ folder where
// the server keeps the attachments it placed WITHOUT a project (a session run by the server
// itself whose directory belongs to no project: there is no project root to place into, and
// an agent still has to be able to open the file).
const ServerAttachmentsDirName = "attachments"

// ServerAttachmentsBase is <config-dir>/run/attachments.
func ServerAttachmentsBase() string {
	return config.RuntimeFilePath("run", ServerAttachmentsDirName)
}

// ServerAttachmentsDir is the dated directory of ServerAttachmentsBase. It uses the SAME
// date layout and retention as a project's attachments, so PruneServerAttachments cleans it
// with the very rules that clean a project's copies.
func ServerAttachmentsDir(now time.Time) string {
	return filepath.Join(ServerAttachmentsBase(), now.Format(attachmentDateLayout))
}

// PruneServerAttachments prunes ServerAttachmentsBase the way a placement prunes a
// project's directory. Like PruneAttachmentsAfterPut it is BEST EFFORT: the file is already
// placed, so a failure is only logged.
func PruneServerAttachments(now time.Time) {
	base := ServerAttachmentsBase()
	removed, err := PruneAttachments(base, now)
	if err != nil {
		slog.Warn("xfer.attachments_prune_failed", "event", "xfer.attachments_prune_failed", "component", "server",
			"dir", base, "err", err)
		return
	}
	if len(removed) > 0 {
		slog.Info("xfer.attachments_pruned", "event", "xfer.attachments_pruned", "component", "server",
			"dir", base, "count", len(removed), "removed", removed)
	}
}

// AttachmentsDir returns the absolute attachment directory of a project:
// <exchange dir>/gofer-attachments.
//
// The result is re-checked to be INSIDE the project's execution root, because its
// caller DELETES directories in it: project.ExchangeDir already resolves the exchange
// subdir with project.SafeJoin (so an escaping `exchange_subdir` is refused), and the
// extra relative check keeps that guarantee explicit here instead of one import away.
func AttachmentsDir(cfg *config.Config, proj config.ProjectConfig) (string, error) {
	exchange, err := project.ExchangeDir(cfg, proj)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(exchange, AttachmentsDirName)
	root := cfg.ExecPath(proj)
	if root == "" {
		return "", fmt.Errorf("project has no execution path")
	}
	if !IsAttachmentPath(root, dir) {
		return "", fmt.Errorf("attachments dir %q is outside the project root %q", dir, root)
	}
	return dir, nil
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
	dir, err := AttachmentsDir(cfg, proj)
	if err != nil {
		return // no attachment dir for this project: nothing was placed there either
	}
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
