package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/tracker"
)

func TestPrimeIncludesPlanHandoff(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if _, _, err := tracker.Init(root, "prime-real", true); err != nil {
		t.Fatalf("init tracker: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/plans":
			fmt.Fprint(w, `{"plans":[{"plan_id":"plan-old","updated_at":10},{"plan_id":"plan-new","updated_at":30},{"plan_id":"plan-mid","updated_at":20},{"plan_id":"plan-four","updated_at":40}],"total":4,"limit":3,"offset":0}`)
		case strings.HasPrefix(r.URL.Path, "/v1/plans/") && strings.HasSuffix(r.URL.Path, "/handoff"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/plans/"), "/handoff")
			fmt.Fprintf(w, `{"plan_id":%q,"version":1,"body":"handoff-%s","by":"agent","at":%d}`, id, id, time.Now().Unix())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configPath := filepath.Join(root, "config.yaml")
	configYAML := fmt.Sprintf("server:\n  addr: %q\n  allow_empty_token: true\nprojects:\n  self:\n    host_path: %q\n", server.URL, root)
	if err := os.WriteFile(configPath, []byte(configYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := tracker.Discover(root, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := config.Load(configPath)
	if key, ok := cfg.ProjectForPath(root); !ok || key != "self" {
		t.Fatalf("project resolution=%q/%v cfg=%+v", key, ok, cfg.Projects)
	}
	cli, cliErr := newClient(configPath, "", "")
	if cliErr != nil {
		t.Fatalf("new client: %v", cliErr)
	}
	plans, listErr := cli.ListPlans(client.PlanListOpts{Status: "open", Project: "self", Limit: 3})
	if listErr != nil {
		t.Fatalf("list plans: %v", listErr)
	}
	if _, handoffErr := cli.GetPlanHandoff(plans.Plans[0].PlanID, 0); handoffErr != nil {
		t.Fatalf("get handoff: %v", handoffErr)
	}
	body, err := primeWithServerHandoffs(s, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## 进行中 plan") || !strings.Contains(body, "plan-four") || !strings.Contains(body, "plan-new") || !strings.Contains(body, "plan-mid") || strings.Contains(body, "plan-old") {
		t.Fatalf("prime handoff section/order/limit mismatch: %s", body)
	}
	if strings.Index(body, "plan-four") > strings.Index(body, "plan-new") || strings.Index(body, "plan-new") > strings.Index(body, "plan-mid") {
		t.Fatalf("plans not sorted newest first: %s", body)
	}
}

func TestPrimeHandoffServerTimeoutIsSilent(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if _, _, err := tracker.Init(root, "prime-timeout", true); err != nil {
		t.Fatalf("init tracker: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(3 * time.Second) }))
	defer server.Close()
	configPath := filepath.Join(root, "config.yaml")
	config := fmt.Sprintf("server:\n  addr: %q\n  allow_empty_token: true\nprojects:\n  self:\n    host_path: %q\n", server.URL, root)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := tracker.Discover(root, "")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	body, err := primeWithServerHandoffs(s, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("prime timeout took %s", elapsed)
	}
	if strings.Contains(body, "## 进行中 plan") {
		t.Fatalf("timeout should omit handoff section: %s", body)
	}
}
