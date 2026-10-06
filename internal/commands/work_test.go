package commands

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/config"
)

func workCLI(t *testing.T, server string, args ...string) (string, int) {
	t.Helper()
	workOpts = workOptions{} // gcli keeps the previous parse's values for zero defaults
	var code int
	out := captureOutput(t, func() {
		app := NewApp("test")
		code = app.Run(append([]string{"work", args[0], "--server", server}, args[1:]...))
	})
	return out, code
}

func workCLIOK(t *testing.T, server string, args ...string) string {
	t.Helper()
	out, code := workCLI(t, server, args...)
	if code != 0 {
		t.Fatalf("gofer work %v failed (exit %d):\n%s", args, code, out)
	}
	return out
}

func workIDFrom(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`w-[0-9a-f]{10}`).FindString(out)
	if m == "" {
		t.Fatalf("no work item id in output:\n%s", out)
	}
	return m
}

func TestWorkCLIEndToEnd(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	server := newPlanTestServer(t)

	out := workCLIOK(t, server, "new", "买两台设备", "--goal", "给现场换设备", "--project", "self")
	a := workIDFrom(t, out)
	b := workIDFrom(t, workCLIOK(t, server, "new", "另一件事"))

	// ls shows both, with the header counts.
	out = workCLIOK(t, server, "ls")
	if !strings.Contains(out, a) || !strings.Contains(out, "买两台设备") || !strings.Contains(out, "open=2") {
		t.Fatalf("ls:\n%s", out)
	}
	if out := workCLIOK(t, server, "ls", "--status", "needs_onsite"); strings.Contains(out, a) {
		t.Fatalf("status filter leaked:\n%s", out)
	}

	// set: human status wins; a prefix id works; `-` clears a field.
	workCLIOK(t, server, "set", a[:6], "--status", "needs_onsite", "--blocker", "要去现场")
	out = workCLIOK(t, server, "show", a)
	if !strings.Contains(out, "needs_onsite") || !strings.Contains(out, "you set it") || !strings.Contains(out, "要去现场") {
		t.Fatalf("show:\n%s", out)
	}
	workCLIOK(t, server, "set", a, "--blocker", "-")
	if out := workCLIOK(t, server, "show", a); strings.Contains(out, "blocker:") {
		t.Fatalf("blocker not cleared:\n%s", out)
	}
	if out, code := workCLI(t, server, "set", a); code == 0 {
		t.Fatalf("set with no field must fail:\n%s", out)
	}
	if out, code := workCLI(t, server, "set", a, "--status", "bogus"); code == 0 || !strings.Contains(out, "invalid status") {
		t.Fatalf("bad status must fail with the server's reason:\n%s", out)
	}
	// stale rev -> conflict surfaced.
	if out, code := workCLI(t, server, "set", a, "--title", "x", "--rev", "1"); code == 0 || !strings.Contains(out, "409") {
		t.Fatalf("stale rev must fail with 409:\n%s", out)
	}

	// note / park / remind / link / report
	workCLIOK(t, server, "note", a, "周三去现场")
	out = workCLIOK(t, server, "park", a, "--until", "2h", "--note", "到货后继续")
	if !strings.Contains(out, "parked "+a) || !strings.Contains(out, "until") {
		t.Fatalf("park:\n%s", out)
	}
	if out, code := workCLI(t, server, "park", a); code == 0 {
		t.Fatalf("park with neither --until nor --note must fail:\n%s", out)
	}
	workCLIOK(t, server, "remind", a, "tomorrow")
	if out := workCLIOK(t, server, "show", a); !strings.Contains(out, "remind at:") {
		t.Fatalf("remind not shown:\n%s", out)
	}
	workCLIOK(t, server, "remind", a, "--clear")
	if out := workCLIOK(t, server, "show", a); strings.Contains(out, "remind at:") {
		t.Fatalf("remind not cleared:\n%s", out)
	}
	workCLIOK(t, server, "link", a, "--issue", "ISS-7", "--job", "job-1")
	out = workCLIOK(t, server, "show", a)
	if !strings.Contains(out, "ISS-7") || !strings.Contains(out, "job-1") {
		t.Fatalf("links missing:\n%s", out)
	}
	workCLIOK(t, server, "link", a, "--issue", "ISS-7", "--rm")

	// A report with `active` releases the human lock.
	workCLIOK(t, server, "report", a, "--status", "active", "--next", "继续联调", "--summary", "设备已到")
	out = workCLIOK(t, server, "show", a)
	if !strings.Contains(out, "[active]") || !strings.Contains(out, "继续联调") || !strings.Contains(out, "report") {
		t.Fatalf("after report:\n%s", out)
	}
	if out, code := workCLI(t, server, "report", a); code == 0 {
		t.Fatalf("empty report must fail:\n%s", out)
	}

	// merge b into a, then split a new one out; JSON output works.
	out = workCLIOK(t, server, "merge", a, b)
	if !strings.Contains(out, "merged 1 item(s) into "+a) {
		t.Fatalf("merge:\n%s", out)
	}
	if out := workCLIOK(t, server, "ls"); strings.Contains(out, b) {
		t.Fatalf("merged source still listed:\n%s", out)
	}
	out = workCLIOK(t, server, "split", a, "拆出的事", "--goal", "g2")
	c := workIDFrom(t, out)
	if !strings.Contains(workCLIOK(t, server, "ls", "--json"), c) {
		t.Fatal("split item missing from --json list")
	}

	// done items vanish unless --all.
	workCLIOK(t, server, "set", c, "--status", "done")
	if out := workCLIOK(t, server, "ls"); strings.Contains(out, c) {
		t.Fatalf("done item listed:\n%s", out)
	}
	if out := workCLIOK(t, server, "ls", "--all"); !strings.Contains(out, c) {
		t.Fatalf("--all must list the done item:\n%s", out)
	}

	// digest
	out = workCLIOK(t, server, "digest")
	if !strings.Contains(out, "工作摘要") || !strings.Contains(out, "等我 0") {
		t.Fatalf("digest:\n%s", out)
	}
	if out := workCLIOK(t, server, "digest", "--send"); !strings.Contains(out, "queued to 0 webhook(s)") {
		t.Fatalf("digest --send:\n%s", out)
	}
}

func TestParseWorkTime(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
	}{
		{"2h", now.Add(2 * time.Hour)},
		{"90m", now.Add(90 * time.Minute)},
		{"3d", now.AddDate(0, 0, 3)},
		{"1w", now.AddDate(0, 0, 7)},
		{"tomorrow", time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)},
		{"2026-10-08", time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)},
		{"2026-10-08 09:30", time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)},
		{"2026-10-08T09:30:00Z", time.Date(2026, 10, 8, 9, 30, 0, 0, time.UTC)},
		{"1790000000", time.Unix(1790000000, 0)},
	}
	for _, c := range cases {
		got, err := parseWorkTime(c.in, now)
		if err != nil || got != c.want.Unix() {
			t.Fatalf("parseWorkTime(%q) = %d, %v; want %d", c.in, got, err, c.want.Unix())
		}
	}
	for _, bad := range []string{"", "soon", "-2h", "0d"} {
		if _, err := parseWorkTime(bad, now); err == nil {
			t.Fatalf("parseWorkTime(%q) should fail", bad)
		}
	}
}

func TestWorkCLIRequestsSummarizeAndSuggestions(t *testing.T) {
	isolateConfigEnv(t)
	config.InputCfgFile = ""
	t.Cleanup(func() { config.InputCfgFile = "" })
	server := newPlanTestServer(t)
	a := workIDFrom(t, workCLIOK(t, server, "new", "买两台设备"))

	// Nothing in flight yet; an unknown request id is refused by the report.
	if out := workCLIOK(t, server, "requests"); !strings.Contains(out, "no requests") {
		t.Fatalf("requests:\n%s", out)
	}
	if out := workCLIOK(t, server, "requests", a, "--all"); !strings.Contains(out, "no requests") {
		t.Fatalf("requests <id> --all:\n%s", out)
	}
	if out, code := workCLI(t, server, "report", a, "--summary", "x", "--request", "wr-nope"); code == 0 || !strings.Contains(out, "unknown request") {
		t.Fatalf("report with an unknown --request must fail:\n%s", out)
	}

	// No session on the item / no usable summarizer in the test server: honest refusals.
	if out, code := workCLI(t, server, "summarize", a); code == 0 || !strings.Contains(out, "409") {
		t.Fatalf("summarize without a session must fail with 409:\n%s", out)
	}
	if out, code := workCLI(t, server, "accept", a, "goal"); code == 0 || !strings.Contains(out, "404") {
		t.Fatalf("accept without a suggestion must fail with 404:\n%s", out)
	}
	if out, code := workCLI(t, server, "dismiss", a, "goal"); code == 0 || !strings.Contains(out, "404") {
		t.Fatalf("dismiss without a suggestion must fail with 404:\n%s", out)
	}
}
