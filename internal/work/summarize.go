package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work/transcript"
)

// Passive tidy-up of work items (W2a, design §14.2): read the tail of a session's
// transcript (read-only, never disturbing the session), ask a cheap model — a one-shot,
// read-only, tool-less job — for goal / progress / blocker / next as a fixed JSON, and
// write it back without ever overwriting what a person or the session itself wrote:
// such fields become "整理建议" the person adopts or dismisses; the status is only ever
// a hint.

// Why a tidy-up runs.
const (
	CauseManual  = "manual"  // the 「整理」 button / `gofer work summarize`
	CauseRequest = "request" // a report / hand-over request that could not be answered
	CauseAuto    = "auto"    // the passive scan (idle / offline / ended with new activity)
)

const (
	transcriptBytes  = 128 * 1024
	summarizeTimeout = 4 * time.Minute
	maxParallelRuns  = 2
	journalContextN  = 12
)

// ErrNoTranscript is returned by a TranscriptSource that cannot supply a transcript
// (none registered, an old worker, the file is gone); the tidy-up then degrades to the
// session's last message, progress line and the item's journal.
var ErrNoTranscript = errors.New("transcript unavailable")

// TranscriptSource reads the tail of a session's registered transcript (the entry layer
// adapts the local disk and the worker transcript_tail frame; the service never knows
// where the file lives).
type TranscriptSource interface {
	ReadTail(ctx context.Context, a jobstore.AgentSession, maxBytes int64) ([]byte, error)
}

// DialectSource is an optional capability of a TranscriptSource: the agent's configured
// transcript_dialect ("" = not configured). Parsing happens on the server for every
// source (a worker's transcript_tail frame only carries raw bytes), so the dialect never
// needs to cross the wire.
type DialectSource interface {
	ConfiguredDialect(agent string) string
}

// dialectFor resolves how to parse a session's transcript: configured dialect, then the
// agent name, then (Parse) content sniffing.
func (s *Service) dialectFor(agentKey string) string {
	cfg := ""
	if ds, ok := s.transcripts.(DialectSource); ok {
		cfg = ds.ConfiguredDialect(agentKey)
	}
	return transcript.Resolve(cfg, agentKey)
}

// OneShotRequest is the summarizer job: a read-only, tool-less run of a cli-agent.
type OneShotRequest struct {
	Agent      string
	Args       []string
	ProjectKey string
	Prompt     string
	Title      string
	TimeoutSec int
}

// OneShotResult is what the job produced.
type OneShotResult struct {
	JobID  string
	Output string
}

// OneShot runs the summarizer job (the entry layer adapts job.Service).
type OneShot interface {
	// Check reports why the agent cannot be used ("" nil = usable).
	Check(agent string) error
	Run(ctx context.Context, r OneShotRequest) (OneShotResult, error)
}

// ProjectChooser is the optional second face of a OneShot: it lets the service pick the
// project the summarizer job runs in. A OneShot without it keeps the plain order (the
// first non-empty candidate, else `default`) without checking anything.
type ProjectChooser interface {
	// ProjectUsable reports whether the project exists and admits the agent on the
	// built-in local runner (the summarizer job is always local, read-only).
	ProjectUsable(key, agent string) bool
	// ProjectDir is the directory a job of that project runs in ("" when unknown).
	ProjectDir(key string) string
}

// Summarizer project sources, as reported by SummarizerStatus.ProjectSource.
const (
	ProjectSourceConfig  = "config"  // work.summarizer_project is set
	ProjectSourceItem    = "item"    // the work item's own project
	ProjectSourceDefault = "default" // the built-in / declared `default` project
)

// resolveProject picks the project of one summarizer job: work.summarizer_project
// (when set) → the first candidate (the work item's / its session's own project) that
// admits the agent and the local runner → the `default` project. It returns the key and
// where it came from.
func (s *Service) resolveProject(c config.WorkConfig, agent string, candidates ...string) (string, string) {
	if p := strings.TrimSpace(c.SummarizerProject); p != "" {
		return p, ProjectSourceConfig
	}
	chooser, _ := s.oneShot.(ProjectChooser)
	for _, cand := range candidates {
		cand = strings.TrimSpace(cand)
		if cand == "" {
			continue
		}
		if chooser == nil || chooser.ProjectUsable(cand, agent) {
			return cand, ProjectSourceItem
		}
	}
	return config.DefaultProjectKey, ProjectSourceDefault
}

// SetTranscriptSource injects the transcript reader (nil = always degraded input).
func (s *Service) SetTranscriptSource(t TranscriptSource) { s.transcripts = t }

// SetOneShot injects the summarizer job runner (nil = tidy-up unavailable).
func (s *Service) SetOneShot(o OneShot) { s.oneShot = o }

// SummarizerStatus is what the settings page / `GET /v1/work-items/summarizer` shows.
type SummarizerStatus struct {
	// Enabled is the automatic (passive) tidy-up switch; the manual action works
	// whenever Available.
	Enabled bool     `json:"enabled"`
	Agent   string   `json:"agent"`
	Args    []string `json:"args"`
	Project string   `json:"project,omitempty"`
	// EffectiveProject / EffectiveDir / ProjectSource say where a job runs when no
	// work item decides: work.summarizer_project, else the `default` project.
	EffectiveProject string `json:"effective_project,omitempty"`
	EffectiveDir     string `json:"effective_dir,omitempty"`
	ProjectSource    string `json:"project_source,omitempty"`
	Available        bool   `json:"available"`
	// Reason says why it is not available (or degraded) in plain words.
	Reason      string `json:"reason,omitempty"`
	IdleMin     int    `json:"idle_min"`
	IntervalMin int    `json:"interval_min"`
	DailyLimit  int    `json:"daily_limit"`
	DailyUsed   int    `json:"daily_used"`
	AutoHandoff bool   `json:"auto_handoff"`
	TimeoutMin  int    `json:"request_timeout_min"`
}

// SummarizerStatus reports the effective summarizer configuration and availability.
func (s *Service) SummarizerStatus() SummarizerStatus {
	c := s.cfg()
	st := SummarizerStatus{
		Enabled: c.SummarizeOn(), Agent: c.SummarizerAgentName(), Args: c.SummarizerArgsOrDefault(), Project: c.SummarizerProject,
		IdleMin: int(c.SummarizeIdle() / time.Minute), IntervalMin: int(c.SummarizeMinInterval() / time.Minute),
		DailyLimit: c.SummarizeDaily(), AutoHandoff: c.AutoHandoffOn(), TimeoutMin: int(c.RequestTimeout() / time.Minute),
	}
	if n, err := s.store.CountAutoWorkSummaries(s.startOfDay()); err == nil {
		st.DailyUsed = n
	}
	st.EffectiveProject, st.ProjectSource = s.resolveProject(c, st.Agent)
	if chooser, ok := s.oneShot.(ProjectChooser); ok {
		st.EffectiveDir = chooser.ProjectDir(st.EffectiveProject)
	}
	switch {
	case s.oneShot == nil:
		st.Reason = "整理功能未接入（server 没有提供一次性 job 通道）"
	default:
		if err := s.oneShot.Check(st.Agent); err != nil {
			st.Reason = err.Error()
		} else if ch, ok := s.oneShot.(ProjectChooser); ok && !ch.ProjectUsable(st.EffectiveProject, st.Agent) {
			st.Reason = fmt.Sprintf("整理用的项目 %q 不可用（不存在，或不允许 agent %s / 本机 runner）", st.EffectiveProject, st.Agent)
		} else {
			st.Available = true
		}
	}
	if st.Args == nil {
		st.Args = []string{}
	}
	return st
}

func (s *Service) startOfDay() int64 {
	now := s.nowFn()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
}

// SummarizeOpts describes one tidy-up run.
type SummarizeOpts struct {
	Cause string
	// RequestID is the ledger row to settle (answered / failed) when the run ends.
	RequestID string
	// SessionID picks the session to read ("" = the most recently seen current one).
	SessionID string
	By        string
}

// SummarizeResult is what one tidy-up did.
type SummarizeResult struct {
	RunID      int64    `json:"run_id,omitempty"`
	JobID      string   `json:"job_id,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	Applied    []string `json:"applied"`
	Suggested  []string `json:"suggested"`
	StatusHint string   `json:"status_hint,omitempty"`
	Confidence float64  `json:"confidence,omitempty"`
	// Milestone is the summarizer's optional one-liner, journaled as a milestone.
	Milestone string `json:"milestone,omitempty"`
	// Degraded says the transcript could not be read and the input was the session's
	// last message, progress line and the item's journal.
	Degraded bool `json:"degraded,omitempty"`
}

// StartSummarize runs a tidy-up in the background and settles its request when done.
func (s *Service) StartSummarize(itemID string, o SummarizeOpts) {
	if o.RequestID != "" {
		s.markReqInflight(o.RequestID, true)
	}
	if !s.spawn(func() {
		defer func() {
			if o.RequestID != "" {
				s.markReqInflight(o.RequestID, false)
			}
		}()
		ctx, cancel := context.WithTimeout(s.BackgroundContext(), summarizeTimeout+time.Minute)
		defer cancel()
		if _, err := s.RunSummarize(ctx, itemID, o); err != nil {
			slog.Info("work.summarize_failed", "event", "work.summarize_failed", "id", itemID, "cause", o.Cause, "err", err)
		}
	}) && o.RequestID != "" {
		s.markReqInflight(o.RequestID, false) // closed: nothing will run
	}
}

func (s *Service) markReqInflight(id string, on bool) {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	if s.reqInflight == nil {
		s.reqInflight = map[string]bool{}
	}
	if on {
		s.reqInflight[id] = true
	} else {
		delete(s.reqInflight, id)
	}
}

func (s *Service) summarizing(requestID string) bool {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	return s.reqInflight[requestID]
}

// claimSession reserves a session for one tidy-up at a time.
func (s *Service) claimSession(sid string) bool {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	if s.sumSessions == nil {
		s.sumSessions = map[string]bool{}
	}
	if s.sumSessions[sid] {
		return false
	}
	s.sumSessions[sid] = true
	return true
}

func (s *Service) releaseSession(sid string) {
	s.sumMu.Lock()
	delete(s.sumSessions, sid)
	s.sumMu.Unlock()
}

func (s *Service) runningCount() int {
	s.sumMu.Lock()
	defer s.sumMu.Unlock()
	return len(s.sumSessions)
}

// failRequest settles a summarize request as failed (when there is one).
func (s *Service) failRequest(rid, reason string) {
	if rid == "" {
		return
	}
	_, _, _ = s.store.MarkWorkRequest(rid, jobstore.WorkRequestFailed, "", reason, jobstore.WorkRequestPending)
}

// pickSession chooses the session a tidy-up reads.
func (s *Service) pickSession(itemID, want string) (jobstore.AgentSession, bool) {
	rows, err := s.store.ListWorkItemSessions(itemID)
	if err != nil {
		return jobstore.AgentSession{}, false
	}
	var best jobstore.AgentSession
	found := false
	for _, r := range rows {
		if r.Role != jobstore.WorkSessionCurrent || (want != "" && r.SessionID != want) {
			continue
		}
		a, ok, err := s.store.GetAgentSession(r.SessionID)
		if err != nil || !ok {
			continue
		}
		if !found || a.LastSeenAt > best.LastSeenAt {
			best, found = a, true
		}
	}
	return best, found
}

// RunSummarize runs one tidy-up of an item synchronously.
func (s *Service) RunSummarize(ctx context.Context, itemID string, o SummarizeOpts) (SummarizeResult, error) {
	var res SummarizeResult
	fail := func(reason string) (SummarizeResult, error) {
		s.failRequest(o.RequestID, reason)
		return res, errors.New(reason)
	}
	w, ok, err := s.store.GetWorkItem(itemID)
	if err != nil {
		return fail(err.Error())
	}
	if !ok {
		return fail(jobstore.ErrWorkItemNotFound.Error())
	}
	c := s.cfg()
	agentName := c.SummarizerAgentName()
	if s.oneShot == nil {
		return fail("整理器未接入")
	}
	if err := s.oneShot.Check(agentName); err != nil {
		return fail("整理器不可用：" + err.Error())
	}
	a, haveSession := s.pickSession(w.ID, o.SessionID)
	if !haveSession {
		return fail("工作项没有可读取的会话")
	}
	res.SessionID = a.SessionID
	if o.Cause != CauseManual {
		if lim := c.SummarizeDaily(); lim > 0 {
			if n, _ := s.store.CountAutoWorkSummaries(s.startOfDay()); n >= lim {
				return fail(fmt.Sprintf("已达每日整理上限（%d 次）", lim))
			}
		}
	}
	if !s.claimSession(a.SessionID) {
		return fail("该会话已有整理在进行")
	}
	defer s.releaseSession(a.SessionID)

	runID, err := s.store.BeginWorkSummary(w.ID, a.SessionID, a.LastSeenAt, o.Cause)
	if err != nil {
		return fail(err.Error())
	}
	res.RunID = runID
	finish := func(state, jobID, errText string) {
		if err := s.store.FinishWorkSummary(runID, state, jobID, errText); err != nil {
			slog.Warn("work.summary_log_failed", "event", "work.summary_log_failed", "id", w.ID, "err", err)
		}
	}

	material, degraded := s.gatherMaterial(ctx, w, a)
	res.Degraded = degraded
	project, _ := s.resolveProject(c, agentName, w.ProjectKey, a.ProjectKey)
	req := OneShotRequest{
		Agent: agentName, Args: c.SummarizerArgsOrDefault(), ProjectKey: project,
		Prompt: buildPrompt(w, material, degraded), Title: "work summarizer · " + shortID(w.ID),
		TimeoutSec: int(summarizeTimeout / time.Second),
	}
	var out SummaryOut
	var jobID string
	var perr error
	for attempt := 0; attempt < 2; attempt++ {
		r, rerr := s.oneShot.Run(ctx, req)
		if r.JobID != "" {
			jobID = r.JobID
		}
		if rerr != nil {
			perr = rerr
			break
		}
		out, perr = ParseSummary(r.Output)
		if perr == nil {
			break
		}
		req.Prompt = buildPrompt(w, material, degraded) + "\n\n上一次的输出不是合法的 JSON。这一次只输出一个 JSON 对象，不要任何其它文字。"
	}
	res.JobID = jobID
	if perr != nil {
		finish(jobstore.SummaryFailed, jobID, perr.Error())
		s.journalSteward(w.ID, SummarizerBy(agentName), "整理失败："+perr.Error())
		return fail("整理失败：" + perr.Error())
	}
	applied, suggested, hint := s.applySummary(w.ID, out, SummarizerBy(agentName), jobID)
	res.Applied, res.Suggested, res.StatusHint, res.Confidence = applied, suggested, hint, out.Confidence
	finish(jobstore.SummaryOK, jobID, "")
	text := "整理完成"
	if degraded {
		text += "（会话记录读不到，依据最后一条消息与进度）"
	}
	if len(applied) > 0 {
		text += "；已更新：" + strings.Join(applied, "、")
	}
	if len(suggested) > 0 {
		text += "；待采纳建议：" + strings.Join(suggested, "、")
	}
	if len(applied) == 0 && len(suggested) == 0 {
		text += "；没有新内容"
	}
	s.journalSteward(w.ID, SummarizerBy(agentName), text)
	// WORK-06: the one thing since the last tidy-up worth a line on the lane.
	if out.Milestone != "" {
		if _, err := s.store.AppendWorkJournalLevel(w.ID, jobstore.WorkJournalSteward, out.Milestone,
			SummarizerBy(agentName), jobstore.WorkLevelMilestone); err != nil {
			slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", w.ID, "err", err)
		}
		res.Milestone = out.Milestone
	}
	if o.RequestID != "" {
		_, _, _ = s.store.MarkWorkRequest(o.RequestID, jobstore.WorkRequestAnswered, "", "", jobstore.WorkRequestPending)
	}
	return res, nil
}

func (s *Service) journalSteward(id, by, text string) {
	if _, err := s.store.AppendWorkJournal(id, jobstore.WorkJournalSteward, text, by); err != nil {
		slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", id, "err", err)
	}
}

// gatherMaterial builds the model's input: the transcript tail when it can be read,
// otherwise the session's last message and progress line plus the journal.
func (s *Service) gatherMaterial(ctx context.Context, w jobstore.WorkItem, a jobstore.AgentSession) (text string, degraded bool) {
	if s.transcripts != nil && strings.TrimSpace(a.Transcript) != "" {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		raw, err := s.transcripts.ReadTail(rctx, a, transcriptBytes)
		cancel()
		if err == nil {
			turns := transcript.Parse(s.dialectFor(a.Agent), raw)
			if t := transcript.Format(turns, transcript.FormatOpts{}); strings.TrimSpace(t) != "" {
				return t, false
			}
		} else {
			slog.Debug("work.transcript_unavailable", "event", "work.transcript_unavailable", "session", a.SessionID, "err", err)
		}
	}
	var b strings.Builder
	if m := strings.TrimSpace(a.LastMessage); m != "" {
		b.WriteString("会话最后一条消息：\n" + clipRunes(m, 3000) + "\n\n")
	}
	if p := strings.TrimSpace(a.ProgressText); p != "" {
		b.WriteString("会话进度行：" + clipRunes(p, 500) + "\n\n")
	}
	if title := strings.TrimSpace(a.Title); title != "" {
		b.WriteString("会话标题（最近一次提问）：" + clipRunes(title, 300) + "\n\n")
	}
	if j, err := s.store.ListWorkJournal(w.ID, journalContextN, 0); err == nil && len(j) > 0 {
		b.WriteString("工作项日志（最近）：\n")
		for _, e := range j {
			b.WriteString("- " + e.By + "：" + clipRunes(strings.ReplaceAll(e.Text, "\n", " "), 200) + "\n")
		}
	}
	return strings.TrimSpace(b.String()), true
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func buildPrompt(w jobstore.WorkItem, material string, degraded bool) string {
	var b strings.Builder
	b.WriteString("你是一个只读的“工作整理器”。下面给出一个工作项当前已记录的内容，以及它所属终端会话的最近对话" +
		"（或在读不到对话时的最后一条消息）。请据此提炼这件工作的真实状况。不要调用任何工具，不要提问，不要解释。\n\n")
	b.WriteString("只输出一个 JSON 对象，字段如下，值都是字符串（confidence 是 0 到 1 的数字）：\n")
	b.WriteString(`{"goal":"这件事要达成什么（一句话）","progress":"做到哪了（一两句话）","blocker_kind":"卡点类别，如 device/account/onsite/person/decision，没有卡点留空","blocker":"具体卡在什么上，没有卡点留空","next":"下一步该做什么（一句话）","status_hint":"active|needs_me|waiting_resource|needs_onsite|review|parked 之一，拿不准留空","milestone":"自上次整理以来值得留一笔的事（不超过 40 字），没有就留空","confidence":0.0}` + "\n\n")
	b.WriteString("规则：只写对话里有依据的内容；没有依据的字段留空字符串；用中文；status_hint 只是建议；" +
		"milestone 只写一件已经发生、值得记一笔的事（如“CSV 导出完成并通过测试”），没有新进展就留空，不要复述目标或下一步。\n\n")
	b.WriteString("【工作项当前记录】\n")
	fmt.Fprintf(&b, "标题：%s\n目标：%s\n状态：%s\n阻塞：%s\n下一步：%s\n摘要：%s\n\n",
		clipRunes(w.Title, 200), orNone(w.Goal), w.Status, orNone(strings.TrimSpace(w.BlockerKind+" "+w.BlockerText)), orNone(w.NextStep), orNone(w.Summary))
	if degraded {
		b.WriteString("【会话材料（读不到完整对话，只有以下信息）】\n")
	} else {
		b.WriteString("【会话最近对话（旧的在前）】\n")
	}
	b.WriteString(material)
	b.WriteString("\n")
	return b.String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（空）"
	}
	return clipRunes(s, 600)
}

// SummaryOut is the summarizer's fixed JSON.
type SummaryOut struct {
	Goal       string
	Progress   string
	BlockerK   string
	Blocker    string
	Next       string
	StatusHint string
	// Milestone is optional (WORK-06): a <=40-rune line worth keeping on the timeline,
	// "" when nothing new happened (or an older summarizer omitted the key).
	Milestone  string
	Confidence float64
}

// maxMilestoneRunes caps the summarizer's milestone line.
const maxMilestoneRunes = 40

// capMilestone folds whitespace and keeps the line within maxMilestoneRunes (an ellipsis
// included).
func capMilestone(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxMilestoneRunes {
		return string(r[:maxMilestoneRunes-1]) + "…"
	}
	return s
}

// ParseSummary extracts the summary object from a job's output. It is forgiving about
// what surrounds the object (code fences, a stream-json result envelope, chatter) but
// not about its content: an output without any recognised key is an error.
func ParseSummary(output string) (SummaryOut, error) {
	var cands []string
	for _, ln := range strings.Split(output, "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasPrefix(ln, "{") {
			continue
		}
		var env map[string]any
		if json.Unmarshal([]byte(ln), &env) == nil {
			if t, _ := env["type"].(string); t == "result" {
				if r, ok := env["result"].(string); ok && strings.TrimSpace(r) != "" {
					cands = append(cands, r)
				}
			}
		}
	}
	cands = append(cands, output)
	for i := len(cands) - 1; i >= 0; i-- {
		if m := firstSummaryObject(cands[i]); m != nil {
			return summaryFromMap(m), nil
		}
	}
	return SummaryOut{}, errors.New("输出里没有可解析的 JSON 对象")
}

var summaryKeys = []string{"goal", "progress", "blocker_kind", "blocker", "next", "status_hint", "summary", "milestone"}

func firstSummaryObject(s string) map[string]any {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			continue
		}
		for _, k := range summaryKeys {
			if _, ok := m[k]; ok {
				return m
			}
		}
	}
	return nil
}

func strField(m map[string]any, k string) string {
	switch v := m[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func summaryFromMap(m map[string]any) SummaryOut {
	o := SummaryOut{
		Goal: strField(m, "goal"), Progress: strField(m, "progress"), BlockerK: strField(m, "blocker_kind"),
		Blocker: strField(m, "blocker"), Next: strField(m, "next"), StatusHint: strings.ToLower(strField(m, "status_hint")),
		Milestone: capMilestone(strField(m, "milestone")),
	}
	if o.Progress == "" {
		o.Progress = strField(m, "summary")
	}
	switch v := m["confidence"].(type) {
	case float64:
		o.Confidence = v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			o.Confidence = f
		}
	}
	if o.Confidence < 0 {
		o.Confidence = 0
	}
	if o.Confidence > 1 {
		o.Confidence = 1
	}
	return o
}

// isSummarizerOwned reports whether the field's last writer was a summarizer.
func isSummarizerOwned(src jobstore.WorkFieldSource, known bool) bool {
	return known && ActorKind(src.By) == ActorSummarizer
}

// applySummary writes the result back under the rules: fill an empty field or refresh
// one the summarizer itself wrote; otherwise store a suggestion. It returns what was
// applied, what was suggested and the status hint (also stored as a suggestion).
func (s *Service) applySummary(id string, out SummaryOut, by, jobID string) (applied, suggested []string, hint string) {
	applied, suggested = []string{}, []string{}
	w, ok, err := s.store.GetWorkItem(id)
	if err != nil || !ok {
		return
	}
	srcs, _ := s.store.WorkFieldSources(id)
	// Quiet: one `steward` journal line summarises the run, so the per-field status
	// lines would only repeat it.
	patch := jobstore.WorkItemPatch{Quiet: true}
	changed := false
	type field struct {
		name, label string
		cur         string
		val         string
		set         func(string)
		allowClear  bool
	}
	suggest := func(f, val, label string) {
		if st, _ := s.store.UpsertWorkSuggestion(jobstore.WorkSuggestion{
			WorkItemID: id, Field: f, Value: val, Confidence: out.Confidence, By: by, JobID: jobID,
		}); st {
			suggested = append(suggested, label)
		}
	}
	fields := []field{
		{jobstore.WorkFieldGoal, "目标", w.Goal, out.Goal, func(v string) { patch.Goal = &v }, false},
		{jobstore.WorkFieldBlocker, "阻塞", w.BlockerText, out.Blocker, func(v string) { patch.BlockerText = &v }, true},
		{jobstore.WorkFieldNext, "下一步", w.NextStep, out.Next, func(v string) { patch.NextStep = &v }, false},
		{jobstore.WorkFieldSummary, "摘要", w.Summary, out.Progress, func(v string) { patch.Summary = &v }, false},
	}
	for _, f := range fields {
		src, known := srcs[f.name]
		owned := isSummarizerOwned(src, known)
		switch {
		case f.val == "" && f.allowClear && f.cur != "" && owned:
			// The blocker is gone and it was the summarizer's own note: clear it.
			f.set("")
			if f.name == jobstore.WorkFieldBlocker {
				k := ""
				patch.BlockerKind = &k
			}
			applied = append(applied, f.label+"（已清除）")
			changed = true
		case f.val == "" || f.val == f.cur:
			// Nothing new; a stale suggestion for the same field no longer applies.
			if f.val == f.cur && f.val != "" {
				_ = s.store.DeleteWorkSuggestion(id, suggestionField(f.name))
			}
		case f.cur == "" || owned:
			f.set(f.val)
			if f.name == jobstore.WorkFieldBlocker && out.BlockerK != "" {
				k := out.BlockerK
				patch.BlockerKind = &k
			}
			applied = append(applied, f.label)
			changed = true
			_ = s.store.DeleteWorkSuggestion(id, suggestionField(f.name))
		default:
			suggest(suggestionField(f.name), f.val, f.label)
			if f.name == jobstore.WorkFieldBlocker && out.BlockerK != "" && out.BlockerK != w.BlockerKind {
				suggest(jobstore.SuggestBlockerKind, out.BlockerK, "阻塞类型")
			}
		}
	}
	if changed {
		if _, _, err := s.store.UpdateWorkItem(id, patch, 0, by); err != nil {
			slog.Warn("work.summary_apply_failed", "event", "work.summary_apply_failed", "id", id, "err", err)
			applied = []string{}
		}
	}
	if h := out.StatusHint; h != "" && jobstore.ValidWorkStatus(h) && !jobstore.WorkStatusFinal(h) && h != w.Status {
		hint = h
		suggest(jobstore.SuggestStatusHint, h, "状态建议")
	}
	sort.Strings(applied)
	return
}

func suggestionField(name string) string {
	// The field-source names and the suggestion names coincide today; keep the mapping
	// explicit so a rename of either side is one edit.
	return name
}

// ---------------------------------------------------------------- suggestions

// ErrSuggestionConflict is returned when a suggestion cannot be applied any more.
var ErrSuggestionConflict = errors.New("suggestion cannot be applied")

// AcceptSuggestion applies a pending suggestion as the person's own write (by is the
// person): it then counts as human-written, so a later tidy-up proposes instead of
// overwriting. A status_hint becomes the item's status (a person's status).
func (s *Service) AcceptSuggestion(id, field, by string) (jobstore.WorkItem, error) {
	by = s.normalizeBy(by)
	sg, ok, err := s.store.GetWorkSuggestion(id, field)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if !ok {
		return jobstore.WorkItem{}, jobstore.ErrSuggestionNotFound
	}
	var p jobstore.WorkItemPatch
	// Adopting any suggestion means a person has looked at the draft, so it leaves the
	// unsorted lane (the summarizer fills the goal itself; only the status may be left
	// for the person to adopt).
	if cur, found, _ := s.store.GetWorkItem(id); found && cur.Unsorted {
		f := false
		p.Unsorted = &f
	}
	v := sg.Value
	switch field {
	case jobstore.SuggestGoal:
		p.Goal = &v
	case jobstore.SuggestBlocker:
		p.BlockerText = &v
	case jobstore.SuggestBlockerKind:
		p.BlockerKind = &v
	case jobstore.SuggestNext:
		p.NextStep = &v
	case jobstore.SuggestSummary:
		p.Summary = &v
	case jobstore.SuggestStatusHint:
		// A final status is fine here: adopting is the person's own decision (the
		// completion write-back suggests done; nothing else ever stores a final hint).
		if !jobstore.ValidWorkStatus(v) {
			return jobstore.WorkItem{}, fmt.Errorf("%w: status %q", ErrSuggestionConflict, v)
		}
		p.Status = &v
	default:
		return jobstore.WorkItem{}, fmt.Errorf("%w: field %q", jobstore.ErrWorkInvalid, field)
	}
	w, err := s.Update(id, p, 0, by)
	if err != nil {
		return jobstore.WorkItem{}, err
	}
	if err := s.store.DeleteWorkSuggestion(id, field); err != nil {
		return jobstore.WorkItem{}, err
	}
	if _, err := s.store.AppendWorkJournalLevel(id, jobstore.WorkJournalNote, "采纳整理建议："+suggestionLabel(field)+"（来自 "+sg.By+"）", by, jobstore.WorkLevelDetail); err != nil {
		slog.Warn("work.journal_failed", "event", "work.journal_failed", "id", id, "err", err)
	}
	return w, nil
}

// DismissSuggestion drops a pending suggestion (the same proposal is not made again).
func (s *Service) DismissSuggestion(id, field, by string) error {
	by = s.normalizeBy(by)
	sg, ok, err := s.store.GetWorkSuggestion(id, field)
	if err != nil {
		return err
	}
	if !ok {
		return jobstore.ErrSuggestionNotFound
	}
	if err := s.store.DismissWorkSuggestion(id, field); err != nil {
		return err
	}
	_, _ = s.store.AppendWorkJournalLevel(id, jobstore.WorkJournalNote, "忽略整理建议："+suggestionLabel(field)+"（来自 "+sg.By+"）", by, jobstore.WorkLevelDetail)
	return nil
}

func suggestionLabel(field string) string {
	switch field {
	case jobstore.SuggestGoal:
		return "目标"
	case jobstore.SuggestBlocker:
		return "阻塞"
	case jobstore.SuggestBlockerKind:
		return "阻塞类型"
	case jobstore.SuggestNext:
		return "下一步"
	case jobstore.SuggestSummary:
		return "摘要"
	case jobstore.SuggestStatusHint:
		return "状态建议"
	}
	return field
}

// ---------------------------------------------------------------- passive scan

// scanSummarize starts tidy-ups for sessions that went quiet with new activity. It is
// the passive trigger (design §14.2): idle ≥ idle_min, or offline / ended, with activity
// since the last run; a session is tidied at most once per interval; automatic runs are
// capped per day. At most a couple run at once.
func (s *Service) scanSummarize(now time.Time) {
	c := s.cfg()
	if !c.SummarizeOn() || s.oneShot == nil {
		return
	}
	if err := s.oneShot.Check(c.SummarizerAgentName()); err != nil {
		return
	}
	_ = s.store.FailStaleWorkSummaries(now.Add(-summarizeTimeout - 2*time.Minute).Unix())
	budget := maxParallelRuns - s.runningCount()
	if budget <= 0 {
		return
	}
	used := 0
	if lim := c.SummarizeDaily(); lim > 0 {
		n, err := s.store.CountAutoWorkSummaries(s.startOfDay())
		if err != nil || n >= lim {
			return
		}
		if rest := lim - n; rest < budget {
			budget = rest
		}
	}
	items, err := s.store.ListWorkItems(WorkListOptsOpen())
	if err != nil {
		return
	}
	idle := int64(c.SummarizeIdle() / time.Second)
	gap := int64(c.SummarizeMinInterval() / time.Second)
	started := map[string]bool{}
	for _, w := range items {
		if used >= budget {
			return
		}
		rows, err := s.store.ListWorkItemSessions(w.ID)
		if err != nil {
			continue
		}
		for _, r := range rows {
			if used >= budget {
				return
			}
			if r.Role != jobstore.WorkSessionCurrent || started[r.SessionID] {
				continue
			}
			a, ok, err := s.store.GetAgentSession(r.SessionID)
			if err != nil || !ok || !quietEnough(a, now.Unix(), idle) {
				continue
			}
			last, has, err := s.store.LastWorkSummary(a.SessionID)
			if err != nil {
				continue
			}
			if has && (a.LastSeenAt <= last.ActivityAt || now.Unix()-last.At < gap) {
				continue
			}
			started[a.SessionID] = true
			used++
			s.StartSummarize(w.ID, SummarizeOpts{Cause: CauseAuto, SessionID: a.SessionID, By: SummarizerBy(c.SummarizerAgentName())})
		}
	}
}

// quietEnough reports whether a session is in a state worth a tidy-up now: idle (or
// waiting for the human) for at least idleSec, or gone (offline / ended).
func quietEnough(a jobstore.AgentSession, now, idleSec int64) bool {
	switch a.State {
	case jobstore.SessionOffline, jobstore.SessionEnded:
		return true
	case jobstore.SessionIdle, jobstore.SessionWaitingReply:
		return now-a.LastSeenAt >= idleSec
	}
	return false
}

// TidyNow is the manual trigger (the 「整理」 button, `gofer work summarize`): it records
// a summarize request, runs the tidy-up in the background and returns the request id.
func (s *Service) TidyNow(itemID, by string) (jobstore.WorkRequest, error) {
	w, ok, err := s.store.GetWorkItem(itemID)
	if err != nil {
		return jobstore.WorkRequest{}, err
	}
	if !ok {
		return jobstore.WorkRequest{}, jobstore.ErrWorkItemNotFound
	}
	a, have := s.pickSession(w.ID, "")
	if !have {
		return jobstore.WorkRequest{}, ErrNoSession
	}
	if st := s.SummarizerStatus(); !st.Available {
		return jobstore.WorkRequest{}, fmt.Errorf("%w: %s", ErrSummarizerUnavailable, st.Reason)
	}
	req, err := s.store.CreateWorkRequest(jobstore.WorkRequestInput{
		WorkItemID: w.ID, SessionID: a.SessionID, Kind: jobstore.WorkRequestSummarize, By: s.normalizeBy(by),
		Deadline: s.nowFn().Add(s.cfg().RequestTimeout()).Unix(),
	})
	if err != nil {
		return jobstore.WorkRequest{}, err
	}
	s.StartSummarize(w.ID, SummarizeOpts{Cause: CauseManual, RequestID: req.ID, SessionID: a.SessionID, By: by})
	return req, nil
}

// ErrSummarizerUnavailable is returned when no usable summarizer agent is configured.
var ErrSummarizerUnavailable = errors.New("work summarizer unavailable")
