package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// W2a REST faces of the summarizer: the manual 「整理」, the suggestion adopt / dismiss
// pair and the settings read / write behind the console's work section.

// POST /v1/work-items/{id}/summarize — tidy the item up now. It records a summarize
// request in the ledger and runs in the background; the response is that request.
func (s *Server) handleWorkSummarize(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	if callerKindFromCtx(c) == callerKindJob && !callerIsSteward(c) {
		writeError(c, http.StatusForbidden, "job credential may not trigger a tidy-up", "only a person can trigger a work tidy-up")
		return
	}
	req, err := s.work.TidyNow(c.Param("id"), workBy(c))
	if err != nil {
		switch {
		case errors.Is(err, work.ErrNoSession):
			writeError(c, http.StatusConflict, "no session to read", "this work item has no current session to tidy up from")
		case errors.Is(err, work.ErrSummarizerUnavailable):
			writeError(c, http.StatusConflict, "summarizer unavailable", err.Error())
		default:
			writeWorkError(c, err, "summarize work item")
		}
		return
	}
	c.JSON(http.StatusOK, map[string]any{"request": req})
}

func (s *Server) respondSuggestion(c *rux.Context, id string, err error, what string) {
	if err != nil {
		switch {
		case errors.Is(err, jobstore.ErrSuggestionNotFound):
			writeError(c, http.StatusNotFound, what+" failed", err.Error())
		case errors.Is(err, work.ErrSuggestionConflict):
			writeError(c, http.StatusConflict, what+" failed", err.Error())
		default:
			writeWorkError(c, err, what)
		}
		return
	}
	s.respondWorkDetail(c, id, http.StatusOK)
}

// POST /v1/work-items/{id}/suggestions/{field}/accept
func (s *Server) handleAcceptWorkSuggestion(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	_, err := s.work.AcceptSuggestion(c.Param("id"), c.Param("field"), workBy(c))
	s.respondSuggestion(c, c.Param("id"), err, "accept suggestion")
}

// POST /v1/work-items/{id}/suggestions/{field}/dismiss
func (s *Server) handleDismissWorkSuggestion(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	err := s.work.DismissSuggestion(c.Param("id"), c.Param("field"), workBy(c))
	s.respondSuggestion(c, c.Param("id"), err, "dismiss suggestion")
}

// workSettingsView is the effective `work:` block as the console shows it.
type workSettingsView struct {
	SummarizerAgent         string   `json:"summarizer_agent"`
	SummarizerArgs          []string `json:"summarizer_args"`
	SummarizerProject       string   `json:"summarizer_project"`
	SummarizeEnabled        bool     `json:"summarize_enabled"`
	SummarizeIdleMin        int      `json:"summarize_idle_min"`
	SummarizeMinIntervalMin int      `json:"summarize_min_interval_min"`
	SummarizeDailyLimit     int      `json:"summarize_daily_limit"`
	AutoHandoff             bool     `json:"auto_handoff"`
	RequestTimeoutMin       int      `json:"request_timeout_min"`
	DigestEnabled           bool     `json:"digest_enabled"`
	DigestTime              string   `json:"digest_time"`
}

func workSettingsOf(w config.WorkConfig) workSettingsView {
	h, m := w.DigestClock()
	args := w.SummarizerArgsOrDefault()
	if args == nil {
		args = []string{}
	}
	return workSettingsView{
		SummarizerAgent: w.SummarizerAgentName(), SummarizerArgs: args, SummarizerProject: w.SummarizerProject,
		SummarizeEnabled: w.SummarizeOn(), SummarizeIdleMin: int(w.SummarizeIdle().Minutes()),
		SummarizeMinIntervalMin: int(w.SummarizeMinInterval().Minutes()), SummarizeDailyLimit: dailyForView(w),
		AutoHandoff: w.AutoHandoffOn(), RequestTimeoutMin: int(w.RequestTimeout().Minutes()),
		DigestEnabled: w.WorkDigestEnabled(), DigestTime: twoDigits(h) + ":" + twoDigits(m),
	}
}

// dailyForView shows "unlimited" as -1 (what the field takes) rather than 0.
func dailyForView(w config.WorkConfig) int {
	if n := w.SummarizeDaily(); n > 0 {
		return n
	}
	return -1
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + string(rune('0'+n))
	}
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

// GET /v1/work-items/summarizer — the summarizer's status plus the effective work
// settings (what the console's settings page renders).
func (s *Server) handleWorkSummarizerStatus(c *rux.Context) {
	if !s.workReady(c) || !workNotAWorker(c) {
		return
	}
	st := s.work.SummarizerStatus()
	cfg := config.WorkConfig{}
	if s.projects != nil {
		if live := s.projects.Config(); live != nil {
			cfg = live.Work
		}
	}
	c.JSON(http.StatusOK, map[string]any{"status": st, "settings": workSettingsOf(cfg)})
}

// workSettingsPatch is the partial body of PUT /v1/config/work; absent fields stay.
type workSettingsPatch struct {
	SummarizerAgent         *string   `json:"summarizer_agent"`
	SummarizerArgs          *[]string `json:"summarizer_args"`
	SummarizerProject       *string   `json:"summarizer_project"`
	SummarizeEnabled        *bool     `json:"summarize_enabled"`
	SummarizeIdleMin        *int      `json:"summarize_idle_min"`
	SummarizeMinIntervalMin *int      `json:"summarize_min_interval_min"`
	SummarizeDailyLimit     *int      `json:"summarize_daily_limit"`
	AutoHandoff             *bool     `json:"auto_handoff"`
	RequestTimeoutMin       *int      `json:"request_timeout_min"`
	DigestEnabled           *bool     `json:"digest_enabled"`
	DigestTime              *string   `json:"digest_time"`
}

func (p workSettingsPatch) apply(w *config.WorkConfig) []string {
	var set []string
	add := func(name string) { set = append(set, name) }
	if p.SummarizerAgent != nil {
		w.SummarizerAgent = strings.TrimSpace(*p.SummarizerAgent)
		add("summarizer_agent")
	}
	if p.SummarizerArgs != nil {
		w.SummarizerArgs = append([]string(nil), (*p.SummarizerArgs)...)
		add("summarizer_args")
	}
	if p.SummarizerProject != nil {
		w.SummarizerProject = strings.TrimSpace(*p.SummarizerProject)
		add("summarizer_project")
	}
	if p.SummarizeEnabled != nil {
		v := *p.SummarizeEnabled
		w.SummarizeEnabled = &v
		add("summarize_enabled")
	}
	if p.SummarizeIdleMin != nil {
		w.SummarizeIdleMin = *p.SummarizeIdleMin
		add("summarize_idle_min")
	}
	if p.SummarizeMinIntervalMin != nil {
		w.SummarizeMinIntervalMin = *p.SummarizeMinIntervalMin
		add("summarize_min_interval_min")
	}
	if p.SummarizeDailyLimit != nil {
		w.SummarizeDailyLimit = *p.SummarizeDailyLimit
		add("summarize_daily_limit")
	}
	if p.AutoHandoff != nil {
		v := *p.AutoHandoff
		w.AutoHandoff = &v
		add("auto_handoff")
	}
	if p.RequestTimeoutMin != nil {
		w.RequestTimeoutMin = *p.RequestTimeoutMin
		add("request_timeout_min")
	}
	if p.DigestEnabled != nil {
		v := *p.DigestEnabled
		w.DigestEnabled = &v
		add("digest_enabled")
	}
	if p.DigestTime != nil {
		w.DigestTime = strings.TrimSpace(*p.DigestTime)
		add("digest_time")
	}
	return set
}

// PUT /v1/config/work — a PARTIAL update of the work: block through the config write
// transaction (admin only, validated, saved, hot-applied on the next tick).
func (s *Server) handlePutConfigWork(c *rux.Context) {
	caller, ok := s.configWriteCaller(c)
	if !ok {
		return
	}
	cw, ok := s.configWriter(c)
	if !ok {
		return
	}
	var body workSettingsPatch
	if err := c.BindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	var applied []string
	err := cw.Update(func(next *config.Config) error {
		applied = body.apply(&next.Work)
		if len(applied) == 0 {
			return &configWriteError{status: http.StatusBadRequest, msg: "empty request body", detail: "name at least one work setting"}
		}
		if verr := validateCandidate(next); verr != nil {
			return &configWriteError{status: http.StatusBadRequest, msg: "invalid config", detail: verr.Error()}
		}
		return nil
	})
	if err != nil {
		s.writeConfigError(c, err)
		return
	}
	s.recordConfigUpdate(caller, "work", "", applied)
	c.JSON(http.StatusOK, configWriteResp{Status: "ok", Section: "work", Reloaded: true, Fields: applied})
}
