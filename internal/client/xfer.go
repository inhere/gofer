package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// xfer.go is the XFER-01 client for /v1/xfer* (design §一.2). A transfer is a
// staged file with a runner (a worker id, or "local" for the server host), a
// project and a project-relative path: a PUSH uploads our bytes into the staging
// area and lets the runner write them into its project root, a PULL asks the
// runner to read a file out of its project root and stages it for us to
// download. The payload always rides HTTP — the control websocket only carries
// the instruction — so everything below is an ordinary HTTP call.

// XferRecord is one transfer journal row (GET /v1/xfer/{id}). Its JSON tags are
// the wire keys the server's xferView uses: `project` names the project (not
// project_key) and every timestamp is unix SECONDS.
type XferRecord struct {
	ID         string `json:"id"`
	Op         string `json:"op"`
	Runner     string `json:"runner"`
	Project    string `json:"project"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	State      string `json:"state"`
	Error      string `json:"error,omitempty"`
	CallerID   string `json:"caller_id,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
}

// xferMeta is the JSON `meta` field of the create request. A push sends it as the
// first field of a multipart body (the payload follows), a pull sends the same
// object as the whole JSON body. sha256/size are omitted when unknown (a pull).
type xferMeta struct {
	Op      string `json:"op"`
	Runner  string `json:"runner"`
	Project string `json:"project"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Force   bool   `json:"force,omitempty"`
}

// xferCreateResp is the create answer: the server only tells us the id and the
// initial state — the rest of the record is the meta we just sent.
type xferCreateResp struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

// xferChunkSize is the streaming granularity: 1MB blocks, so progress can be
// reported per block and neither side ever holds the file in memory.
const xferChunkSize = 1 << 20

// Transfer operations: a put uploads bytes into the staging area for the runner
// to write, a get asks the runner to read a file out of its project root.
const (
	XferOpPut = "put"
	XferOpGet = "get"
)

// Transfer states as the wire reports them (server side: internal/xfer). They are
// re-exported so a caller can poll without depending on the server's own package.
const (
	XferStateStaged     = "staged"
	XferStateDispatched = "dispatched"
	XferStateDone       = "done"
	XferStateFailed     = "failed"
	XferStateExpired    = "expired"
)

// xferHTTPTimeout caps one transfer end-to-end. The control-plane client caps
// every call at 30s, which is far too short for a multi-GB upload, so transfers
// ride a DEDICATED client whose timeout is the transfer's own budget; a caller
// with a tighter deadline (the CLI's `--timeout`) bounds it further through ctx.
const xferHTTPTimeout = 30 * time.Minute

// xferClient is the dedicated client file transfers ride on. It is shared so
// sequential transfers reuse the connection pool.
var xferClient = &http.Client{Timeout: xferHTTPTimeout}

// xferRequest builds a transfer request: bearer-authenticated, bound to ctx, with
// an explicit content type ("" = none, unlike do's JSON default — a multipart
// upload must not claim to be JSON).
func (c *Client) xferRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

// doXferJSON sends req and decodes the 2xx JSON answer into out (nil to discard
// it). A non-2xx answer becomes the same friendly error doJSON builds, so the
// server's {error,detail} text reaches the caller verbatim.
func doXferJSON(req *http.Request, out any) error {
	resp, err := xferClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if err := errorFor(resp.StatusCode, data); err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// XferPut pushes a local file to runner:project/path (POST /v1/xfer,
// multipart/form-data). The file is stat'ed and hashed first — the server
// verifies the DECLARED digest against the streamed one, so the meta field has to
// carry the real sha256 — then streamed in 1MB chunks, which is what lets
// progress fire and keeps the bytes off the heap. progress, when set, is called
// after every chunk with the bytes sent so far and the total.
//
// force allows replacing a file that already exists at the target (without it a
// push onto an existing path fails with the server's own "exists").
//
// The returned record is the meta we sent plus the created id and state: the
// create answer carries nothing else.
func (c *Client) XferPut(ctx context.Context, runner, project, path, localPath string, force bool, progress func(sent, total int64)) (XferRecord, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return XferRecord{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return XferRecord{}, err
	}
	if st.IsDir() {
		return XferRecord{}, fmt.Errorf("%s is a directory: v1 transfers one file (pack it with tar / Compress-Archive first)", localPath)
	}

	// Pass 1: the digest must be known before the meta field is written.
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return XferRecord{}, fmt.Errorf("hash %s: %w", localPath, err)
	}
	digest := hex.EncodeToString(sum.Sum(nil))
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return XferRecord{}, fmt.Errorf("rewind %s: %w", localPath, err)
	}

	meta := xferMeta{Op: XferOpPut, Runner: runner, Project: project, Path: path, SHA256: digest, Size: st.Size(), Force: force}
	rec := XferRecord{
		Op: XferOpPut, Runner: runner, Project: project, Path: path,
		Size: st.Size(), SHA256: digest, State: XferStateStaged,
	}

	pr, pw := io.Pipe()
	// Closing the reader unblocks the writer goroutine when the request ends
	// early (a 4xx answer mid-stream), so it can never be left blocked on a pipe.
	defer pr.Close()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeXferMultipart(mw, f, filepath.Base(localPath), meta, digest, progress))
	}()

	req, err := c.xferRequest(ctx, http.MethodPost, "/v1/xfer", pr, mw.FormDataContentType())
	if err != nil {
		return XferRecord{}, err
	}
	var created xferCreateResp
	if err := doXferJSON(req, &created); err != nil {
		return XferRecord{}, err
	}
	rec.ID, rec.State = created.ID, created.State
	return rec, nil
}

// writeXferMultipart writes the `meta` field and then the `file` part, streaming
// the file in xferChunkSize blocks. The digest is RECOMPUTED while streaming: the
// meta field already declared one, and a file that changed between the hashing
// pass and this one must fail the transfer rather than be staged under a digest
// that does not describe it.
func writeXferMultipart(mw *multipart.Writer, f *os.File, name string, meta xferMeta, declared string, progress func(sent, total int64)) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("encode meta: %w", err)
	}
	// The meta field MUST come first: the server reads it before it will accept
	// the payload (it validates the target and the size cap off it).
	if err := mw.WriteField("meta", string(raw)); err != nil {
		return fmt.Errorf("write meta: %w", err)
	}
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return fmt.Errorf("write file part: %w", err)
	}
	sum := sha256.New()
	buf := make([]byte, xferChunkSize)
	var sent int64
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			if _, werr := io.MultiWriter(part, sum).Write(buf[:n]); werr != nil {
				return fmt.Errorf("stream %s: %w", name, werr)
			}
			sent += int64(n)
			if progress != nil {
				progress(sent, meta.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("read %s: %w", name, rerr)
		}
	}
	if got := hex.EncodeToString(sum.Sum(nil)); !strings.EqualFold(got, declared) {
		return fmt.Errorf("%s changed while it was uploaded (sha256 %s -> %s)", name, declared, got)
	}
	return mw.Close()
}

// XferGet asks a runner to read project/path and stage it for us (POST /v1/xfer
// with a JSON meta, no payload). The returned record is dispatched already: poll
// XferStatus until it is done, then XferDownload the content.
func (c *Client) XferGet(ctx context.Context, runner, project, path string) (XferRecord, error) {
	raw, err := json.Marshal(xferMeta{Op: XferOpGet, Runner: runner, Project: project, Path: path})
	if err != nil {
		return XferRecord{}, fmt.Errorf("encode meta: %w", err)
	}
	req, err := c.xferRequest(ctx, http.MethodPost, "/v1/xfer", bytes.NewReader(raw), "application/json")
	if err != nil {
		return XferRecord{}, err
	}
	var created xferCreateResp
	if err := doXferJSON(req, &created); err != nil {
		return XferRecord{}, err
	}
	return XferRecord{
		ID: created.ID, Op: XferOpGet, Runner: runner, Project: project, Path: path,
		State: created.State,
	}, nil
}

// XferStatus fetches one transfer's current record (GET /v1/xfer/{id}).
func (c *Client) XferStatus(id string) (XferRecord, error) {
	var rec XferRecord
	if err := c.doJSON(http.MethodGet, "/v1/xfer/"+url.PathEscape(id), nil, &rec); err != nil {
		return XferRecord{}, err
	}
	return rec, nil
}

// XferList lists the staging area (GET /v1/xfer?state=&runner=), newest first as
// the server orders it. Empty filters are omitted.
func (c *Client) XferList(state, runner string) ([]XferRecord, error) {
	q := url.Values{}
	if state != "" {
		q.Set("state", state)
	}
	if runner != "" {
		q.Set("runner", runner)
	}
	path := "/v1/xfer"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var resp struct {
		Xfers []XferRecord `json:"xfers"`
	}
	if err := c.doJSON(http.MethodGet, path, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Xfers, nil
}

// XferRemove deletes a transfer and its staged payload (DELETE /v1/xfer/{id}).
func (c *Client) XferRemove(id string) error {
	return c.doJSON(http.MethodDelete, "/v1/xfer/"+url.PathEscape(id), nil, nil)
}

// XferDownload streams a transfer's payload into destPath (GET
// /v1/xfer/{id}/content) and returns the bytes written plus their sha256.
//
// The bytes are verified twice on the way in: the declared size against
// X-Gofer-Size, and the streaming digest against X-Gofer-Sha256. Anything that
// does not match — or a write error — REMOVES the file, so a caller never sees a
// truncated or corrupted payload it might mistake for the real one.
//
// destPath is written as given: the caller owns the temp-name + rename policy
// (the CLI downloads to `<dst>.gofer-part` and renames), because only the caller
// knows what "never leave a partial file" means for its destination.
func (c *Client) XferDownload(ctx context.Context, id, destPath string) (int64, string, error) {
	req, err := c.xferRequest(ctx, http.MethodGet, "/v1/xfer/"+url.PathEscape(id)+"/content", nil, "")
	if err != nil {
		return 0, "", err
	}
	resp, err := xferClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("request GET /v1/xfer/%s/content: %w", id, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err := errorFor(resp.StatusCode, data); err != nil {
			return 0, "", err
		}
		return 0, "", fmt.Errorf("download %s: unexpected status %d", id, resp.StatusCode)
	}

	f, err := os.Create(destPath)
	if err != nil {
		return 0, "", err
	}
	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, sum), resp.Body)
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destPath)
		if copyErr != nil {
			return 0, "", fmt.Errorf("download %s: %w", id, copyErr)
		}
		return 0, "", fmt.Errorf("download %s: %w", id, closeErr)
	}
	digest := hex.EncodeToString(sum.Sum(nil))

	if want := resp.Header.Get("X-Gofer-Size"); want != "" {
		if wantN, perr := strconv.ParseInt(want, 10, 64); perr == nil && wantN != n {
			_ = os.Remove(destPath)
			return 0, "", fmt.Errorf("size mismatch: the server staged %d bytes, the download has %d", wantN, n)
		}
	}
	if want := resp.Header.Get("X-Gofer-Sha256"); want != "" && !strings.EqualFold(want, digest) {
		_ = os.Remove(destPath)
		return 0, "", fmt.Errorf("sha256 mismatch: the server staged %s, the download hashed %s", want, digest)
	}
	return n, digest, nil
}
