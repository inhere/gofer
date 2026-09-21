package job

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
)

// xfer.go is XFER-01 X2's job-side half: the file steps a job carries with it.
//
//   - Uploads are staged transfers the EXECUTING machine places in the job's cwd
//     BEFORE the agent starts (a failed upload fails the job — the agent must never
//     run on a cwd the caller did not describe). The payload bytes always travel
//     HTTP, never here.
//   - Collect globs are matched in that same cwd AFTER the job ends (on failure too,
//     and after the verify step): each match becomes an artifact of the job
//     (artifacts/collected/<project-root-relative path>), so the existing artifact
//     download/preview surface serves it.
//
// Which machine moves the bytes is the XferBridge seam: the hub copies from its own
// staging area and PULLS a worker's collected files back through the transfer
// manager; a worker fetches an upload over HTTP with its own token and lets the hub
// pull what it collected (it cannot write the hub's result dir). The job package
// therefore stays free of HTTP, of the wire and of internal/xfer (G022).
type XferBridge interface {
	// FetchUpload places the staged transfer xferID at dst — an absolute path this
	// machine resolved (the job's cwd + the request's Dest) — and returns an error
	// describing why it could not (the job then fails with it).
	FetchUpload(ctx context.Context, xferID, projectKey, dst string) error
	// PullCollected fetches one file the EXECUTING machine matched for collection into
	// dst (an absolute path in the job's artifact directory on THIS machine) and
	// returns its size. Only a machine that OWNS the job's result dir implements it;
	// see OwnsArtifacts.
	PullCollected(ctx context.Context, p CollectedPull) (int64, error)
	// CollectLimits are the collect caps in force (per file, and per job in total).
	// Zero means "no cap" — a bridge that has no transfer configuration says so.
	CollectLimits() CollectLimits
	// OwnsArtifacts reports whether the JOB ROW's result dir is on this machine. The
	// hub says true: a collected file is copied straight into the artifacts (and a
	// worker's collected files are PULLED back here). A worker says false: it reports
	// what it matched, and the hub pulls the bytes once the outcome arrives.
	OwnsArtifacts() bool
}

// CollectedPull is one "fetch this collected file" request for the bridge.
type CollectedPull struct {
	// JobID is the job the file belongs to (journaled on the transfer, so the audit
	// trail says which job a transfer served).
	JobID string
	// Runner is the EXECUTING machine's transfer runner: the resolved worker id of a
	// runner=worker job (the same key the transfer journal routes on).
	Runner string
	// ProjectKey is the project whose root the name is relative to on that machine.
	ProjectKey string
	// Name is the file's path relative to the project root ON THE EXECUTING MACHINE.
	Name string
	// Dst is where THIS machine wants the bytes (absolute, already inside the job's
	// artifact directory).
	Dst string
}

// CollectLimits are the `--collect` byte caps: one file, and the job's total. A zero
// field means the bridge has no cap for it.
type CollectLimits struct {
	MaxFile  int64
	MaxTotal int64
}

// SetXferBridge installs the job file-transfer seam. It is wired at assemble time
// (core.Build for a hub, the worker command for a worker); without one a job that
// asks for uploads fails cleanly with an explanation and a collect-only job records
// its matches without moving bytes.
func (s *Service) SetXferBridge(b XferBridge) { s.xfer = b }

// UploadSpec is one file that travels with a job (XFER-01 X2): a staged transfer id
// plus the destination on the EXECUTING machine, relative to the job's cwd. The wire
// and yaml tags mirror each other so the md+yaml task-file path can carry one too.
type UploadSpec struct {
	XferID string `json:"xfer_id" yaml:"xfer_id"`
	Dest   string `json:"dest" yaml:"dest"`
}

// XferSummary is what a job's file steps did (XFER-01 X2), persisted as
// jobs.xfer_json and shown by `job show` / the web detail. It is nil for a job that
// carried no files (an empty summary would be a claim the job never made).
type XferSummary struct {
	// Uploads is one entry per --upload, in request order, with the outcome of
	// placing it on the executing machine.
	Uploads []XferUploadResult `json:"uploads,omitempty"`
	// Collected is one entry per file the collect globs matched, with its path
	// relative to the PROJECT ROOT (the coordinate the artifacts and the transfer
	// journal use).
	Collected []XferFile `json:"collected,omitempty"`
	// Skipped is what collect deliberately left out, with the reason ("too large", a
	// failed pull, a file that vanished between the match and the read).
	Skipped []XferSkipped `json:"skipped,omitempty"`
}

// XferUploadResult is one upload's outcome. Dest is the request's destination as
// given (cwd-relative); Size is what landed there.
type XferUploadResult struct {
	Dest  string `json:"dest"`
	Size  int64  `json:"size,omitempty"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// XferFile is one collected file: its project-root-relative path and its size.
type XferFile struct {
	Name string `json:"name"`
	Size int64  `json:"size,omitempty"`
}

// XferSkipped is one file a collect pattern matched but that was not collected.
type XferSkipped struct {
	Pattern string `json:"pattern,omitempty"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason"`
}

// collectedDir is where a job's collected files live inside its result dir: the
// directory the artifacts manifest already enumerates, so GET
// /v1/jobs/{id}/artifacts/collected/<name> serves them with no new surface.
const collectedDir = "collected"

// Upload/collect failure reasons that are not the transfer's own error.
const (
	reasonTooLarge      = "too large"
	reasonTotalExceeded = "collect total exceeds the limit"
	reasonOutsideRoot   = "outside the project root"
	reasonVanish        = "file disappeared before it could be read"
)

// materializeUploads places every staged upload of this job on THIS machine, before
// the agent starts. It returns the error the job must fail with (the caller turns it
// into the terminal state) — the first failure stops the list, because the job is
// about to fail anyway and a half-placed set must not be reported as complete.
//
// Best-effort exactly like the rest of the outcome capture: whatever happened is
// recorded on the job (entry.result.Xfer) and as an event, so a failed upload is
// inspectable even though the job never ran.
func (s *Service) materializeUploads(ctx context.Context, entry *jobEntry, req runner.Request, workDir, projectKey string) error {
	if len(req.Uploads) == 0 {
		return nil
	}
	summary := &XferSummary{}
	var firstErr error
	for _, up := range req.Uploads {
		res := XferUploadResult{Dest: up.Dest}
		switch {
		case s.xfer == nil:
			res.Error = "file transfers are not available on this machine"
		default:
			dst, err := project.SafeJoin(workDir, up.Dest)
			if err != nil {
				res.Error = err.Error()
				break
			}
			if err := s.xfer.FetchUpload(ctx, up.XferID, projectKey, dst); err != nil {
				res.Error = err.Error()
				break
			}
			if info, err := os.Stat(dst); err == nil {
				res.Size = info.Size()
			}
			res.OK = true
		}
		summary.Uploads = append(summary.Uploads, res)
		if !res.OK && firstErr == nil {
			s.recordEvent(req.JobID, EventJobUploadFailed, map[string]any{
				"dest": up.Dest, "xfer_id": up.XferID, "error": res.Error,
			})
			firstErr = fmt.Errorf("upload %s: %s", up.Dest, res.Error)
		}
	}
	entry.mu.Lock()
	entry.result.Xfer = mergeXferSummary(entry.result.Xfer, summary)
	entry.mu.Unlock()
	return firstErr
}

// collectFiles matches the job's collect globs in workDir (the cwd the job ran in, on
// the machine that ran it) and, when this machine owns the job's result dir, copies
// each match into artifacts/collected/<project-root-relative path>. What it matched
// and what it left out land on the job's Xfer summary; the event carries the tally.
//
// It never returns an error: a collect step is reporting, and a job's terminal status
// is decided by the agent (and its verify step) alone.
func (s *Service) collectFiles(ctx context.Context, entry *jobEntry, req runner.Request, workDir string) {
	if len(req.Collect) == 0 {
		return
	}
	entry.mu.Lock()
	projectKey, resultDir, jobID := entry.result.ProjectKey, entry.result.ResultDir, entry.result.ID
	entry.mu.Unlock()

	limits := s.collectLimits()
	root := s.projectRoot(projectKey)
	summary := &XferSummary{}
	var total int64
	for _, pattern := range req.Collect {
		matches, err := filepath.Glob(filepath.Join(workDir, filepath.FromSlash(pattern)))
		if err != nil {
			summary.Skipped = append(summary.Skipped, XferSkipped{Pattern: pattern, Reason: "invalid pattern: " + err.Error()})
			continue
		}
		sort.Strings(matches)
		for _, match := range matches {
			info, err := os.Lstat(match)
			if err != nil {
				summary.Skipped = append(summary.Skipped, XferSkipped{Pattern: pattern, Name: match, Reason: reasonVanish})
				continue
			}
			// Directories and symlinks are never collected (design §一.3): a symlink
			// could point outside the project, and a directory is not a file.
			if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			name := rootRelative(root, match)
			if name == "" {
				summary.Skipped = append(summary.Skipped, XferSkipped{Pattern: pattern, Name: match, Reason: reasonOutsideRoot})
				continue
			}
			if limits.MaxFile > 0 && info.Size() > limits.MaxFile {
				summary.Skipped = append(summary.Skipped, XferSkipped{Pattern: pattern, Name: name, Reason: reasonTooLarge})
				continue
			}
			if limits.MaxTotal > 0 && total+info.Size() > limits.MaxTotal {
				summary.Skipped = append(summary.Skipped, XferSkipped{Pattern: pattern, Name: name, Reason: reasonTotalExceeded})
				continue
			}
			if s.xfer != nil && s.xfer.OwnsArtifacts() {
				dst := filepath.Join(resultDir, "artifacts", collectedDir, filepath.FromSlash(name))
				if err := copyIntoArtifacts(match, dst); err != nil {
					summary.Skipped = append(summary.Skipped, XferSkipped{Pattern: pattern, Name: name, Reason: err.Error()})
					continue
				}
			}
			total += info.Size()
			summary.Collected = append(summary.Collected, XferFile{Name: name, Size: info.Size()})
		}
	}
	if len(summary.Collected) == 0 && len(summary.Skipped) == 0 {
		return
	}
	entry.mu.Lock()
	entry.result.Xfer = mergeXferSummary(entry.result.Xfer, summary)
	entry.mu.Unlock()
	s.recordEvent(jobID, EventJobFilesCollected, map[string]any{
		"count": len(summary.Collected), "bytes": total, "skipped": len(summary.Skipped),
	})
}

// pullCollected brings a REMOTE job's collected files back to the machine that owns
// its artifacts (the hub), reusing XFER-01's pull path: the transfer manager stages a
// get for the executing worker and the worker uploads the bytes. Each pulled file is
// added to the job's artifact manifest, so the download/preview surface serves it
// exactly like a file the job left in its own result dir.
//
// A file that cannot be pulled is moved from Collected to Skipped with the reason:
// the summary must never claim a file that is not there.
func (s *Service) pullCollected(ctx context.Context, entry *jobEntry, o *runner.Outcome) {
	if len(o.Xfer) == 0 {
		return
	}
	var summary XferSummary
	if err := json.Unmarshal(o.Xfer, &summary); err != nil {
		slog.Warn("pullCollected: remote xfer summary is not valid JSON, skipped", "job_id", entry.result.ID, "err", err)
		return
	}
	if len(summary.Collected) == 0 {
		return
	}
	entry.mu.Lock()
	jobID, projectKey, resultDir := entry.result.ID, entry.result.ProjectKey, entry.result.ResultDir
	workerID := entry.result.WorkerID
	entry.mu.Unlock()

	kept := make([]XferFile, 0, len(summary.Collected))
	items := decodeArtifacts(entry)
	for _, f := range summary.Collected {
		dst := filepath.Join(resultDir, "artifacts", collectedDir, filepath.FromSlash(f.Name))
		switch {
		case s.xfer == nil:
			summary.Skipped = append(summary.Skipped, XferSkipped{Name: f.Name, Reason: "this server has no transfer manager"})
			continue
		case workerID == "":
			summary.Skipped = append(summary.Skipped, XferSkipped{Name: f.Name, Reason: "the executing machine cannot transfer files"})
			continue
		}
		size, err := s.xfer.PullCollected(ctx, CollectedPull{
			JobID: jobID, Runner: workerID, ProjectKey: projectKey, Name: f.Name, Dst: dst,
		})
		if err != nil {
			summary.Skipped = append(summary.Skipped, XferSkipped{Name: f.Name, Reason: err.Error()})
			continue
		}
		if size >= 0 {
			f.Size = size
		}
		kept = append(kept, f)
		items = append(items, ArtifactItem{
			Name: collectedDir + "/" + f.Name, Size: f.Size, Mtime: fileMtime(dst),
		})
	}
	summary.Collected = kept
	entry.mu.Lock()
	entry.result.Xfer = mergeXferSummary(entry.result.Xfer, &summary)
	if len(items) > 0 {
		if b, err := json.Marshal(items); err == nil {
			entry.result.ArtifactsJSON = string(b)
		}
	}
	entry.mu.Unlock()
}

// decodeArtifacts reads back the artifact manifest a remote outcome just applied (the
// execution machine's own list), so a pull can EXTEND it instead of replacing it.
func decodeArtifacts(entry *jobEntry) []ArtifactItem {
	entry.mu.Lock()
	raw := entry.result.ArtifactsJSON
	entry.mu.Unlock()
	if raw == "" {
		return nil
	}
	var items []ArtifactItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

// collectLimits resolves this machine's collect caps (no bridge = no caps, which only
// matters for a deployment with no transfer manager at all: nothing is copied then).
func (s *Service) collectLimits() CollectLimits {
	if s.xfer == nil {
		return CollectLimits{}
	}
	return s.xfer.CollectLimits()
}

// projectRoot resolves a project key to its execution root on THIS machine (the
// reference the collected names are relative to). "" when the project is unknown —
// the caller then treats every match as outside the root.
func (s *Service) projectRoot(key string) string {
	cfg := s.config()
	if cfg == nil {
		return ""
	}
	proj, err := s.projects.Get(key)
	if err != nil {
		return ""
	}
	return cfg.ExecPath(proj)
}

// rootRelative returns abs as a slash-separated path relative to root, or "" when it
// is not inside root (a collect pattern must never reach outside the project — the
// same boundary a job's --cwd and a transfer's path obey).
func rootRelative(root, abs string) string {
	if root == "" {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return ""
	}
	return path.Clean(filepath.ToSlash(rel))
}

// copyIntoArtifacts copies src to dst (under the job's artifacts), creating parents.
// The copy goes through a temp file in the destination directory so a reader never
// sees a half-written artifact.
func copyIntoArtifacts(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".gofer-part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fileMtime is an artifact entry's modification time (unix seconds); 0 when the file
// cannot be stat'ed (the entry is still listed — the manifest is a finding aid).
func fileMtime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().Unix()
}

// mergeXferSummary folds one step's summary into the job's running one, so the upload
// step and the collect step (and a later pull) each contribute to one record.
func mergeXferSummary(base, add *XferSummary) *XferSummary {
	if base == nil {
		base = &XferSummary{}
	}
	if add == nil {
		return base
	}
	base.Uploads = append(base.Uploads, add.Uploads...)
	base.Collected = append(base.Collected, add.Collected...)
	base.Skipped = append(base.Skipped, add.Skipped...)
	return base
}

// marshalXfer / unmarshalXfer serialize the summary into jobs.xfer_json (E-pattern:
// nil stays "" so a job that carried no files reads back as "no transfers"). The
// reverse tolerant on a corrupt column: the summary is reporting, never a
// precondition for reading a job.
func marshalXfer(x *XferSummary) string {
	if x == nil || (len(x.Uploads) == 0 && len(x.Collected) == 0 && len(x.Skipped) == 0) {
		return ""
	}
	b, err := json.Marshal(x)
	if err != nil {
		return ""
	}
	return string(b)
}

func unmarshalXfer(raw string) *XferSummary {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var x XferSummary
	if err := json.Unmarshal([]byte(raw), &x); err != nil {
		return nil
	}
	return &x
}
