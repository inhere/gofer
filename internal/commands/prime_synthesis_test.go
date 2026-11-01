package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/tracker"
)

// synthesisPrimeServer answers the two reads prime makes: the newest succeeded run of the
// project (metadata) and that run's detail with the result.
func synthesisPrimeServer(t *testing.T, project string, runs []jobstore.MemorySynthesisRun) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/memory-syntheses":
			if q := r.URL.Query(); q.Get("project") != project || q.Get("state") != "succeeded" || q.Get("limit") != "1" {
				t.Errorf("unexpected list query %v", q)
			}
			meta := []jobstore.MemorySynthesisRun{}
			for _, run := range runs {
				run.ResultJSON = ""
				meta = append(meta, run)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runs": meta})
		case strings.HasPrefix(r.URL.Path, "/v1/memory-syntheses/"):
			for _, run := range runs {
				if r.URL.Path == "/v1/memory-syntheses/"+run.ID {
					_ = json.NewEncoder(w).Encode(run)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func synthesisPrimeStore(t *testing.T, project string) *tracker.Store {
	t.Helper()
	s := refocusStore(t)
	if err := os.WriteFile(filepath.Join(s.Dir, "config.yaml"), []byte("project_key: "+project+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return s
}

// citationConfig writes a config that opts in to the opening citation
// (memory.synthesis.prime_citation, off by default), with extra YAML appended.
func citationConfig(t *testing.T, extra string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("memory:\n  synthesis:\n    prime_citation: true\n"+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPrimeSynthesisSectionReferencesLatestRun(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	srv := synthesisPrimeServer(t, "proj", []jobstore.MemorySynthesisRun{{
		ID: "ms-0001", ProjectKey: "proj", Day: "2026-10-31", Status: "succeeded",
		ResultJSON: `{"schema":"daily-memory-synthesis.v1","summary":"the scheduler landed","highlights":[{"text":"SECRET-HIGHLIGHT"}],"open_questions":["SECRET-QUESTION"]}`,
	}})
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	s := synthesisPrimeStore(t, "proj")
	out := primeSynthesisSection(s, citationConfig(t, ""), now, 4000)
	for _, want := range []string{"2026-10-31", "ms-0001", "the scheduler landed", "gofer memory synthesis show ms-0001"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET") || len(out) > 600 {
		t.Errorf("highlights / open questions must stay out and the block is capped at 600 bytes (len %d):\n%s", len(out), out)
	}
}

func TestPrimeSynthesisSectionAbsentCases(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	run := func(day, result string) []jobstore.MemorySynthesisRun {
		return []jobstore.MemorySynthesisRun{{ID: "ms-1", ProjectKey: "proj", Day: day, Status: "succeeded", ResultJSON: result}}
	}
	good := `{"summary":"fine"}`
	for name, tc := range map[string]struct {
		runs   []jobstore.MemorySynthesisRun
		budget int
	}{
		"no successful run": {nil, 4000},
		"expired run":       {run("2026-10-20", good), 4000},
		"unparsable result": {run("2026-10-31", "not json"), 4000},
		"empty summary":     {run("2026-10-31", `{"summary":""}`), 4000},
		"no budget left":    {run("2026-10-31", good), 80},
	} {
		srv := synthesisPrimeServer(t, "proj", tc.runs)
		t.Setenv("GOFER_SERVER_ADDR", srv.URL)
		if out := primeSynthesisSection(synthesisPrimeStore(t, "proj"), citationConfig(t, ""), now, tc.budget); out != "" {
			t.Errorf("%s: want no section, got %q", name, out)
		}
	}
	// Unreachable server / no project key / no address: never an error, never a section.
	t.Setenv("GOFER_SERVER_ADDR", "")
	if out := primeSynthesisSection(synthesisPrimeStore(t, "proj"), citationConfig(t, ""), now, 4000); out != "" {
		t.Errorf("no server address: %q", out)
	}
	srv := synthesisPrimeServer(t, "proj", run("2026-10-31", good))
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	if out := primeSynthesisSection(nil, citationConfig(t, ""), now, 4000); out != "" {
		t.Errorf("no project key: %q", out)
	}
	// Off by default: a fresh successful run is not cited unless the config opts in.
	offCfg := filepath.Join(t.TempDir(), "off.yaml")
	if err := os.WriteFile(offCfg, []byte("memory:\n  synthesis:\n    enabled: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"prime_citation unset": offCfg, "no readable config": filepath.Join(t.TempDir(), "missing.yaml")} {
		if out := primeSynthesisSection(synthesisPrimeStore(t, "proj"), path, now, 4000); out != "" {
			t.Errorf("%s: the citation is opt-in, got %q", name, out)
		}
	}
}

func TestPrimeSynthesisSectionFollowsDeploymentLanguage(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.Local)
	srv := synthesisPrimeServer(t, "proj", []jobstore.MemorySynthesisRun{{ID: "ms-1", ProjectKey: "proj", Day: "2026-10-31", Status: "succeeded", ResultJSON: `{"summary":"调度已落地"}`}})
	t.Setenv("GOFER_SERVER_ADDR", srv.URL)
	zhCfg := citationConfig(t, "server:\n  language: zh-CN\n")
	enCfg := citationConfig(t, "server:\n  language: en\n")
	if out := primeSynthesisSection(synthesisPrimeStore(t, "proj"), zhCfg, now, 4000); !strings.Contains(out, "项目动态") || !strings.Contains(out, "调度已落地") {
		t.Errorf("zh-CN deployment:\n%s", out)
	}
	if out := primeSynthesisSection(synthesisPrimeStore(t, "proj"), enCfg, now, 4000); !strings.Contains(out, "Project activity") {
		t.Errorf("en deployment:\n%s", out)
	}
}
