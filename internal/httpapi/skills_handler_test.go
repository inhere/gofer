package httpapi

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/job/workflow"
	"github.com/inhere/gofer/internal/project"
	"github.com/inhere/gofer/internal/runner"
	localrunner "github.com/inhere/gofer/internal/runner/local"
	"github.com/inhere/gofer/internal/skill"
)

// newSkillsTestServer wires a Server over a REAL skill.Store — a temp library root
// plus the jobstore's skills table, exactly the pair core.Build assembles — and hands
// it back so a test can seed the library through the store and then assert what the
// HTTP surface did to it.
func newSkillsTestServer(t *testing.T, cfg *config.Config) (*Server, *skill.Store) {
	t.Helper()
	root := t.TempDir()
	if cfg.Storage.Root == "" {
		cfg.Storage.Root = filepath.Join(root, "store")
	}
	if cfg.Projects == nil {
		cfg.Projects = map[string]config.ProjectConfig{}
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]config.AgentConfig{}
	}
	if cfg.Runners == nil {
		cfg.Runners = map[string]config.RunnerConfig{}
	}
	projects := project.NewRegistry(cfg, filepath.Join(root, "config.yaml"))
	agents := agent.NewRegistry(cfg)
	runners := map[string]runner.Runner{localrunner.Name: localrunner.New()}
	st := openTestStore(t, root)
	jobs := drainOnCleanup(t, job.NewService(cfg, projects, agents, runners, st, nil))
	eng := workflow.NewEngine(jobs)
	jobs.SetWorkflow(eng)
	s := New(&cfg.Server, cfg.Server.Token, cfg.Server.AllowEmptyToken, jobs, eng, projects, agents, nil, cfg.Runners, nil, nil)

	lib, err := skill.NewStore(filepath.Join(root, "skills"), st, skill.DefaultLimits())
	if err != nil {
		t.Fatalf("new skill store: %v", err)
	}
	s.SetSkills(lib)
	return s, lib
}

// skillZipBytes builds a valid skill archive in memory: a SKILL.md carrying the
// frontmatter the store reads the name/description from, plus one nested attachment so
// the file list is not a single entry.
func skillZipBytes(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ path, data string }{
		{"SKILL.md", "---\nname: " + name + "\ndescription: " + name + " rules\n---\n\n" + body},
		{"notes/ref.md", "see SKILL.md\n"},
	} {
		w, err := zw.Create(f.path)
		if err != nil {
			t.Fatalf("zip entry %s: %v", f.path, err)
		}
		if _, err := io.WriteString(w, f.data); err != nil {
			t.Fatalf("zip write %s: %v", f.path, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// writeSkillZip materialises one of those archives at a .zip path — the shape
// skill.Store.Import requires of a local source (it classifies by extension).
func writeSkillZip(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".zip")
	if err := os.WriteFile(path, skillZipBytes(t, name, body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// skillImportUpload posts a zip as the multipart `file` part, the body shape the web
// console sends (the filename is arbitrary — the skill's name comes from SKILL.md).
func skillImportUpload(t *testing.T, s *Server, token string, zipBytes []byte, filename string) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write(zipBytes); err != nil {
		t.Fatalf("write zip part: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/skills/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

// skillLibraryNames is the library's name set, as the handler would list it.
func skillLibraryNames(t *testing.T, lib *skill.Store) []string {
	t.Helper()
	list, err := lib.List()
	if err != nil {
		t.Fatalf("list library: %v", err)
	}
	names := make([]string, 0, len(list))
	for _, sk := range list {
		names = append(names, sk.Name)
	}
	slices.Sort(names)
	return names
}

// TestSkillsHTTPRequiresAdminForWrites pins the can_admin gate on all three skill
// writes (design §一) with the project/config routes' 403 body, and proves the gate is
// the ONLY difference: the same calls that a reader is refused succeed as an admin, and
// the refused ones left the library untouched.
func TestSkillsHTTPRequiresAdminForWrites(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{
			Governance: config.GovernanceConfig{RequireAdminCapability: true},
			Callers: []config.CallerConfig{
				{ID: "reader", Token: "reader-token"},
				{ID: "admin", Token: "admin-token", CanAdmin: true},
			},
		},
	}
	s, lib := newSkillsTestServer(t, cfg)

	// A skill that already exists, so update/delete have a real target: a 403 is then
	// the only reason they could not have taken effect.
	if _, err := lib.Import(writeSkillZip(t, "house-rules", "house rules body"), "fixture"); err != nil {
		t.Fatalf("seed library: %v", err)
	}
	before := skillLibraryNames(t, lib)
	zipBytes := skillZipBytes(t, "fresh", "fresh body")

	rejected := []struct {
		name, method, path string
		multipart          bool
	}{
		{"import", http.MethodPost, "/v1/skills/import", true},
		{"update", http.MethodPost, "/v1/skills/house-rules/update", false},
		{"delete", http.MethodDelete, "/v1/skills/house-rules", false},
	}
	for _, tc := range rejected {
		var resp *http.Response
		if tc.multipart {
			resp = skillImportUpload(t, s, "reader-token", zipBytes, "whatever.zip")
		} else {
			resp = do(t, s, tc.method, tc.path, "reader-token", nil)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s as reader: status=%d, want 403", tc.name, resp.StatusCode)
		}
		var body errorBody
		decode(t, resp, &body)
		if body.Error != "admin not permitted for this caller" || body.Detail != "caller lacks can_admin capability" {
			t.Fatalf("%s as reader: body=%+v, want the project routes' 403 wording", tc.name, body)
		}
	}
	if got := skillLibraryNames(t, lib); !slices.Equal(got, before) {
		t.Fatalf("library after the refused writes = %v, want %v (nothing may be touched)", got, before)
	}

	// The same three calls as an admin: none is refused, and each one takes effect.
	if resp := skillImportUpload(t, s, "admin-token", zipBytes, "whatever.zip"); resp.StatusCode != http.StatusCreated {
		t.Fatalf("import as admin: status=%d, want 201", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/skills/house-rules/update", "admin-token", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("update as admin: status=%d, want 200", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/skills/house-rules", "admin-token", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete as admin: status=%d, want 204", resp.StatusCode)
	}
	// The reader's refused delete did not remove it; the admin's did — leaving exactly
	// the skill the admin import added.
	if got := skillLibraryNames(t, lib); !slices.Equal(got, []string{"fresh"}) {
		t.Fatalf("library after the admin writes = %v, want [fresh]", got)
	}
}

// TestSkillsHTTPImportZipAndList drives the whole read/import/export/delete surface
// end to end over a real store: a zip goes in as admin, the library lists it, the
// detail carries the SKILL.md text, the export streams an archive that re-imports to
// the same version, and a delete takes it away.
func TestSkillsHTTPImportZipAndList(t *testing.T) {
	// Unwired (where mcp/most tests sit): the routes are mounted but answer 503, so a
	// caller can tell "this server has no library" from "no such route".
	unwired := newTestServer(t, testToken, false)
	if resp := do(t, unwired, http.MethodGet, "/v1/skills", testToken, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("list on an unwired server: status=%d, want 503", resp.StatusCode)
	}

	s, _ := newSkillsTestServer(t, &config.Config{Server: config.ServerConfig{Token: testToken}})

	// An empty library lists as [] and never null, so the console can map over it
	// before anything has been imported.
	emptyResp := do(t, s, http.MethodGet, "/v1/skills", testToken, nil)
	rawEmpty, err := io.ReadAll(emptyResp.Body)
	emptyResp.Body.Close()
	if err != nil {
		t.Fatalf("read empty list: %v", err)
	}
	if strings.TrimSpace(string(rawEmpty)) != `{"skills":[]}` {
		t.Fatalf("empty list body = %q, want {\"skills\":[]}", rawEmpty)
	}

	zipBytes := skillZipBytes(t, "house-rules", "house rules body\n")

	resp := skillImportUpload(t, s, testToken, zipBytes, "some-upload.zip")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import: status=%d, want 201", resp.StatusCode)
	}
	var imported skillImportResp
	decode(t, resp, &imported)
	if imported.Skill.Name != "house-rules" || imported.Replaced {
		t.Fatalf("import = %+v, want a first-time import of house-rules", imported)
	}
	if len(imported.Skill.Files) != 2 {
		t.Fatalf("import files = %+v, want SKILL.md + notes/ref.md", imported.Skill.Files)
	}

	resp = do(t, s, http.MethodGet, "/v1/skills", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list: status=%d, want 200", resp.StatusCode)
	}
	var listed skillsListResp
	decode(t, resp, &listed)
	if len(listed.Skills) != 1 || listed.Skills[0].Name != "house-rules" {
		t.Fatalf("list = %+v, want just house-rules", listed.Skills)
	}
	paths := make([]string, 0, len(listed.Skills[0].Files))
	for _, f := range listed.Skills[0].Files {
		if f.SHA256 == "" {
			t.Fatalf("file %s has no sha256: %+v", f.Path, f)
		}
		paths = append(paths, f.Path)
	}
	if !slices.Equal(paths, []string{"SKILL.md", "notes/ref.md"}) {
		t.Fatalf("listed files = %v, want [SKILL.md notes/ref.md]", paths)
	}

	resp = do(t, s, http.MethodGet, "/v1/skills/house-rules", testToken, nil)
	var detail skillDetailResp
	decode(t, resp, &detail)
	if detail.Name != "house-rules" || detail.Description != "house-rules rules" {
		t.Fatalf("detail = %+v, want the SKILL.md frontmatter", detail)
	}
	if !strings.Contains(detail.Content, "name: house-rules") || !strings.Contains(detail.Content, "house rules body") {
		t.Fatalf("detail content = %q, want the SKILL.md text", detail.Content)
	}

	if resp := do(t, s, http.MethodGet, "/v1/skills/nope", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get unknown: status=%d, want 404", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodGet, "/v1/skills/nope/export", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("export unknown: status=%d, want 404", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodDelete, "/v1/skills/nope", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete unknown: status=%d, want 404", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodPost, "/v1/skills/nope/update", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("update unknown: status=%d, want 404", resp.StatusCode)
	}

	resp = do(t, s, http.MethodGet, "/v1/skills/house-rules/export", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: status=%d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("export content-type=%q, want application/zip", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); cd != `attachment; filename="house-rules.zip"` {
		t.Fatalf("export disposition=%q", cd)
	}
	exported, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	if got := zipMember(t, exported, "SKILL.md"); !strings.Contains(got, "house rules body") {
		t.Fatalf("exported SKILL.md = %q, want the skill's manifest", got)
	}

	// An export is a re-importable archive: the same bytes come back as a replace of
	// the same name, with the same version (a content hash, so nothing drifted).
	resp = skillImportUpload(t, s, testToken, exported, "roundtrip.zip")
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("re-import of the export: status=%d, want 201", resp.StatusCode)
	}
	var roundtrip skillImportResp
	decode(t, resp, &roundtrip)
	if !roundtrip.Replaced || roundtrip.Skill.Name != "house-rules" {
		t.Fatalf("re-import = %+v, want a replace of house-rules", roundtrip)
	}
	if roundtrip.Skill.Version != imported.Skill.Version {
		t.Fatalf("version after re-import = %q, want %q", roundtrip.Skill.Version, imported.Skill.Version)
	}

	if resp := do(t, s, http.MethodDelete, "/v1/skills/house-rules", testToken, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: status=%d, want 204", resp.StatusCode)
	}
	if resp := do(t, s, http.MethodGet, "/v1/skills/house-rules", testToken, nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get after delete: status=%d, want 404", resp.StatusCode)
	}

	// The JSON body is the CLI's other half: a spec the SERVER resolves itself (here a
	// local directory; http(s)/git+https go down the same branch). It also pins that the
	// static `import` segment wins over `{name}` on POST.
	dir := t.TempDir()
	manifest := "---\nname: from-dir\ndescription: dir source\n---\n\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write dir skill: %v", err)
	}
	resp = do(t, s, http.MethodPost, "/v1/skills/import", testToken, map[string]string{"source": dir})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("import from a spec: status=%d, want 201", resp.StatusCode)
	}
	var fromSpec skillImportResp
	decode(t, resp, &fromSpec)
	if fromSpec.Skill.Name != "from-dir" || fromSpec.Replaced {
		t.Fatalf("import from a spec = %+v, want from-dir", fromSpec)
	}

	// A successful update (the recorded source is the temp dir above, so the re-fetch
	// resolves) pins the `change` wire keys the web binds to: lower-case, with the three
	// diff lists present as arrays even when — as here — nothing changed.
	resp = do(t, s, http.MethodPost, "/v1/skills/from-dir/update", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update from-dir: status=%d, want 200", resp.StatusCode)
	}
	var updated struct {
		Skill  skill.Skill    `json:"skill"`
		Change map[string]any `json:"change"`
	}
	decode(t, resp, &updated)
	if updated.Skill.Name != "from-dir" {
		t.Fatalf("update skill = %+v, want from-dir", updated.Skill)
	}
	keys := make([]string, 0, len(updated.Change))
	for k := range updated.Change {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if !slices.Equal(keys, []string{"added", "changed", "name", "removed"}) {
		t.Fatalf("change keys = %v, want [added changed name removed]", keys)
	}
	for _, k := range []string{"added", "changed", "removed"} {
		if _, ok := updated.Change[k].([]any); !ok {
			t.Fatalf("change.%s = %#v, want a JSON array", k, updated.Change[k])
		}
	}

	if resp := do(t, s, http.MethodPost, "/v1/skills/import", testToken, map[string]string{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("import without a source: status=%d, want 400", resp.StatusCode)
	}
}

// zipMember returns the text of one archive entry, failing the test when it is absent.
func zipMember(t *testing.T, archive []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open exported archive: %v", err)
	}
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(data)
	}
	t.Fatalf("exported archive has no %s: %v", name, zr.File)
	return ""
}
