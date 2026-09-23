package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/inhere/gofer/internal/skill"
)

// skills.go is the JOB-10 client for /v1/skills* (design §一): the library's list,
// show, import, update, remove and export. The library itself lives on the server
// next to its config; a client node reaches it through these six calls, which is
// what makes `gofer agent skill …` work identically over HTTP and against a local
// config (the dual mode the CLI switches on).

// SkillView is one skill as GET /v1/skills/{name} returns it: the index metadata
// plus the SKILL.md text, so `show` needs one request rather than two.
type SkillView struct {
	skill.Skill
	// Content is the skill's SKILL.md verbatim (frontmatter included). Skipped by
	// the listing endpoint, which is why it is a separate type from Skill.
	Content string `json:"content,omitempty"`
}

// SkillImportResult is the answer to POST /v1/skills/import: the imported skill and
// whether it replaced a library entry of the same name (import is re-runnable, so
// "replaced" is information, not an error).
type SkillImportResult struct {
	Skill    skill.Skill `json:"skill"`
	Replaced bool        `json:"replaced,omitempty"`
}

// SkillUpdateResult is the answer to POST /v1/skills/{name}/update: the re-fetched
// metadata and the file-level diff the update produced.
type SkillUpdateResult struct {
	Skill  skill.Skill  `json:"skill"`
	Change skill.Change `json:"change"`
}

// SkillList lists the library (GET /v1/skills). The server always answers with an
// array, but a null body is normalised to an empty slice so a caller may range and
// len() without a nil check.
func (c *Client) SkillList() ([]skill.Skill, error) {
	var out struct {
		Skills []skill.Skill `json:"skills"`
	}
	if err := c.doJSON(http.MethodGet, "/v1/skills", nil, &out); err != nil {
		return nil, err
	}
	if out.Skills == nil {
		out.Skills = []skill.Skill{}
	}
	return out.Skills, nil
}

// SkillShow fetches one skill together with its SKILL.md text
// (GET /v1/skills/{name}).
func (c *Client) SkillShow(name string) (SkillView, error) {
	var out SkillView
	err := c.doJSON(http.MethodGet, "/v1/skills/"+url.PathEscape(name), nil, &out)
	return out, err
}

// SkillImportFile uploads a local .zip as the multipart `file` part
// (POST /v1/skills/import). The CLI zips a local DIRECTORY into a temp .zip first:
// the wire carries an archive, never a client-side path, so the server never has to
// reach back into this machine's filesystem.
func (c *Client) SkillImportFile(path string) (SkillImportResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return SkillImportResult{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	// A skill is capped (2MiB/file, 10MiB total by default), so buffering the
	// archive costs nothing and keeps the request a single, retry-safe body.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return SkillImportResult{}, fmt.Errorf("build multipart: %w", err)
	}
	if _, err := io.Copy(fw, f); err != nil {
		return SkillImportResult{}, fmt.Errorf("read %s: %w", path, err)
	}
	if err := mw.Close(); err != nil {
		return SkillImportResult{}, fmt.Errorf("close multipart: %w", err)
	}

	req, err := c.xferRequest(context.Background(), http.MethodPost, "/v1/skills/import", &body, mw.FormDataContentType())
	if err != nil {
		return SkillImportResult{}, err
	}
	var out SkillImportResult
	if err := doXferJSON(req, &out); err != nil {
		return SkillImportResult{}, err
	}
	return out, nil
}

// SkillImportSource imports from a spec the SERVER resolves itself
// (`http(s)://…/x.zip`, `git+https://…#subdir`) via POST /v1/skills/import with a
// JSON body. Those sources are fetched server-side on purpose: a client node has no
// reason to download an archive it would only hand straight back.
func (c *Client) SkillImportSource(src string) (SkillImportResult, error) {
	payload, err := json.Marshal(map[string]string{"source": src})
	if err != nil {
		return SkillImportResult{}, fmt.Errorf("encode source: %w", err)
	}
	var out SkillImportResult
	if err := c.doJSON(http.MethodPost, "/v1/skills/import", bytes.NewReader(payload), &out); err != nil {
		return SkillImportResult{}, err
	}
	return out, nil
}

// SkillUpdate re-fetches a skill from its recorded source
// (POST /v1/skills/{name}/update).
func (c *Client) SkillUpdate(name string) (SkillUpdateResult, error) {
	var out SkillUpdateResult
	err := c.doJSON(http.MethodPost, "/v1/skills/"+url.PathEscape(name)+"/update", nil, &out)
	return out, err
}

// SkillRemove deletes a skill (DELETE /v1/skills/{name}, 204).
func (c *Client) SkillRemove(name string) error {
	return c.doJSON(http.MethodDelete, "/v1/skills/"+url.PathEscape(name), nil, nil)
}

// SkillExport streams a skill's archive (GET /v1/skills/{name}/export) into w.
//
// It streams rather than buffering for the same reason XferDownload does — the
// payload is a file the caller owns — and w is written as given: the caller owns
// the temp-name + rename policy, so a failed stream never leaves a usable-looking
// .zip behind.
func (c *Client) SkillExport(name string, w io.Writer) error {
	req, err := c.xferRequest(context.Background(), http.MethodGet, "/v1/skills/"+url.PathEscape(name)+"/export", nil, "")
	if err != nil {
		return err
	}
	resp, err := xferClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errorFor(resp.StatusCode, data)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	return nil
}
