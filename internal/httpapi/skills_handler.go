package httpapi

// skills_handler.go is the JOB-10 skill-library HTTP surface (design §一): the entry
// layer's read/write operations over skill.Store, which serve injects via SetSkills.
// Handlers only resolve params, enforce can_admin on the three writes and encode
// responses — the store owns the name grammar, the staging pipeline and the byte caps
// (G021: this layer validates the REQUEST, never re-implements the operation).
//
// The routes are always mounted and answer 503 while no library is wired (mcp / most
// tests), the same degradation /v1/xfer and /v1/config use, so no router rebuild is
// needed for the seam. Reads are open to any authenticated caller; import, update and
// delete are can_admin-gated, because the library is a server-side asset every job on
// the machine may be handed.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/skill"
)

// skillManifest is the file every skill must carry at its root (design §一.1): its
// frontmatter holds name/description and its body is the text GET /v1/skills/{name}
// returns alongside the index entry.
const skillManifest = "SKILL.md"

// skillUploadPattern names the temp file a multipart import streams into. The .zip
// suffix is not cosmetic: skill.Store.Import classifies a local source by extension
// and refuses anything else as "not a .zip file".
const skillUploadPattern = "gofer-skill-*.zip"

// maxSkillJSONBody bounds the JSON import body, which carries one spec string — small
// even for a git+https URL with a #subdir.
const maxSkillJSONBody = 64 << 10

// skillsListResp is GET /v1/skills: always an array (never null) so a console can map
// over it unconditionally.
type skillsListResp struct {
	Skills []skill.Skill `json:"skills"`
}

// skillDetailResp is GET /v1/skills/{name}: the index entry plus the SKILL.md text,
// which is what the operator actually wants to read.
type skillDetailResp struct {
	skill.Skill
	Content string `json:"content"`
}

// skillImportResp is the 201 answer: the indexed skill plus whether the import
// REPLACED an existing entry of the same name (import is re-runnable by design).
type skillImportResp struct {
	Skill    skill.Skill `json:"skill"`
	Replaced bool        `json:"replaced"`
}

// skillChangeView is the wire form of skill.Change. The store's own type carries no
// json tags, so a raw payload would go out with capitalized Go field names while every
// other /v1 body is lower-case; the four fields are the store's, nothing more. A client
// that decodes into skill.Change is unaffected — json field matching is
// case-insensitive — and the diff lists are normalized to [] rather than null, because
// "nothing changed in this category" is an empty list to render, not an absent one.
type skillChangeView struct {
	Name    string   `json:"name"`
	Added   []string `json:"added"`
	Changed []string `json:"changed"`
	Removed []string `json:"removed"`
}

func skillChangeJSON(ch skill.Change) skillChangeView {
	return skillChangeView{
		Name:    ch.Name,
		Added:   orEmpty(ch.Added),
		Changed: orEmpty(ch.Changed),
		Removed: orEmpty(ch.Removed),
	}
}

// orEmpty returns paths itself, or an allocated empty slice — a non-nil []string so the
// field marshals as [] (see skillChangeView).
func orEmpty(paths []string) []string {
	if paths == nil {
		return []string{}
	}
	return paths
}

// skillUpdateResp is the answer of a re-fetch: the new entry plus the file-level diff
// against the version it replaced, so a console can show what upstream changed.
type skillUpdateResp struct {
	Skill  skill.Skill     `json:"skill"`
	Change skillChangeView `json:"change"`
}

// skillsUnavailable answers 503 when no skill library is wired (mcp / most tests):
// the routes are always mounted so the surface is uniform, exactly like /v1/xfer.
func (s *Server) skillsUnavailable(c *rux.Context) bool {
	if s.skills == nil {
		writeError(c, http.StatusServiceUnavailable, "skills unavailable",
			"this server has no skill library wired; start it with `gofer serve`")
		return true
	}
	return false
}

// skillsWriteCaller runs the can_admin gate the three skill writes share — the same
// capability bit and the same 403 wording the project and config write routes use, so
// "no permission" is one condition across the product.
func (s *Server) skillsWriteCaller(c *rux.Context) (string, bool) {
	caller := callerFromCtx(c)
	if !s.callerMayAdmin(caller) {
		writeError(c, http.StatusForbidden, "admin not permitted for this caller", "caller lacks can_admin capability")
		return "", false
	}
	return caller, true
}

// writeSkillError maps a skill-store failure onto the HTTP contract: a missing skill
// is a 404, every refusal the store NAMES (bad name, path escape, symlink, over a cap)
// is the caller's 400, and anything else is this server's 500. summary is the short
// phrase for the operation that failed.
func writeSkillError(c *rux.Context, err error, summary string) {
	switch {
	case errors.Is(err, skill.ErrNotFound):
		writeError(c, http.StatusNotFound, "unknown skill", err.Error())
	case errors.Is(err, skill.ErrInvalid), errors.Is(err, skill.ErrEscape),
		errors.Is(err, skill.ErrSymlink), errors.Is(err, skill.ErrTooLarge):
		writeError(c, http.StatusBadRequest, summary, err.Error())
	default:
		writeError(c, http.StatusInternalServerError, summary, err.Error())
	}
}

// skillMaxTotalBytes is the per-skill byte cap (server.skill_limits.max_total_bytes),
// with skill.DefaultLimits as the shipped default. It is read from the live server
// config on every request, so a hot-reloaded cap applies to the next import (0 reads
// as "unset", the way every other cap in gofer's config behaves).
func (s *Server) skillMaxTotalBytes() int64 {
	if s.cfg != nil && s.cfg.SkillLimits.MaxTotalBytes > 0 {
		return s.cfg.SkillLimits.MaxTotalBytes
	}
	return skill.DefaultLimits().MaxTotalBytes
}

// skillNameIndex returns the names the library currently holds, keyed for lookup.
func (s *Server) skillNameIndex() (map[string]bool, error) {
	list, err := s.skills.List()
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool, len(list))
	for _, sk := range list {
		names[sk.Name] = true
	}
	return names, nil
}

// handleListSkills serves GET /v1/skills: the whole library, index entries only (the
// files' bytes stay on disk — a caller that wants them reads one skill or exports it).
func (s *Server) handleListSkills(c *rux.Context) {
	if s.skillsUnavailable(c) {
		return
	}
	list, err := s.skills.List()
	if err != nil {
		writeSkillError(c, err, "list skills failed")
		return
	}
	// Non-nil so the wire shape is an empty array, never null.
	out := make([]skill.Skill, 0, len(list))
	out = append(out, list...)
	c.JSON(http.StatusOK, skillsListResp{Skills: out})
}

// handleGetSkill serves GET /v1/skills/{name}: the index entry plus the SKILL.md body.
func (s *Server) handleGetSkill(c *rux.Context) {
	if s.skillsUnavailable(c) {
		return
	}
	name := c.Param("name")
	sk, ok, err := s.skills.Get(name)
	if err != nil {
		writeSkillError(c, err, "read skill failed")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown skill", "no skill named "+name)
		return
	}
	// Get validated the name, so Dir is never "" here; the guard keeps a future
	// refactor from turning an unvalidated name into a read of the process's cwd.
	dir := s.skills.Dir(name)
	if dir == "" {
		writeError(c, http.StatusBadRequest, "invalid skill name", "not a valid skill name: "+name)
		return
	}
	// The index and the tree are written together (skill.Store.publish), so an
	// unreadable manifest here is a broken library, not a missing skill: report it as
	// this server's failure instead of pretending the skill is unknown.
	content, err := os.ReadFile(filepath.Join(dir, skillManifest))
	if err != nil {
		writeError(c, http.StatusInternalServerError, "read skill failed",
			"skill "+name+" has no readable "+skillManifest+": "+err.Error())
		return
	}
	c.JSON(http.StatusOK, skillDetailResp{Skill: sk, Content: string(content)})
}

// handleImportSkill serves POST /v1/skills/import — the admin-only entry that copies a
// source INTO the library. Two bodies are accepted:
//
//	application/json      {"source":"<dir|zip|http(s)://…/x.zip|git+https://…#subdir>"}
//	multipart/form-data    the zip itself in part `file` (any filename)
//
// The JSON spec is resolved server-side by the store (that is where the URL/git fetch
// lives), the uploaded zip is streamed to a temp file first — the store's import
// pipeline reads a path, and the uploaded name is deliberately NOT trusted as one.
func (s *Server) handleImportSkill(c *rux.Context) {
	if s.skillsUnavailable(c) {
		return
	}
	caller, ok := s.skillsWriteCaller(c)
	if !ok {
		return
	}

	spec, cleanup, status, err := s.skillImportSpec(c)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		writeError(c, status, "invalid import source", err.Error())
		return
	}

	// `replaced` comes from the index read BEFORE the import: the import itself is
	// atomic (skill.Store.publish) but does not report whether it overwrote an entry,
	// and the skill's name is only known once the source has been read. A concurrent
	// admin import of the same name is the only way this can mis-report, and the flag
	// is advisory — the console's "overwritten" hint.
	before, err := s.skillNameIndex()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "import skill failed", err.Error())
		return
	}
	sk, err := s.skills.Import(spec, caller)
	if err != nil {
		writeSkillError(c, err, "import skill failed")
		return
	}
	c.JSON(http.StatusCreated, skillImportResp{Skill: sk, Replaced: before[sk.Name]})
}

// skillImportSpec resolves the request body into the spec Store.Import takes, plus a
// cleanup for whatever it had to materialise. status is 0 unless err is set.
func (s *Server) skillImportSpec(c *rux.Context) (spec string, cleanup func(), status int, err error) {
	if strings.HasPrefix(c.Req.Header.Get("Content-Type"), "multipart/form-data") {
		return s.skillUploadSpec(c)
	}
	raw, err := io.ReadAll(io.LimitReader(c.Req.Body, maxSkillJSONBody))
	if err != nil {
		return "", nil, http.StatusBadRequest, err
	}
	var req struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return "", nil, http.StatusBadRequest,
			fmt.Errorf(`body must be {"source":"<dir|zip|url|git+https>"} or multipart/form-data: %w`, err)
	}
	spec = strings.TrimSpace(req.Source)
	if spec == "" {
		return "", nil, http.StatusBadRequest, errors.New("source is required")
	}
	return spec, nil, 0, nil
}

// skillUploadSpec streams the multipart `file` part into a temp .zip and returns its
// path as the import spec. The upload is bounded at the configured per-skill cap while
// it is being read (one byte past the cap, so an over-limit upload is DETECTED rather
// than silently truncated); the store's own caps still run over the decompressed
// bytes, since a zip's size says nothing about what it inflates to.
func (s *Server) skillUploadSpec(c *rux.Context) (string, func(), int, error) {
	mr, err := c.Req.MultipartReader()
	if err != nil {
		return "", nil, http.StatusBadRequest, err
	}
	for {
		part, perr := mr.NextPart()
		if errors.Is(perr, io.EOF) {
			break
		}
		if perr != nil {
			return "", nil, http.StatusBadRequest, perr
		}
		if part.FormName() != "file" {
			continue // another form field; NextPart discards the rest of it
		}
		f, cerr := os.CreateTemp("", skillUploadPattern)
		if cerr != nil {
			return "", nil, http.StatusInternalServerError, cerr
		}
		// Removed on EVERY path (returned to the caller, which defers it): the temp
		// dir is where an import's bytes live, and a refused import must leave nothing.
		cleanup := func() { _ = os.Remove(f.Name()) }

		maxBytes := s.skillMaxTotalBytes()
		n, cerr := io.Copy(f, io.LimitReader(part, maxBytes+1))
		closeErr := f.Close()
		if cerr != nil {
			return "", cleanup, http.StatusInternalServerError, cerr
		}
		if closeErr != nil {
			return "", cleanup, http.StatusInternalServerError, closeErr
		}
		if n > maxBytes {
			return "", cleanup, http.StatusBadRequest,
				fmt.Errorf("upload exceeds max_total_bytes (%d)", maxBytes)
		}
		return f.Name(), cleanup, 0, nil
	}
	return "", nil, http.StatusBadRequest, errors.New("the multipart body needs a zip in a `file` part")
}

// handleUpdateSkill serves POST /v1/skills/{name}/update: re-fetch the skill from the
// source its index entry recorded (design §一.2) and answer the file-level diff.
func (s *Server) handleUpdateSkill(c *rux.Context) {
	if s.skillsUnavailable(c) {
		return
	}
	caller, ok := s.skillsWriteCaller(c)
	if !ok {
		return
	}
	name := c.Param("name")
	change, err := s.skills.Update(name, caller)
	if err != nil {
		writeSkillError(c, err, "update skill failed")
		return
	}
	sk, ok, err := s.skills.Get(name)
	if err != nil {
		writeSkillError(c, err, "update skill failed")
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "unknown skill", "no skill named "+name)
		return
	}
	c.JSON(http.StatusOK, skillUpdateResp{Skill: sk, Change: skillChangeJSON(change)})
}

// handleDeleteSkill serves DELETE /v1/skills/{name}: 204 with no body.
func (s *Server) handleDeleteSkill(c *rux.Context) {
	if s.skillsUnavailable(c) {
		return
	}
	if _, ok := s.skillsWriteCaller(c); !ok {
		return
	}
	if err := s.skills.Remove(c.Param("name")); err != nil {
		writeSkillError(c, err, "remove skill failed")
		return
	}
	c.Resp.WriteHeader(http.StatusNoContent)
}

// handleExportSkill serves GET /v1/skills/{name}/export: the skill as a zip of its
// files under their relative paths, so an export re-imports to the same tree.
func (s *Server) handleExportSkill(c *rux.Context) {
	if s.skillsUnavailable(c) {
		return
	}
	name := c.Param("name")
	// Read the entry FIRST: Export streams into the response body, so an unknown
	// skill has to be answered while the status is still ours to choose.
	if _, ok, err := s.skills.Get(name); err != nil {
		writeSkillError(c, err, "export skill failed")
		return
	} else if !ok {
		writeError(c, http.StatusNotFound, "unknown skill", "no skill named "+name)
		return
	}
	c.SetHeader("Content-Type", "application/zip")
	c.SetHeader("Content-Disposition", "attachment; filename="+strconv.Quote(name+".zip"))
	c.Resp.WriteHeader(http.StatusOK)
	if err := s.skills.Export(name, c.Resp); err != nil {
		// The status is out: a walk error mid-stream can only be a truncated archive,
		// which the caller discards (skill.Store.Export's contract). Log it so the
		// failure is not invisible.
		slog.Error("skill export failed mid-stream", "skill", name, "error", err)
	}
}
