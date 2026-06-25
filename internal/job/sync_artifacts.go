package job

import (
	"encoding/json"
	"strings"
	"time"
)

// Synchronous-submit wait caps (design §6.1 / P1-a): a sync submit blocks at
// most DefaultWaitSec when no explicit wait_timeout_sec is given, and never
// longer than MaxWaitSec, after which the caller falls back to async semantics
// (the HTTP layer maps this to 202 + X-Gofer-Async). These were the httpapi
// handler's defaultWaitSec/maxWaitSec, moved here with the wait policy.
const (
	DefaultWaitSec = 30
	MaxWaitSec     = 60
)

// SubmitSync submits a job and, when synchronous semantics are requested, blocks
// until it reaches a terminal state (capped). It collects the sync-wait policy
// that previously lived in httpapi.handleCreateJob (Submit + WaitFor + clampWait
// + async fallback判定), so the handler only maps the outcome to HTTP表现
// (200 / 202 + X-Gofer-Async). The wantSync decision (?wait=1 query param) stays
// in the handler — it is an HTTP-transport concern — and is passed in via sync.
//
// Returns:
//   - out:   the JobResult to hand back to the client — the terminal result when
//     a sync wait succeeded, otherwise the initial submit snapshot (also the
//     snapshot to return when async fallback kicks in);
//   - async: true when the wait exceeded the server cap while still running (the
//     job keeps running in the background; the client should switch to polling);
//   - err:   a Submit rejection (handler maps via submitStatus).
func (s *Service) SubmitSync(req JobRequest, sync bool) (out JobResult, async bool, err error) {
	res, err := s.Submit(req)
	if err != nil {
		return JobResult{}, false, err
	}

	// Synchronous submit: block until terminal (capped). An already-terminal
	// result (e.g. an idempotent hit on a finished job) returns immediately.
	if sync && !IsTerminal(res.Status) {
		if final, ok := s.WaitFor(res.ID, clampWait(req.WaitTimeoutSec)); ok {
			return final, false, nil
		}
		// Exceeded the server wait cap and still not terminal: fall back to async
		// semantics. The job keeps running; the client should switch to polling.
		return res, true, nil
	}
	return res, false, nil
}

// clampWait turns a requested wait_timeout_sec into a duration, applying the
// default (when 0/negative) and the hard server cap.
func clampWait(sec int) time.Duration {
	if sec <= 0 {
		sec = DefaultWaitSec
	}
	if sec > MaxWaitSec {
		sec = MaxWaitSec
	}
	return time.Duration(sec) * time.Second
}

// ArtifactManifest is the resolved artifact view for a job: the manifest items
// (always non-nil) plus whether the job ran on a remote machine (worker/peer),
// where its artifact files live — so a byte download from this host is not
// served (P4 / D6). The HTTP/MCP layers map Remote to a 409.
type ArtifactManifest struct {
	Items  []ArtifactItem
	Remote bool
	// Source echoes the job's execution location (""=local / worker:<id> /
	// peer:<name>) so the caller can build the remote-artifact message.
	Source string
}

// GetArtifactManifest resolves a job's artifact manifest and remote-source
// status. It collects the data-plane logic that previously lived in
// httpapi.manifestFor + remoteSource: prefer the persisted manifest
// (ArtifactsJSON, captured at finish), else a live scan of the result dir
// (always non-nil); and report whether the job ran on a remote machine. An
// unknown id returns ok=false (handler maps to 404). The HTTP/MCP layers keep
// the status mapping (404/409).
func (s *Service) GetArtifactManifest(id string) (ArtifactManifest, bool) {
	res, ok := s.Get(id)
	if !ok {
		return ArtifactManifest{}, false
	}
	return ArtifactManifest{
		Items:  manifestFor(res),
		Remote: IsRemoteSource(res.Source),
		Source: res.Source,
	}, true
}

// manifestFor resolves a job's artifact list: the persisted manifest when
// present (and parseable), else a live scan of the result dir. It always
// returns a non-nil slice.
func manifestFor(res JobResult) []ArtifactItem {
	if res.ArtifactsJSON != "" {
		var items []ArtifactItem
		if err := json.Unmarshal([]byte(res.ArtifactsJSON), &items); err == nil && items != nil {
			return items
		}
		// Corrupt/empty manifest → fall through to a live scan rather than 500.
	}
	if items := ScanArtifacts(res.ResultDir); items != nil {
		return items
	}
	return []ArtifactItem{}
}

// IsRemoteSource reports whether a job's Source marks a remote execution machine
// (worker:<id> / peer:<name>), i.e. its artifact files live on that machine, not
// on this host (P4 / D6). An empty source is local. Exported so the artifact
// download path can do the remote-artifact 409 check without resolving the full
// manifest (it was httpapi.remoteSource).
func IsRemoteSource(source string) bool {
	return strings.HasPrefix(source, "worker:") || strings.HasPrefix(source, "peer:")
}
