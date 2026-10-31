package work

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// summarizerTextOutputs renders every text the summarizer path hands to the agent or
// returns as a status / failure reason, in lang. The zh-CN golden was recorded from the
// code BEFORE the text moved into the catalog (S3e); GOFER_GOLDEN_UPDATE=1 rewrites only
// the English file.
func summarizerTextOutputs(t *testing.T, lang i18n.Lang) string {
	t.Helper()
	var sb strings.Builder
	sect := func(name string, lines ...string) {
		sb.WriteString("## " + name + "\n")
		for _, l := range lines {
			sb.WriteString(l + "\n")
		}
	}
	setup := func(outputs ...string) (*Service, *jobstore.Store, jobstore.WorkItem, *fakeOneShot) {
		svc, st, w, os := sumFixture(t, outputs...)
		svc.SetLanguageFor(func(string) i18n.Lang { return lang })
		return svc, st, w, os
	}

	svc, st, _, _ := setup(goodJSON)
	w, _ := st.CreateWorkItem(jobstore.WorkItemInput{Title: "Order export", By: "human:alice"})
	item, _, _ := st.GetWorkItem(w.ID)
	item.Goal, item.NextStep, item.Summary, item.BlockerKind, item.BlockerText = "G", "N", "S", "person", "wait"
	sess := jobstore.AgentSession{LastMessage: "last msg", ProgressText: "progress line", Title: "the title"}
	_, _ = st.AppendWorkJournal(w.ID, jobstore.WorkJournalNote, "a journal\nline", "human:alice")
	mat, deg := shimGatherMaterial(svc, item, sess)
	sect("material degraded", fmt.Sprint(deg), mat)
	mat2, _ := shimGatherMaterial(svc, item, jobstore.AgentSession{})
	sect("material empty", mat2)
	sect("prompt fresh", shimBuildPrompt(svc, item, "MATERIAL", false, false))
	sect("prompt resumed degraded", shimBuildPrompt(svc, item, "MATERIAL", true, true))
	empty := item
	empty.Goal, empty.NextStep, empty.Summary, empty.BlockerKind, empty.BlockerText = "", "", "", "", ""
	sect("prompt empty fields", shimBuildPrompt(svc, empty, "MATERIAL", false, false))
	sect("session tail fallback", shimSessionTail(t, svc, st))

	// retry nudge
	svc3, _, w3, os3 := setup("not json", goodJSON)
	_, _ = svc3.RunSummarize(context.Background(), w3.ID, SummarizeOpts{Cause: CauseManual})
	sect("retry nudge", strings.TrimPrefix(os3.calls[1].Prompt, os3.calls[0].Prompt))

	// status reasons
	svcS, _, _, _ := setup(goodJSON)
	svcS.SetOneShot(nil)
	sect("status no oneshot", shimStatus(svcS).Reason)
	svcS.SetOneShot(choosyOneShot{fakeOneShot: &fakeOneShot{}, usable: map[string]bool{}})
	sect("status unusable project", shimStatus(svcS).Reason)

	// failure reasons returned to the caller (and stored in the request ledger)
	fail := func(name string, mod func(*Service, *jobstore.Store, jobstore.WorkItem, *fakeOneShot), cause string) {
		svcF, stF, wF, osF := setup(goodJSON)
		if mod != nil {
			mod(svcF, stF, wF, osF)
		}
		req, _ := stF.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: wF.ID, Kind: jobstore.WorkRequestSummarize})
		_, err := svcF.RunSummarize(shimCtx(lang), wF.ID, SummarizeOpts{Cause: cause, RequestID: req.ID})
		r, _, _ := stF.GetWorkRequest(req.ID)
		line := "ERR " + fmt.Sprint(err) + " | ledger " + r.Error
		sect(name, line)
	}
	fail("fail no oneshot", func(s *Service, _ *jobstore.Store, _ jobstore.WorkItem, _ *fakeOneShot) { s.SetOneShot(nil) }, CauseManual)
	fail("fail agent unavailable", func(_ *Service, _ *jobstore.Store, _ jobstore.WorkItem, o *fakeOneShot) {
		o.checkEr = errors.New("agent x gone")
	}, CauseManual)
	{
		svcF, stF, _, _ := setup(goodJSON)
		bare, _ := stF.CreateWorkItem(jobstore.WorkItemInput{Title: "bare", By: "human:alice"})
		req, _ := stF.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: bare.ID, Kind: jobstore.WorkRequestSummarize})
		_, err := svcF.RunSummarize(shimCtx(lang), bare.ID, SummarizeOpts{Cause: CauseManual, RequestID: req.ID})
		r, _, _ := stF.GetWorkRequest(req.ID)
		sect("fail no session", "ERR "+fmt.Sprint(err)+" | ledger "+r.Error)
	}
	fail("fail daily limit", func(s *Service, _ *jobstore.Store, w jobstore.WorkItem, _ *fakeOneShot) {
		s.SetConfigFn(func() config.WorkConfig { return config.WorkConfig{SummarizeDailyLimit: 1} })
		_, _ = s.RunSummarize(context.Background(), w.ID, SummarizeOpts{Cause: CauseAuto})
	}, CauseAuto)
	fail("fail session busy", func(s *Service, _ *jobstore.Store, _ jobstore.WorkItem, _ *fakeOneShot) {
		s.claimSession("sess-sum00001")
	}, CauseManual)
	fail("fail project busy", func(s *Service, st *jobstore.Store, w jobstore.WorkItem, _ *fakeOneShot) {
		a, _, _ := st.GetAgentSession("sess-sum00001")
		c := s.cfg()
		project, _ := s.resolveProject(c, c.SummarizerAgentName(), w.ProjectKey, a.ProjectKey)
		s.claimSession(summarizerProjectKey(project))
	}, CauseManual)
	{
		svcF, stF, wF, _ := setup("still not json")
		req, _ := stF.CreateWorkRequest(jobstore.WorkRequestInput{WorkItemID: wF.ID, Kind: jobstore.WorkRequestSummarize})
		_, err := svcF.RunSummarize(context.Background(), wF.ID, SummarizeOpts{Cause: CauseRequest, RequestID: req.ID})
		r, _, _ := stF.GetWorkRequest(req.ID)
		runs, _ := stF.ListWorkSummaries(wF.ID, 5)
		sect("fail bad output", "ERR "+fmt.Sprint(err)+" | ledger "+r.Error, "run log: "+runs[0].Error)
		j, _ := stF.ListWorkJournal(wF.ID, 10, 0)
		var jl []string
		for _, e := range j {
			if strings.Contains(e.Text, "JSON") || strings.Contains(e.Text, "整理") {
				jl = append(jl, e.Text)
			}
		}
		sect("fail bad output journal", jl...)
	}
	// "创建工作项" is the store's own journal line (jobstore, not migrated yet).
	return goldenIDs.ReplaceAllString(strings.ReplaceAll(sb.String(), "创建工作项", "<created>"), "<ID>")
}

func TestSummarizerTextGolden(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.ZhCN, i18n.EN} {
		t.Run(string(lang), func(t *testing.T) {
			got := summarizerTextOutputs(t, lang)
			path := filepath.Join("testdata", "i18n_golden", "summarizer."+string(lang)+".txt")
			_, statErr := os.Stat(path)
			if (lang == i18n.EN && os.Getenv("GOFER_GOLDEN_UPDATE") == "1") || (lang == i18n.ZhCN && statErr != nil) {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Errorf("summarizer text for %s changed\n--- got ---\n%s\n--- want ---\n%s", lang, got, want)
			}
			if lang == i18n.EN {
				for i, l := range strings.Split(got, "\n") {
					if han.MatchString(l) {
						t.Errorf("line %d of the English text carries CJK: %s", i+1, l)
					}
				}
			}
		})
	}
}
