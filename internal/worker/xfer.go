package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/wsproto"
)

// xferMaxConcurrent bounds how many transfers one worker runs at once. A transfer is
// mostly a stream through this process (disk + HTTP), so two in flight keeps a slow
// link busy without letting a burst of transfers swamp the worker's real job.
// Excess frames WAIT for a slot rather than being refused: an operator pushed that
// file deliberately, and a far-side refusal is a silent failure — while the wait is
// bounded by the transfer's own deadline, so a queued transfer can never outlive it.
const xferMaxConcurrent = 2

// errXferExists is the refusal to overwrite an existing destination without force. Only
// its TEXT is the contract: the hub records the literal error string and settles the
// transfer with it. It is spelled here rather than shared with internal/xfer because the
// two sides are different processes exchanging a message — an error VALUE would not
// survive the wire anyway.
var errXferExists = errors.New("exists")

// errXferSourceNotFound is reported for a get whose source is not on this machine; the
// hub maps it to the same "source not found" text a local transfer produces.
var errXferSourceNotFound = errors.New("source not found")

// xferHTTP is the client every transfer uses. It carries no timeout of its own: each
// transfer runs under a ctx with its own deadline (worker.xfer_timeout_sec), which is
// the bound that must apply to the whole GET/PUT.
var xferHTTP = &http.Client{}

// handleFileXfer carries out ONE inbound transfer instruction (XFER-01, protocol v9).
//
// It mirrors handleDispatch's contract: it never returns an error to the caller —
// every outcome, success or failure, is reported back as a file_xfer_result on the
// wire, because the hub's dispatch is parked on exactly that frame. It runs in its own
// goroutine (started by recvLoop), so a 256MB transfer can never stall the read loop's
// pongs, cancels or dispatches.
func (cl *Client) handleFileXfer(ctx context.Context, sessionURL string, fr wsproto.FileXfer) {
	started := time.Now()
	// The slot wait counts against the transfer's OWN deadline (the ctx is created
	// first): a transfer queued behind two running ones is still expected to finish
	// within the same budget, and reporting "no slot in time" is truthful, whereas an
	// unbounded wait would let the hub give up (and mark the transfer failed) long
	// before this machine even started moving the bytes.
	tctx, cancel := context.WithTimeout(ctx, cl.effectiveXferTimeout())
	defer cancel()
	select {
	case cl.xferSem <- struct{}{}:
		defer func() { <-cl.xferSem }()
	case <-tctx.Done():
		reason := "timed out waiting for a free transfer slot"
		if ctx.Err() != nil {
			reason = "worker is shutting down"
		}
		cl.reportFileXfer(ctx, wsproto.FileXferResult{
			XferID:     fr.XferID,
			Error:      reason,
			DurationMS: time.Since(started).Milliseconds(),
		})
		return
	}

	size, sum, err := cl.runFileXfer(tctx, sessionURL, fr)

	res := wsproto.FileXferResult{
		XferID:     fr.XferID,
		OK:         err == nil,
		Size:       size,
		SHA256:     sum,
		DurationMS: time.Since(started).Milliseconds(),
	}
	if err != nil {
		res.Error = err.Error()
		slog.Warn("xfer.failed", "event", "xfer.failed", "component", "worker",
			"worker_id", cl.workerID, "xfer_id", fr.XferID, "op", fr.Op,
			"project", fr.ProjectKey, "path", fr.Path, "err", err)
	} else {
		slog.Info("xfer.done", "event", "xfer.done", "component", "worker",
			"worker_id", cl.workerID, "xfer_id", fr.XferID, "op", fr.Op,
			"project", fr.ProjectKey, "path", fr.Path, "size", size, "sha256", sum,
			"duration_ms", res.DurationMS)
	}
	cl.reportFileXfer(ctx, res)
}

// reportFileXfer writes the result frame. ctx is the PROCESS ctx, never the
// transfer's deadline: a transfer that timed out still owes the hub an answer, and
// writeFrame fails cleanly when the connection is down (the hub then fails the record
// on its own timeout or disconnect path).
func (cl *Client) reportFileXfer(ctx context.Context, res wsproto.FileXferResult) {
	if err := cl.writeFrame(ctx, wsproto.TypeFileXferResult, "", res); err != nil {
		slog.Warn("xfer.report_failed", "event", "xfer.report_failed", "component", "worker",
			"worker_id", cl.workerID, "xfer_id", res.XferID, "err", err)
	}
}

// runFileXfer resolves the transfer's target on THIS machine and moves the bytes. It
// returns the size + sha256 the payload ended up with (what the result frame reports).
func (cl *Client) runFileXfer(ctx context.Context, sessionURL string, fr wsproto.FileXfer) (int64, string, error) {
	root, err := cl.xferProjectRoot(fr.ProjectKey)
	if err != nil {
		return 0, "", err
	}
	// SafeJoin is the SAME boundary a local job's --cwd obeys (review #8: the worker
	// re-validates with its own config): a path that escapes the project root is
	// refused here, before this machine's filesystem is touched at all.
	target, err := project.SafeJoin(root, fr.Path)
	if err != nil {
		return 0, "", fmt.Errorf("path escapes project: %w", err)
	}
	base, err := xferHTTPBase(sessionURL)
	if err != nil {
		return 0, "", err
	}
	contentURL, err := xferContentURL(base, fr.URLPath)
	if err != nil {
		return 0, "", err
	}
	switch fr.Op {
	case "put":
		return cl.xferFetch(ctx, contentURL, fr, target)
	case "get":
		return cl.xferSend(ctx, contentURL, target)
	default:
		return 0, "", fmt.Errorf("unknown transfer op %q", fr.Op)
	}
}

// xferProjectRoot resolves a project key against the worker's OWN config (re-read per
// call, so a reloaded config is honoured) and returns the execution root a transfer
// under it must stay inside.
func (cl *Client) xferProjectRoot(key string) (string, error) {
	cfg := cl.jobs.Config()
	if cfg == nil {
		return "", errors.New("worker has no config")
	}
	proj, ok := cfg.Projects[key]
	if !ok {
		return "", fmt.Errorf("unknown project %q", key)
	}
	root := cfg.ExecPath(proj)
	if root == "" {
		return "", fmt.Errorf("project %q has no execution path", key)
	}
	return root, nil
}

// xferFetch runs op=put: download the staged payload and put it at dst.
func (cl *Client) xferFetch(ctx context.Context, contentURL string, fr wsproto.FileXfer, dst string) (int64, string, error) {
	if !fr.Force {
		if _, err := os.Stat(dst); err == nil {
			// Refuse BEFORE downloading: the answer is the same either way, and
			// streaming 256MB only to throw it away is pure waste.
			return 0, "", errXferExists
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, contentURL, nil)
	if err != nil {
		return 0, "", err
	}
	cl.xferAuth(req)
	resp, err := xferHTTP.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("fetch payload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("fetch payload: server answered %s", resp.Status)
	}
	if fr.Size > 0 && resp.ContentLength >= 0 && resp.ContentLength != fr.Size {
		return 0, "", fmt.Errorf("size mismatch: server announced %d bytes, expected %d", resp.ContentLength, fr.Size)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return 0, "", fmt.Errorf("create destination directory: %w", err)
	}
	// Write NEXT TO the destination (same filesystem, so the rename is atomic) under a
	// transfer-scoped temp name, and only then rename: a half-written payload must
	// never be visible where the project's tools read it.
	tmp := dst + ".gofer-tmp-" + fr.XferID
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, "", fmt.Errorf("open temp file: %w", err)
	}
	h := sha256.New()
	n, cerr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if cerr != nil || closeErr != nil {
		reason := cerr
		if reason == nil {
			reason = closeErr
		}
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("write payload: %w", reason)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if fr.Size > 0 && n != fr.Size {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("size mismatch: received %d bytes, expected %d", n, fr.Size)
	}
	// A payload's digest is what proves the bytes arrived intact. The requester only
	// knows it when it staged the transfer itself; otherwise the SERVER announces it
	// on the response (its staging area settled the payload with that digest), and an
	// announced digest is always enforced.
	want := fr.SHA256
	if want == "" {
		want = resp.Header.Get("X-Gofer-Sha256")
	}
	if want != "" && !strings.EqualFold(want, sum) {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("sha256 mismatch: received %s, expected %s", sum, want)
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return 0, "", fmt.Errorf("move payload into place: %w", err)
	}
	return n, sum, nil
}

// xferSend runs op=get: hash the source file and upload it to the staging area.
func (cl *Client) xferSend(ctx context.Context, contentURL, src string) (int64, string, error) {
	f, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", errXferSourceNotFound
		}
		return 0, "", fmt.Errorf("open source: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, "", fmt.Errorf("stat source: %w", err)
	}
	if st.IsDir() {
		return 0, "", errors.New("source is a directory; XFER-01 transfers a single file")
	}
	// Hash the file FIRST: the server verifies the declared digest while it receives
	// the body, so the value has to exist before the body starts streaming (the
	// alternative — trusting the server to hash blind — is exactly the check the
	// design puts on the receiving side, §一.1).
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return 0, "", fmt.Errorf("hash source: %w", err)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, "", fmt.Errorf("rewind source: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, contentURL, f)
	if err != nil {
		return 0, "", err
	}
	req.ContentLength = st.Size()
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("X-Gofer-Size", strconv.FormatInt(st.Size(), 10))
	req.Header.Set("X-Gofer-Sha256", sum)
	cl.xferAuth(req)
	resp, err := xferHTTP.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("upload payload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The server's reason (sha256 mismatch / too large / not your transfer) is the
		// actionable part, so it travels into the result frame verbatim.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return 0, "", fmt.Errorf("upload payload: server answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return st.Size(), sum, nil
}

// xferAuth presents the worker's own hub token: the content endpoints authenticate the
// SAME caller the connection registered as, which is what confines a transfer id to
// the worker it was assigned to.
func (cl *Client) xferAuth(req *http.Request) {
	if cl.token != "" {
		req.Header.Set("Authorization", "Bearer "+cl.token)
	}
}

// effectiveXferTimeout returns this worker's single-transfer deadline: the
// operator-configured worker.xfer_timeout_sec when one was resolved, else the
// documented 10 minutes.
func (cl *Client) effectiveXferTimeout() time.Duration {
	if cl.xferTimeout > 0 {
		return cl.xferTimeout
	}
	return time.Duration(config.DefaultWorkerXferTimeoutSec) * time.Second
}

// SetXferTimeout overrides the single-transfer deadline. worker.Serve calls it from the
// worker's own config before any frame can arrive (the hub cannot know this machine's
// budget); <= 0 keeps the default.
func (cl *Client) SetXferTimeout(d time.Duration) {
	if d > 0 {
		cl.xferTimeout = d
	}
}

// xferHTTPBase derives the HTTP base of the hub this session is connected to from ITS
// ws URL (the same one it registered on): one origin, so there is no second address to
// configure — or to get wrong.
func xferHTTPBase(sessionURL string) (string, error) {
	u, err := url.Parse(sessionURL)
	if err != nil {
		return "", fmt.Errorf("parse hub url %q: %w", sessionURL, err)
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	default:
		return "", fmt.Errorf("hub url %q has unsupported scheme %q", sessionURL, u.Scheme)
	}
	u.Path, u.RawPath, u.RawQuery, u.Fragment, u.User = "", "", "", "", nil
	return strings.TrimSuffix(u.String(), "/"), nil
}

// xferContentURL joins the instruction's server-relative content path onto the base.
// The path is used verbatim (the hub minted it) but MUST be absolute: a relative one
// would silently resolve against a different prefix.
func xferContentURL(base, path string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("bad content path %q", path)
	}
	return base + path, nil
}
