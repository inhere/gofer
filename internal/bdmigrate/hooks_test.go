package bdmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestOrderedJSONRoundTripsUntouchedDocuments(t *testing.T) {
	docs := []string{
		"{\n  \"env\": {\n    \"A\": \"1\"\n  },\n  \"hooks\": {\n    \"Stop\": [\n      {\n        \"matcher\": \"\",\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"a < b && c\",\n            \"timeout\": 10\n          }\n        ]\n      }\n    ]\n  },\n  \"empty\": {},\n  \"list\": [],\n  \"n\": 1.50,\n  \"flag\": true,\n  \"nil\": null\n}\n",
		"{\n\t\"tab\": [\n\t\t1,\n\t\t2\n\t]\n}",
	}
	for _, doc := range docs {
		v, err := parseOrdered([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		out, err := marshalOrdered(v, detectIndent([]byte(doc)))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimRight(string(out), "\n") != strings.TrimRight(doc, "\n") {
			t.Fatalf("round trip changed the document:\n%s\n---\n%s", doc, out)
		}
	}
}

func TestPlanHooksClaudeReplacesBdPrimeInPlace(t *testing.T) {
	root := t.TempDir()
	path := hookFile(root, "claude")
	orig := "{\n  \"env\": {\n    \"K\": \"v\"\n  },\n  \"hooks\": {\n    \"PreCompact\": [\n      {\n        \"hooks\": [\n          {\n            \"command\": \"bd prime\",\n            \"type\": \"command\"\n          }\n        ],\n        \"matcher\": \"\"\n      }\n    ],\n    \"SessionStart\": [\n      {\n        \"hooks\": [\n          {\n            \"command\": \"bd prime --no-memories --hook-json\",\n            \"type\": \"command\"\n          }\n        ],\n        \"matcher\": \"\"\n      },\n      {\n        \"hooks\": [\n          {\n            \"command\": \"gofer hook claude\",\n            \"timeout\": 10,\n            \"type\": \"command\"\n          }\n        ],\n        \"matcher\": \"\"\n      }\n    ],\n    \"PostToolUse\": [\n      {\n        \"matcher\": \"\",\n        \"hooks\": [\n          {\n            \"type\": \"command\",\n            \"command\": \"other & tool\"\n          }\n        ]\n      }\n    ]\n  }\n}\n"
	writeFile(t, path, orig)
	plan, err := planHooks("claude", path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(orig, "bd prime --no-memories --hook-json", "gofer repo prime --hook-json --agent claude", 1)
	// PreCompact loses its (now empty) group and event; nothing else moves.
	want = strings.Replace(want, "    \"PreCompact\": [\n      {\n        \"hooks\": [\n          {\n            \"command\": \"bd prime\",\n            \"type\": \"command\"\n          }\n        ],\n        \"matcher\": \"\"\n      }\n    ],\n", "", 1)
	if string(plan.New) != want {
		t.Fatalf("new settings:\n%s\nwant:\n%s", plan.New, want)
	}
	if len(plan.Changes) != 2 {
		t.Fatalf("changes: %v", plan.Changes)
	}
	// Re-planning the result is a no-op.
	writeFile(t, path, string(plan.New))
	again, err := planHooks("claude", path)
	if err != nil || again.New != nil || len(again.Changes) != 0 {
		t.Fatalf("second plan must be empty: %+v %v", again, err)
	}
}

func TestPlanHooksCodexDropsAllBdHooksKeepsExistingGoferPrime(t *testing.T) {
	root := t.TempDir()
	path := hookFile(root, "codex")
	writeFile(t, path, `{
  "hooks": {
    "PostCompact": [{"hooks": [{"command": "bd codex-hook PostCompact", "type": "command"}], "matcher": "manual|auto"}],
    "SessionStart": [
      {"hooks": [{"command": "bd codex-hook SessionStart", "statusMessage": "Loading", "type": "command"}], "matcher": "startup|resume|clear"},
      {"hooks": [{"command": "gofer repo prime --hook-json --agent codex", "type": "command"}], "matcher": ""}
    ],
    "UserPromptSubmit": [{"hooks": [{"command": "bd codex-hook UserPromptSubmit", "type": "command"}]}]
  }
}
`)
	plan, err := planHooks("codex", path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(plan.New)
	if strings.Contains(out, "bd codex-hook") || strings.Count(out, "gofer repo prime") != 1 || strings.Contains(out, "PostCompact") || strings.Contains(out, "UserPromptSubmit") {
		t.Fatalf("codex hooks:\n%s", out)
	}
}

func TestPlanHooksAddsPrimeWhenMissingFlagsCompoundBdAndSkipsAbsentFile(t *testing.T) {
	root := t.TempDir()
	path := hookFile(root, "claude")
	writeFile(t, path, `{"hooks":{"Stop":[{"matcher":"","hooks":[{"type":"command","command":"bd sync && other"}]}]}}`)
	plan, err := planHooks("claude", path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan.New), "gofer repo prime --hook-json --agent claude") || !strings.Contains(string(plan.New), "bd sync && other") {
		t.Fatalf("add prime, keep foreign:\n%s", plan.New)
	}
	if len(plan.Manual) != 1 || !strings.Contains(plan.Manual[0], "bd sync") {
		t.Fatalf("a hook that still calls bd must be flagged: %v", plan.Manual)
	}
	absent, err := planHooks("codex", hookFile(root, "codex"))
	if err != nil || absent.Exists || absent.New != nil {
		t.Fatalf("absent file must be left alone: %+v %v", absent, err)
	}
	writeFile(t, hookFile(root, "codex"), "{not json")
	if _, err := planHooks("codex", hookFile(root, "codex")); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
}
