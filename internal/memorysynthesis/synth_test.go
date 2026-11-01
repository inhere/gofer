package memorysynthesis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// fakeOneShot records every request and answers with a canned output / error.
type fakeOneShot struct {
	mu     sync.Mutex
	reqs   []OneShotRequest
	output string
	err    error
	block  bool
}

func (f *fakeOneShot) Run(ctx context.Context, r OneShotRequest) (string, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	f.mu.Unlock()
	if f.block {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return f.output, f.err
}

func (f *fakeOneShot) calls() []OneShotRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]OneShotRequest(nil), f.reqs...)
}

const goodAnswer = `{"schema":"daily-memory-synthesis.v1","summary":"fact landed","highlights":[{"text":"the fact","source_refs":["work:w-1"]}],"open_questions":["what next"],"extra":"dropped"}`

func synthCfg() SynthConfig {
	return SynthConfig{Agent: "fake-agent", Model: "m1", Args: []string{"--model", "m1"}, Timeout: time.Minute, MaxOutputBytes: 4000, Lang: i18n.EN}
}

func runSynth(t *testing.T, o OneShot, c SynthConfig, items []Item) (Outcome, jobstore.MemorySynthesisRun) {
	t.Helper()
	st := openStore(t)
	svc := newService(st, items, NewSynthesizer(o, c))
	out, err := svc.Run(context.Background(), "proj", "2026-10-28", "tester")
	if err != nil {
		t.Fatal(err)
	}
	return out, out.Run
}

func TestSynthesizerSucceedsAndStoresNormalisedResult(t *testing.T) {
	f := &fakeOneShot{output: "```json\n" + goodAnswer + "\n```"}
	out, run := runSynth(t, f, synthCfg(), oneItem)
	if out.State != OutcomeSucceeded || run.Status != jobstore.MemorySynthesisSucceeded {
		t.Fatalf("out=%+v run=%+v", out, run)
	}
	var got Result
	if err := json.Unmarshal([]byte(run.ResultJSON), &got); err != nil {
		t.Fatal(err)
	}
	if got.Summary != "fact landed" || len(got.Highlights) != 1 || got.Highlights[0].SourceRefs[0] != "work:w-1" {
		t.Fatalf("result=%+v", got)
	}
	if strings.Contains(run.ResultJSON, "extra") {
		t.Fatalf("unknown model fields must not be stored: %s", run.ResultJSON)
	}
	if run.Agent != "fake-agent" || run.Model != "m1" || run.OutputDigest == "" {
		t.Fatalf("run=%+v", run)
	}
	calls := f.calls()
	if len(calls) != 1 || calls[0].Agent != "fake-agent" || calls[0].ProjectKey != "proj" || calls[0].Timeout != time.Minute {
		t.Fatalf("calls=%+v", calls)
	}
}

func TestSynthesizerFailureReasonsAreRecorded(t *testing.T) {
	cases := []struct {
		name    string
		oneshot *fakeOneShot
		cfg     func(*SynthConfig)
		want    string
	}{
		{"not json", &fakeOneShot{output: "I cannot do that"}, nil, "invalid output: no JSON object"},
		{"bad json", &fakeOneShot{output: `{"schema":"daily-memory-synthesis.v1","summary":`}, nil, "invalid output"},
		{"wrong schema", &fakeOneShot{output: `{"schema":"x","summary":"s"}`}, nil, `schema "x"`},
		{"empty summary", &fakeOneShot{output: `{"schema":"daily-memory-synthesis.v1","summary":" "}`}, nil, "summary is empty"},
		{"unknown ref", &fakeOneShot{output: `{"schema":"daily-memory-synthesis.v1","summary":"s","highlights":[{"text":"t","source_refs":["job:nope"]}]}`}, nil, "unknown source_ref"},
		{"too long", &fakeOneShot{output: goodAnswer}, func(c *SynthConfig) { c.MaxOutputBytes = 20 }, "limit 20"},
		{"secret in output", &fakeOneShot{output: `{"schema":"daily-memory-synthesis.v1","summary":"use password=hunter2hunter2 now"}`}, nil, "unsafe output"},
		{"agent error", &fakeOneShot{err: errors.New("job did not succeed (exit 1)")}, nil, "agent call failed: job did not succeed"},
		{"timeout", &fakeOneShot{block: true}, func(c *SynthConfig) { c.Timeout = 30 * time.Millisecond }, "timed out after 30ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := synthCfg()
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			out, run := runSynth(t, tc.oneshot, cfg, oneItem)
			if out.State != OutcomeFailed || run.Status != jobstore.MemorySynthesisFailed {
				t.Fatalf("out=%+v", out)
			}
			if !strings.Contains(run.Error, tc.want) {
				t.Fatalf("error %q does not mention %q", run.Error, tc.want)
			}
			if run.ResultJSON != "" || run.Agent != "fake-agent" {
				t.Fatalf("a failed run keeps no result and names the agent: %+v", run)
			}
			if strings.Contains(run.Error, "hunter2hunter2") {
				t.Fatalf("error leaks the secret: %q", run.Error)
			}
		})
	}
}

// The model only ever receives scrubbed material: a secret in a source item never reaches
// the OneShot, and an envelope that bypassed Build is refused before any call.
func TestSynthesizerNeverSendsUnscrubbedMaterial(t *testing.T) {
	const secret = "sk-abcdefghijklmnop1234"
	items := []Item{
		{Kind: "work_item", SourceRef: "work:w-1", Title: "deploy", Text: "used token=" + secret + " and Authorization: Bearer abcdefgh12345678"},
		{Kind: "job", SourceRef: "job:j-1", Title: "mail owner@example.com", Text: "API_KEY=topsecretvalue"},
	}
	f := &fakeOneShot{output: goodAnswer}
	out, _ := runSynth(t, f, synthCfg(), items)
	if out.State != OutcomeSucceeded {
		t.Fatalf("out=%+v", out)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("calls=%d", len(calls))
	}
	for _, leak := range []string{secret, "abcdefgh12345678", "owner@example.com", "topsecretvalue"} {
		if strings.Contains(calls[0].Prompt, leak) {
			t.Fatalf("prompt leaks %q", leak)
		}
	}
	if !strings.Contains(calls[0].Prompt, "[redacted]") {
		t.Fatalf("prompt should carry the redaction marker: %s", calls[0].Prompt)
	}

	// Bypassing Build: an envelope that still holds a secret fails closed, no call is made.
	f2 := &fakeOneShot{output: goodAnswer}
	raw := Materials{Envelope: Envelope{Schema: SchemaV1, Project: "proj", Day: "2026-10-28",
		Items: []Item{{ID: "m-001", Kind: "job", SourceRef: "job:j", Text: "password=" + secret}}}}
	_, err := NewSynthesizer(f2, synthCfg())(context.Background(), raw)
	if !errors.Is(err, ErrUnsafeInput) || len(f2.calls()) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(f2.calls()))
	}
}

// The prompt is written in the project language and carries the envelope and budget.
func TestSynthesizerPromptFollowsLanguage(t *testing.T) {
	for _, tc := range []struct {
		lang i18n.Lang
		want string
	}{{i18n.EN, "project-activity summarizer"}, {i18n.ZhCN, "项目动态摘要员"}} {
		f := &fakeOneShot{output: goodAnswer}
		cfg := synthCfg()
		cfg.Lang = tc.lang
		runSynth(t, f, cfg, oneItem)
		p := f.calls()[0].Prompt
		if !strings.Contains(p, tc.want) || !strings.Contains(p, `"source_ref":"work:w-1"`) || !strings.Contains(p, "4000") {
			t.Fatalf("%s prompt: %s", tc.lang, p)
		}
	}
}

func TestParseResultToleratesChatterButNotTruncation(t *testing.T) {
	allowed := map[string]bool{"a": true}
	r, err := ParseResult("Sure! {\"schema\":\"daily-memory-synthesis.v1\",\"summary\":\"s {x}\",\"highlights\":[]} done", allowed)
	if err != nil || r.Summary != "s {x}" || r.Highlights == nil || r.OpenQuestions == nil {
		t.Fatalf("r=%+v err=%v", r, err)
	}
	if _, err := ParseResult(`{"schema":"daily-memory-synthesis.v1","summary":"s"`, allowed); !errors.Is(err, ErrOutputFormat) {
		t.Fatalf("err=%v", err)
	}
}

// A forced re-run reaches the synthesizer again; an unforced one with the same input does not.
func TestRunWithForceReusesOrReruns(t *testing.T) {
	st := openStore(t)
	f := &fakeOneShot{output: goodAnswer}
	svc := newService(st, oneItem, NewSynthesizer(f, synthCfg()))
	ctx := context.Background()
	first, err := svc.RunWith(ctx, RunOptions{Project: "proj", Day: "2026-10-28", CreatedBy: "u"})
	if err != nil || first.State != OutcomeSucceeded {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	again, _ := svc.RunWith(ctx, RunOptions{Project: "proj", Day: "2026-10-28", CreatedBy: "u"})
	if again.State != OutcomeReused || again.Run.ID != first.Run.ID || len(f.calls()) != 1 {
		t.Fatalf("again=%+v calls=%d", again, len(f.calls()))
	}
	forced, err := svc.RunWith(ctx, RunOptions{Project: "proj", Day: "2026-10-28", CreatedBy: "u", Force: true})
	if err != nil || forced.State != OutcomeSucceeded || forced.Run.ID == first.Run.ID || len(f.calls()) != 2 {
		t.Fatalf("forced=%+v err=%v calls=%d", forced, err, len(f.calls()))
	}
	old, _, _ := st.GetMemorySynthesisRun(first.Run.ID)
	if old.Status != jobstore.MemorySynthesisSkipped {
		t.Fatalf("old=%+v", old)
	}
}
