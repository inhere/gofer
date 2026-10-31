package work

import (
	"context"
	"testing"

	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/jobstore"
)

// The shims give the golden test one call shape across the S3e migration.

func shimCtx(lang i18n.Lang) context.Context { return context.Background() }

func shimGatherMaterial(svc *Service, w jobstore.WorkItem, a jobstore.AgentSession) (string, bool) {
	return svc.gatherMaterial(context.Background(), w, a)
}

func shimBuildPrompt(svc *Service, w jobstore.WorkItem, material string, degraded, resumed bool) string {
	return svc.buildPrompt(w, material, degraded, resumed)
}

func shimStatus(svc *Service) SummarizerStatus { return svc.SummarizerStatus(context.Background()) }

func shimSessionTail(t *testing.T, svc *Service, st *jobstore.Store) string {
	a, err := st.UpsertAgentSession(jobstore.AgentSession{SessionID: "sess-tail0001", Agent: "claude", ProjectKey: "p1", State: jobstore.SessionIdle, ProgressText: "progress line"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.TouchAgentSession(a.SessionID, jobstore.SessionHeartbeat{State: jobstore.SessionIdle, LastMessage: "last msg", ProgressText: "progress line"}); err != nil {
		t.Fatal(err)
	}
	r, err := svc.SessionTail(context.Background(), a.SessionID, 0)
	if err != nil {
		t.Fatal(err)
	}
	return r.Text
}
