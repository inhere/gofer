package commands

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// teamStub is a minimal /v1/teams + /v1/mail server: it answers the routes the team commands
// call, records what the CLI put on the wire, and counts the single-team reads so a test can
// check which team a name resolved to. No t.Fatalf here (G053).
type teamStub struct {
	// teams is the list answer (GET /v1/teams), one row per team without roster.
	teams []map[string]any
	// rosters is the single-team answer (GET /v1/teams/{id}) by id, with its roster.
	rosters map[string]map[string]any
	routes  []map[string]any
	evals   []map[string]any
	// posted records the POST /v1/teams bodies, mails the POST /v1/mail bodies.
	posted []map[string]any
	mails  []map[string]any
	// deleted records the ids of the archived (DELETE) teams.
	deleted []string
	// readIDs records the ids GET /v1/teams/{id} was asked for.
	readIDs        []string
	postedMembers  []string
	removedMembers []string
}

func (s *teamStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case path == "/v1/meta", path == "/v1/projects":
			_ = json.NewEncoder(w).Encode(map[string]any{"projects": []map[string]any{
				{"key": "p1", "host_path": "/work/p1", "container_path": "/work/p1"},
				{"key": "p2", "host_path": "/work/p2", "container_path": "/work/p2"},
			}})
		case path == "/v1/teams" && r.Method == http.MethodGet:
			project := r.URL.Query().Get("project")
			rows := []map[string]any{}
			for _, t := range s.teams {
				if project == "" || t["project_key"] == project {
					rows = append(rows, t)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"teams": rows})
		case path == "/v1/teams" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.posted = append(s.posted, body)
			name, _ := body["name"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "team-new", "name": name, "project_key": body["project_key"], "leader_seat_id": "seat-1",
				"relay_mode": "leader", "relay_round_limit": 3, "relay_daily_limit": 20, "merge_window_sec": 120,
				"roster": []map[string]any{{"role": "leader", "id": "seat-1", "name": "desk", "project_key": body["project_key"], "kind": "terminal", "state": "none"}},
			})
		case strings.HasSuffix(path, "/routes"):
			_ = json.NewEncoder(w).Encode(map[string]any{"routes": s.routes})
		case strings.HasSuffix(path, "/evals"):
			_ = json.NewEncoder(w).Encode(map[string]any{"evals": s.evals})
		case strings.HasSuffix(path, "/members") && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			seat, _ := body["seat"].(string)
			s.postedMembers = append(s.postedMembers, seat)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "team-1", "name": "crew", "project_key": "p1", "leader_seat_id": "seat-1"})
		case strings.Contains(path, "/members/") && r.Method == http.MethodDelete:
			s.removedMembers = append(s.removedMembers, path[strings.LastIndex(path, "/")+1:])
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "team-1", "name": "crew", "project_key": "p1", "leader_seat_id": "seat-1"})
		case strings.HasPrefix(path, "/v1/teams/"):
			id := strings.TrimPrefix(path, "/v1/teams/")
			switch r.Method {
			case http.MethodDelete:
				s.deleted = append(s.deleted, id)
				_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "name": "crew", "project_key": "p1", "leader_seat_id": "seat-1"})
			default:
				s.readIDs = append(s.readIDs, id)
				t, ok := s.rosters[id]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": "team not found", "detail": id})
					return
				}
				_ = json.NewEncoder(w).Encode(t)
			}
		case path == "/v1/mail" && r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			s.mails = append(s.mails, body)
			to, _ := body["to"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"mail": map[string]any{
				"id": 7, "to_address": to, "state": "queued",
				"from": map[string]any{"kind": "caller"}, "to": map[string]any{"kind": "team", "display": to},
			}})
		default:
			http.NotFound(w, r)
		}
	})
}

// teamTestStub starts the stub and points the CLI's connection flags at it.
func teamTestStub(t *testing.T, s *teamStub) {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	teamStubServer = srv.URL
	jobConnOpts.server, jobConnOpts.token = srv.URL, "tok"
	t.Cleanup(func() { teamStubServer, jobConnOpts.server, jobConnOpts.token = "", "", "" })
}

// teamStubServer is the address teamCLI points the command at after resetting the flags:
// resetFlagGlobals restores the snapshot, which would otherwise drop the stub's server.
var teamStubServer string

// teamCLI runs one `team …` through the real command dispatch and returns its exit code and
// output (the flag structs are package globals gcli never resets: see flagreset_test.go).
func teamCLI(t *testing.T, args ...string) (string, int) {
	t.Helper()
	resetFlagGlobals()
	jobConnOpts.server, jobConnOpts.token = teamStubServer, "tok"
	var code int
	out := captureOutput(t, func() { code = NewApp("test").Run(args) })
	return out, code
}

// teamRow is one team as the list answer carries it (no roster).
func teamRow(id, name, project string) map[string]any {
	return map[string]any{"id": id, "name": name, "project_key": project, "leader_seat_id": "seat-1",
		"relay_mode": "leader", "relay_round_limit": 3, "relay_daily_limit": 20, "merge_window_sec": 120,
		"created_at": 1700000000, "updated_at": 1700000000}
}

// crewRoster is the single-team answer of team-1: a running leader on a flow and an offline member.
func crewRoster() map[string]any {
	t := teamRow("team-1", "crew", "p1")
	t["brief"] = "ship the release"
	t["roster"] = []map[string]any{
		{"role": "leader", "id": "seat-1", "name": "desk", "project_key": "p1", "kind": "terminal", "state": "running",
			"activity_at": 1700000060, "stage": map[string]any{"work_item_id": "wi-1", "step": "doing", "step_no": 2, "step_total": 5, "status": "active"}},
		{"role": "member", "id": "seat-2", "name": "a1", "project_key": "p1", "kind": "acp", "state": "offline"},
	}
	return t
}

// TestTeamAddPostsTheLeaderAndBrief: the create body carries the project, the leader seat and the
// text of --brief, and the answer names the leader by seat name.
func TestTeamAddPostsTheLeaderAndBrief(t *testing.T) {
	stub := &teamStub{}
	teamTestStub(t, stub)
	out, code := teamCLI(t, "team", "add", "crew", "--leader", "desk", "--brief", "ship the release", "-p", "p1")
	if code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	if len(stub.posted) != 1 {
		t.Fatalf("posted = %+v", stub.posted)
	}
	body := stub.posted[0]
	if body["project_key"] != "p1" || body["name"] != "crew" || body["leader"] != "desk" || body["brief"] != "ship the release" {
		t.Errorf("create body = %+v", body)
	}
	for _, want := range []string{"crew", "p1", "desk"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// TestTeamAddReadsTheBriefFromAFile: `--brief @<file>` sends the file's text, not the flag value.
func TestTeamAddReadsTheBriefFromAFile(t *testing.T) {
	stub := &teamStub{}
	teamTestStub(t, stub)
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte("report to the leader\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := teamCLI(t, "team", "add", "crew", "--leader", "desk", "--brief", "@"+path, "-p", "p1")
	if code != 0 {
		t.Fatalf("add exit=%d out=%s", code, out)
	}
	if len(stub.posted) != 1 || stub.posted[0]["brief"] != "report to the leader" {
		t.Fatalf("posted = %+v", stub.posted)
	}
}

// TestTeamShowPrintsTheRosterAndRoutes: the roster comes leader first with its state and current
// step, and the routing decisions name the seats they picked rather than their ids.
func TestTeamShowPrintsTheRosterAndRoutes(t *testing.T) {
	stub := &teamStub{
		teams:   []map[string]any{teamRow("team-1", "crew", "p1")},
		rosters: map[string]map[string]any{"team-1": crewRoster()},
		routes: []map[string]any{{
			"id": 3, "mail_id": 9, "from_ref": "seat-1", "to_seat_ids": []string{"seat-2"},
			"rule": "human_named", "hop": 0, "outcome": "delivered", "decided_at": 1700000000,
		}},
	}
	teamTestStub(t, stub)
	out, code := teamCLI(t, "team", "show", "crew", "-p", "p1")
	if code != 0 {
		t.Fatalf("show exit=%d out=%s", code, out)
	}
	for _, want := range []string{"crew", "team-1", "desk", "leader", "doing 2/5", "a1", "human_named", "delivered"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// The route's seats are rendered by name: an id here means the roster lookup was skipped.
	if strings.Contains(out, "seat-2") {
		t.Errorf("the route shows the seat id instead of the name:\n%s", out)
	}
}

// TestTeamShowResolvesTheNameInTheProjectOfTheFlag: the same name in two projects follows -p.
func TestTeamShowResolvesTheNameInTheProjectOfTheFlag(t *testing.T) {
	other := teamRow("team-2", "crew", "p2")
	other["roster"] = []map[string]any{{"role": "leader", "id": "seat-9", "name": "desk2", "project_key": "p2", "kind": "acp", "state": "none"}}
	stub := &teamStub{
		teams:   []map[string]any{teamRow("team-1", "crew", "p1"), other},
		rosters: map[string]map[string]any{"team-1": crewRoster(), "team-2": other},
	}
	teamTestStub(t, stub)
	out, code := teamCLI(t, "team", "show", "crew", "-p", "p2")
	if code != 0 {
		t.Fatalf("show exit=%d out=%s", code, out)
	}
	if len(stub.readIDs) == 0 || stub.readIDs[len(stub.readIDs)-1] != "team-2" {
		t.Errorf("resolved ids = %v, want the p2 team last", stub.readIDs)
	}
	if !strings.Contains(out, "desk2") {
		t.Errorf("output lacks the leader of the p2 team:\n%s", out)
	}
}

// TestTeamNameLookupFailsReadably: an unknown name is an error naming the name and the project,
// never an empty print.
func TestTeamNameLookupFailsReadably(t *testing.T) {
	stub := &teamStub{teams: []map[string]any{teamRow("team-1", "crew", "p1")}}
	teamTestStub(t, stub)
	out, code := teamCLI(t, "team", "show", "ghost", "-p", "p1")
	if code == 0 {
		t.Fatalf("an unknown team must fail, out=%s", out)
	}
	for _, want := range []string{"ghost", "p1"} {
		if !strings.Contains(out, want) {
			t.Errorf("error lacks %q:\n%s", want, out)
		}
	}
}

// TestTeamSaySendsToTheTeamAddress: `team say` is an ordinary letter to team:<name>@<project>,
// so the server's routing rules (not the CLI) decide who gets it.
func TestTeamSaySendsToTheTeamAddress(t *testing.T) {
	stub := &teamStub{
		teams:   []map[string]any{teamRow("team-1", "crew", "p1")},
		rosters: map[string]map[string]any{"team-1": crewRoster()},
	}
	teamTestStub(t, stub)
	out, code := teamCLI(t, "team", "say", "crew", "check the build", "-p", "p1")
	if code != 0 {
		t.Fatalf("say exit=%d out=%s", code, out)
	}
	if len(stub.mails) != 1 {
		t.Fatalf("mails = %+v", stub.mails)
	}
	if stub.mails[0]["to"] != "team:crew@p1" || stub.mails[0]["body"] != "check the build" {
		t.Errorf("mail body = %+v", stub.mails[0])
	}
	if !strings.Contains(out, "#7") {
		t.Errorf("output lacks the letter number:\n%s", out)
	}
}

// TestTeamRemoveArchives: `team rm` archives (DELETE) the team it resolved.
func TestTeamRemoveArchives(t *testing.T) {
	stub := &teamStub{
		teams:   []map[string]any{teamRow("team-1", "crew", "p1")},
		rosters: map[string]map[string]any{"team-1": crewRoster()},
	}
	teamTestStub(t, stub)
	out, code := teamCLI(t, "team", "rm", "crew", "-p", "p1")
	if code != 0 {
		t.Fatalf("rm exit=%d out=%s", code, out)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != "team-1" {
		t.Fatalf("deleted = %v", stub.deleted)
	}
	if !strings.Contains(out, "crew") {
		t.Errorf("output lacks the team name:\n%s", out)
	}
}

// TestTeamMemberAddAndRemove: the member sub-group adds and removes a seat of the resolved team.
func TestTeamMemberAddAndRemove(t *testing.T) {
	stub := &teamStub{
		teams:   []map[string]any{teamRow("team-1", "crew", "p1")},
		rosters: map[string]map[string]any{"team-1": crewRoster()},
	}
	teamTestStub(t, stub)
	if out, code := teamCLI(t, "team", "member", "add", "crew", "a2", "-p", "p1"); code != 0 {
		t.Fatalf("member add exit=%d out=%s", code, out)
	}
	if len(stub.postedMembers) != 1 || stub.postedMembers[0] != "a2" {
		t.Fatalf("posted members = %v", stub.postedMembers)
	}
	if out, code := teamCLI(t, "team", "member", "rm", "crew", "a2", "-p", "p1"); code != 0 {
		t.Fatalf("member rm exit=%d out=%s", code, out)
	}
	if len(stub.removedMembers) != 1 || stub.removedMembers[0] != "a2" {
		t.Fatalf("removed members = %v", stub.removedMembers)
	}
}

// TestTeamCatalogCoversEveryKey: every key the team commands ask cliText for exists in both
// catalogs, so a misspelt key never reaches the operator as `cli.team.…`.
func TestTeamCatalogCoversEveryKey(t *testing.T) {
	keyPattern := regexp.MustCompile(`cliText\("([^"]+)"`)
	seen := 0
	for _, file := range []string{"team.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, m := range keyPattern.FindAllStringSubmatch(string(src), -1) {
			key := m[1]
			seen++
			for _, lang := range i18n.Supported() {
				if got := i18n.T(lang, key); got == key {
					t.Errorf("%s: key %q is missing in %s", file, key, lang)
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no cliText key found — the scan is broken")
	}
}
