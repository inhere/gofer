package workbench

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

const defaultWindow = 7 * 24 * time.Hour

type Store interface {
	LoadWorkbenchSnapshot(callerID string, since int64) (jobstore.WorkbenchSnapshot, error)
	ListWorkbenchThreadPrefs(callerID string) ([]jobstore.WorkbenchThreadPref, error)
	UpsertWorkbenchThreadPref(pref jobstore.WorkbenchThreadPref) error
	GetOrCreateWorkbenchSeenBaseline(callerID string, observedAt int64) (int64, error)
	SetWorkbenchSeenBaseline(callerID string, observedAt int64) (int64, error)
	ListJobs(query jobstore.ListQuery) ([]jobstore.JobRecord, error)
	GetJob(id string) (jobstore.JobRecord, bool, error)
}

type Jobs interface {
	ResumeJob(jobID, prompt, runner, callerID string) (job.JobResult, error)
}

type Relay interface {
	Say(sessionID, answer, callerID string) (jobstore.PlanDecision, error)
}

type Service struct {
	store Store
	jobs  Jobs
	relay Relay
	now   func() time.Time
}

func NewService(store Store, jobs Jobs, relay Relay) *Service {
	return &Service{store: store, jobs: jobs, relay: relay, now: time.Now}
}

type projection struct {
	thread    Thread
	attention *AttentionItem
}

func (s *Service) List(callerID string, query Query) (Response, error) {
	if s == nil || s.store == nil {
		return Response{}, ErrUnavailable
	}
	callerID = normalizeCaller(callerID)
	since := query.Since
	if since <= 0 {
		since = s.now().Add(-defaultWindow).Unix()
	}
	seenBaseline, err := s.store.GetOrCreateWorkbenchSeenBaseline(callerID, s.now().Unix())
	if err != nil {
		return Response{}, err
	}
	snapshot, err := s.store.LoadWorkbenchSnapshot(callerID, since)
	if err != nil {
		return Response{}, err
	}
	projections := projectThreads(snapshot, seenBaseline)

	groups := make(map[string]*ProjectGroup)
	attention := make([]AttentionItem, 0)
	needle := strings.ToLower(strings.TrimSpace(query.Q))
	for _, p := range projections {
		thread := p.thread
		if query.Project != "" && thread.ProjectKey != query.Project {
			continue
		}
		if query.Status != "" && thread.Status != query.Status {
			continue
		}
		if needle != "" && !threadMatches(thread, needle) {
			continue
		}
		group := ensureGroup(groups, thread.ProjectKey)
		group.Threads = append(group.Threads, thread)
		addThreadCount(&group.Counts, thread.Status)
		group.Status = higherStatus(group.Status, thread.Status)
		if p.attention != nil {
			attention = append(attention, *p.attention)
		}
	}

	planByID := make(map[string]jobstore.Plan)
	for _, plan := range snapshot.BlockedPlans {
		planByID[plan.PlanID] = plan
		if query.Project != "" && plan.ProjectKey != query.Project {
			continue
		}
		if plan.ProjectKey == "" {
			continue
		}
		group := ensureGroup(groups, plan.ProjectKey)
		group.Counts.OrphanBlocked++
		group.Status = higherStatus(group.Status, StatusBlocked)
	}
	for _, plan := range snapshot.DecisionPlans {
		planByID[plan.PlanID] = plan
	}
	for _, decision := range snapshot.OpenPlanDecisions {
		plan, ok := planByID[decision.PlanID]
		if !ok || plan.ProjectKey == "" || (query.Project != "" && plan.ProjectKey != query.Project) {
			continue
		}
		group := ensureGroup(groups, plan.ProjectKey)
		group.Counts.OrphanBlocked++
		group.Status = higherStatus(group.Status, StatusBlocked)
	}

	projects := make([]ProjectGroup, 0, len(groups))
	total := 0
	for _, group := range groups {
		sort.SliceStable(group.Threads, func(i, j int) bool {
			if group.Threads[i].Pinned != group.Threads[j].Pinned {
				return group.Threads[i].Pinned
			}
			if group.Threads[i].UpdatedAt != group.Threads[j].UpdatedAt {
				return group.Threads[i].UpdatedAt > group.Threads[j].UpdatedAt
			}
			return group.Threads[i].ID < group.Threads[j].ID
		})
		total += len(group.Threads)
		projects = append(projects, *group)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ProjectKey < projects[j].ProjectKey })
	sort.SliceStable(attention, func(i, j int) bool {
		if attention[i].WaitingSince != attention[j].WaitingSince {
			return attention[i].WaitingSince < attention[j].WaitingSince
		}
		return attention[i].ThreadID < attention[j].ThreadID
	})
	return Response{Projects: projects, Attention: attention, Total: total, Since: since}, nil
}

func projectThreads(snapshot jobstore.WorkbenchSnapshot, seenBaseline int64) []projection {
	prefs := make(map[string]jobstore.WorkbenchThreadPref, len(snapshot.Prefs))
	for _, pref := range snapshot.Prefs {
		prefs[pref.ThreadID] = pref
	}
	interactions := make(map[string][]job.Interaction)
	for _, rec := range snapshot.PendingInteractions {
		interactions[rec.JobID] = append(interactions[rec.JobID], interactionFromRecord(rec))
	}
	jobGroups := make(map[string][]jobstore.JobRecord)
	for _, rec := range snapshot.Jobs {
		id := "j:" + rec.ID
		if rec.SessionID != "" {
			id = "s:" + rec.SessionID
		}
		jobGroups[id] = append(jobGroups[id], rec)
	}
	out := make([]projection, 0, len(jobGroups)+len(snapshot.Sessions))
	for id, records := range jobGroups {
		sort.Slice(records, func(i, j int) bool {
			if records[i].StartedAt != records[j].StartedAt {
				return records[i].StartedAt < records[j].StartedAt
			}
			return records[i].ID < records[j].ID
		})
		pref, ok := prefs[id]
		if !ok && strings.HasPrefix(id, "s:") && len(records) > 0 {
			pref = prefs["j:"+records[0].ID]
		}
		out = append(out, projectJobThread(id, records, pref, interactions, snapshot.StalledJobIDs, seenBaseline))
	}
	openTurns := make(map[string][]jobstore.PlanDecision)
	for _, decision := range snapshot.RelayDecisions {
		openTurns[decision.SessionID] = append(openTurns[decision.SessionID], decision)
	}
	for _, session := range snapshot.Sessions {
		id := "r:" + session.SessionID
		out = append(out, projectRelayThread(id, session, prefs[id], openTurns[session.SessionID]))
	}
	return out
}

func projectJobThread(id string, records []jobstore.JobRecord, pref jobstore.WorkbenchThreadPref, interactions map[string][]job.Interaction, stalledJobs map[string]bool, seenBaseline int64) projection {
	first, latest := records[0], records[len(records)-1]
	kind := KindJob
	if latest.SessionID != "" {
		kind = KindAgent
	}
	thread := Thread{
		ID:          id,
		Kind:        kind,
		RawStatus:   latest.Status,
		Title:       defaultThreadTitle(first),
		ProjectKey:  latest.ProjectKey,
		Agent:       threadAgent(first),
		Runner:      latest.Runner,
		Cwd:         latest.Cwd,
		Interactive: latest.Interactive,
		Resumable:   latest.SessionID != "",
		Pinned:      pref.Pinned,
		SeenAt:      pref.SeenAt,
		StartedAt:   first.StartedAt,
		UpdatedAt:   latest.UpdatedAt,
		Turns:       len(records),
		LatestJobID: latest.ID,
		JobIDs:      make([]string, 0, len(records)),
		Jobs:        make([]JobTurn, 0, len(records)),
	}
	if pref.Title != "" {
		thread.Title = pref.Title
	}
	for _, rec := range records {
		usage := parseJobUsage(rec.UsageJSON)
		thread.JobIDs = append(thread.JobIDs, rec.ID)
		thread.Jobs = append(thread.Jobs, JobTurn{JobID: rec.ID, Status: rec.Status, StartedAt: rec.StartedAt, EndedAt: rec.EndedAt, Usage: usage})
		addUsage(&thread.Usage, usage)
		if rec.UpdatedAt > thread.UpdatedAt {
			thread.UpdatedAt = rec.UpdatedAt
		}
		if rec.Status == job.StatusRunning && stalledJobs[rec.ID] {
			thread.Stalled = true
		}
		thread.PendingInteractions = append(thread.PendingInteractions, interactions[rec.ID]...)
	}
	sort.Slice(thread.PendingInteractions, func(i, j int) bool {
		if thread.PendingInteractions[i].CreatedAt != thread.PendingInteractions[j].CreatedAt {
			return thread.PendingInteractions[i].CreatedAt < thread.PendingInteractions[j].CreatedAt
		}
		return thread.PendingInteractions[i].ID < thread.PendingInteractions[j].ID
	})

	var attention *AttentionItem
	if len(thread.PendingInteractions) > 0 || latest.Status == job.StatusPendingInteraction {
		thread.Status = StatusBlocked
		thread.WaitingSince = latest.UpdatedAt
		item := AttentionItem{ThreadID: id, ProjectKey: thread.ProjectKey, Title: thread.Title, Status: StatusBlocked, Action: ActionAnswer, JobID: latest.ID}
		if len(thread.PendingInteractions) > 0 {
			oldest := thread.PendingInteractions[0]
			thread.WaitingSince = oldest.CreatedAt
			item.JobID, item.InteractionID = oldest.JobID, oldest.ID
		}
		item.WaitingSince = thread.WaitingSince
		attention = &item
	} else {
		switch latest.Status {
		case job.StatusQueued, job.StatusRunning, job.StatusWaitingDir, job.StatusRecovering:
			thread.Status = StatusWorking
		case job.StatusNeedsReview:
			thread.Status = StatusReview
			thread.WaitingSince = recordWaitAt(latest)
		case job.StatusDone:
			thread.WaitingSince = recordWaitAt(latest)
			if terminalSeen(pref.SeenAt, seenBaseline, thread.WaitingSince) || thread.Agent == "exec" || !jobHasChanges(latest) {
				thread.Status = StatusDone
			} else {
				thread.Status = StatusReview
			}
		case job.StatusFailed, job.StatusTimeout, job.StatusRejected:
			thread.WaitingSince = recordWaitAt(latest)
			if terminalSeen(pref.SeenAt, seenBaseline, thread.WaitingSince) {
				thread.Status = StatusDone
			} else {
				thread.Status = StatusReview
			}
		case job.StatusCancelled:
			thread.Status = StatusDone
		default:
			thread.Status = StatusWorking
		}
		if thread.Status == StatusReview {
			attention = &AttentionItem{
				ThreadID: id, ProjectKey: thread.ProjectKey, Title: thread.Title,
				Status: StatusReview, Action: ActionReview, WaitingSince: thread.WaitingSince, JobID: latest.ID,
			}
		}
	}
	return projection{thread: thread, attention: attention}
}

func (s *Service) SeenAll(callerID string) (int64, error) {
	if s == nil || s.store == nil {
		return 0, ErrUnavailable
	}
	return s.store.SetWorkbenchSeenBaseline(normalizeCaller(callerID), s.now().Unix())
}

func projectRelayThread(id string, session jobstore.AgentSession, pref jobstore.WorkbenchThreadPref, decisions []jobstore.PlanDecision) projection {
	title := strings.TrimSpace(session.Title)
	if title == "" {
		title = strings.TrimSpace(session.Agent)
	}
	if title == "" {
		title = session.SessionID
	}
	if pref.Title != "" {
		title = pref.Title
	}
	thread := Thread{
		ID: id, Kind: KindRelay, RawStatus: session.State, Title: truncateRunes(title, 30),
		ProjectKey: session.ProjectKey, Agent: session.Agent, Runner: session.Runner, Cwd: session.Cwd,
		Resumable: session.State != jobstore.SessionEnded, Pinned: pref.Pinned, SeenAt: pref.SeenAt,
		StartedAt: session.StartedAt, UpdatedAt: session.LastSeenAt, Turns: int(session.TurnNo),
		Relay: &RelaySummary{
			SessionID: session.SessionID, State: session.State, RelayMode: session.RelayMode,
			LastEvent: session.LastEvent, LastMessage: session.LastMessage,
		},
	}
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].AskedAt != decisions[j].AskedAt {
			return decisions[i].AskedAt < decisions[j].AskedAt
		}
		return decisions[i].ID < decisions[j].ID
	})
	if len(decisions) > 0 || session.State == jobstore.SessionWaitingReply || session.State == jobstore.SessionNeedsAttention {
		thread.Status = StatusBlocked
		thread.WaitingSince = session.LastSeenAt
		item := AttentionItem{
			ThreadID: id, ProjectKey: session.ProjectKey, Title: thread.Title, Status: StatusBlocked,
			Action: ActionReply, WaitingSince: thread.WaitingSince, SessionID: session.SessionID,
		}
		if len(decisions) > 0 {
			thread.WaitingSince = decisions[0].AskedAt
			item.WaitingSince, item.DecisionID = decisions[0].AskedAt, decisions[0].ID
		}
		return projection{thread: thread, attention: &item}
	}
	if session.State == jobstore.SessionEnded {
		thread.Status = StatusDone
	} else {
		thread.Status = StatusIdle
	}
	return projection{thread: thread}
}

func (s *Service) Patch(callerID, threadID string, input PatchInput) (jobstore.WorkbenchThreadPref, error) {
	if s == nil || s.store == nil {
		return jobstore.WorkbenchThreadPref{}, ErrUnavailable
	}
	kind, rawID, err := ParseThreadID(threadID)
	if err != nil {
		return jobstore.WorkbenchThreadPref{}, err
	}
	callerID = normalizeCaller(callerID)
	snapshot, err := s.store.LoadWorkbenchSnapshot(callerID, 0)
	if err != nil {
		return jobstore.WorkbenchThreadPref{}, err
	}
	if !snapshotHasThread(snapshot, kind, rawID) {
		return jobstore.WorkbenchThreadPref{}, ErrUnknownThread
	}
	prefs := make(map[string]jobstore.WorkbenchThreadPref, len(snapshot.Prefs))
	for _, pref := range snapshot.Prefs {
		prefs[pref.ThreadID] = pref
	}
	pref, ok := prefs[threadID]
	if !ok && kind == KindAgent {
		for _, rec := range snapshot.Jobs {
			if rec.SessionID == rawID && rec.ResumedFrom == "" {
				pref = prefs["j:"+rec.ID]
				break
			}
		}
	}
	pref.CallerID, pref.ThreadID = callerID, threadID
	if input.Title != nil {
		pref.Title = strings.TrimSpace(*input.Title)
	}
	if input.Seen != nil {
		pref.SeenAt = 0
		if *input.Seen {
			pref.SeenAt = s.now().Unix()
		}
	}
	if input.Pinned != nil {
		pref.Pinned = *input.Pinned
	}
	if err := s.store.UpsertWorkbenchThreadPref(pref); err != nil {
		return jobstore.WorkbenchThreadPref{}, err
	}
	return pref, nil
}

func (s *Service) Turn(callerID, threadID, text string) (TurnResult, error) {
	kind, rawID, err := ParseThreadID(threadID)
	if err != nil {
		return TurnResult{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return TurnResult{}, ErrEmptyTurn
	}
	callerID = normalizeCaller(callerID)
	switch kind {
	case KindAgent:
		if s.jobs == nil || s.store == nil {
			return TurnResult{}, ErrUnavailable
		}
		rows, err := s.store.ListJobs(jobstore.ListQuery{Session: rawID, Limit: 1000})
		if err != nil {
			return TurnResult{}, err
		}
		if len(rows) == 0 {
			return TurnResult{}, ErrUnknownThread
		}
		result, err := s.jobs.ResumeJob(rows[0].ID, text, "", callerID)
		if err != nil {
			return TurnResult{}, err
		}
		return TurnResult{ThreadID: threadID, JobID: result.ID}, nil
	case KindRelay:
		if s.relay == nil {
			return TurnResult{}, ErrUnavailable
		}
		decision, err := s.relay.Say(rawID, text, callerID)
		if err != nil {
			return TurnResult{}, err
		}
		return TurnResult{ThreadID: threadID, DecisionID: decision.ID}, nil
	case KindJob:
		if s.store == nil {
			return TurnResult{}, ErrUnavailable
		}
		if _, ok, err := s.store.GetJob(rawID); err != nil {
			return TurnResult{}, err
		} else if !ok {
			return TurnResult{}, ErrUnknownThread
		}
		return TurnResult{}, fmt.Errorf("%w: 该一次性批处理没有 session，无法续接", ErrNotResumable)
	default:
		return TurnResult{}, ErrInvalidThreadID
	}
}

func ParseThreadID(id string) (ThreadKind, string, error) {
	prefix, value, ok := strings.Cut(strings.TrimSpace(id), ":")
	if !ok || strings.TrimSpace(value) == "" {
		return "", "", ErrInvalidThreadID
	}
	switch prefix {
	case "s":
		return KindAgent, value, nil
	case "j":
		return KindJob, value, nil
	case "r":
		return KindRelay, value, nil
	default:
		return "", "", ErrInvalidThreadID
	}
}

func snapshotHasThread(snapshot jobstore.WorkbenchSnapshot, kind ThreadKind, rawID string) bool {
	switch kind {
	case KindAgent:
		for _, rec := range snapshot.Jobs {
			if rec.SessionID == rawID {
				return true
			}
		}
	case KindJob:
		for _, rec := range snapshot.Jobs {
			if rec.ID == rawID {
				return true
			}
		}
	case KindRelay:
		for _, session := range snapshot.Sessions {
			if session.SessionID == rawID {
				return true
			}
		}
	}
	return false
}

func normalizeCaller(callerID string) string {
	if strings.TrimSpace(callerID) == "" {
		return "default"
	}
	return callerID
}

func defaultThreadTitle(first jobstore.JobRecord) string {
	var req job.JobRequest
	_ = json.Unmarshal([]byte(first.RequestJSON), &req)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = strings.TrimSpace(req.Prompt)
	}
	if title == "" {
		title = first.ID
	}
	return truncateRunes(title, 30)
}

func threadAgent(first jobstore.JobRecord) string {
	if first.Agent == "exec" && strings.TrimSpace(first.OriginAgent) != "" {
		return first.OriginAgent
	}
	return first.Agent
}

func jobHasChanges(rec jobstore.JobRecord) bool {
	if strings.TrimSpace(rec.DiffSummary) != "" {
		return true
	}
	var commits []json.RawMessage
	return json.Unmarshal([]byte(rec.CommitsJSON), &commits) == nil && len(commits) > 0
}

func terminalSeen(threadSeenAt, baseline, terminalAt int64) bool {
	return (threadSeenAt != 0 && threadSeenAt >= terminalAt) || (baseline != 0 && terminalAt < baseline)
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func parseJobUsage(raw string) *job.Usage {
	if raw == "" {
		return nil
	}
	var usage job.Usage
	if err := json.Unmarshal([]byte(raw), &usage); err != nil {
		return nil
	}
	return &usage
}

func addUsage(total **Usage, usage *job.Usage) {
	if usage == nil {
		return
	}
	if *total == nil {
		*total = &Usage{}
	}
	(*total).InputTokens += usage.InputTokens
	(*total).OutputTokens += usage.OutputTokens
	(*total).CacheReadTokens += usage.CacheReadTokens
	(*total).CacheWriteTokens += usage.CacheWriteTokens
	(*total).TotalTokens += usage.TotalTokens
	(*total).CostUSD += usage.CostUSD
}

func interactionFromRecord(rec jobstore.InteractionRecord) job.Interaction {
	var options []job.InteractionOption
	if rec.OptionsJSON != "" {
		_ = json.Unmarshal([]byte(rec.OptionsJSON), &options)
	}
	var toolCall *job.InteractionToolCall
	if rec.ToolCallJSON != "" {
		var value job.InteractionToolCall
		if json.Unmarshal([]byte(rec.ToolCallJSON), &value) == nil {
			toolCall = &value
		}
	}
	return job.Interaction{
		ID: rec.ID, JobID: rec.JobID, Type: rec.Type, Prompt: rec.Prompt, Options: options,
		Status: rec.Status, Answer: rec.Answer, CreatedAt: rec.CreatedAt, AnsweredAt: rec.AnsweredAt,
		ToolCall: toolCall, PolicyHint: rec.PolicyHint, ExpiresAt: rec.ExpiresAt,
		EscalatedAt: rec.EscalatedAt, AnsweredBy: rec.AnsweredBy, NeedsHuman: rec.NeedsHuman,
	}
}

func recordWaitAt(rec jobstore.JobRecord) int64 {
	if rec.EndedAt > 0 {
		return rec.EndedAt
	}
	if rec.UpdatedAt > 0 {
		return rec.UpdatedAt
	}
	return rec.StartedAt
}

func threadMatches(thread Thread, needle string) bool {
	for _, value := range []string{thread.Title, thread.ID, thread.Agent, thread.Cwd} {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func ensureGroup(groups map[string]*ProjectGroup, projectKey string) *ProjectGroup {
	if group := groups[projectKey]; group != nil {
		return group
	}
	group := &ProjectGroup{ProjectKey: projectKey, Status: StatusIdle, Threads: make([]Thread, 0)}
	groups[projectKey] = group
	return group
}

func addThreadCount(counts *Counts, status Status) {
	counts.Total++
	switch status {
	case StatusBlocked:
		counts.Blocked++
	case StatusWorking:
		counts.Working++
	case StatusReview:
		counts.Review++
	case StatusDone:
		counts.Done++
	case StatusIdle:
		counts.Idle++
	}
}

func higherStatus(left, right Status) Status {
	priority := map[Status]int{StatusIdle: 1, StatusDone: 2, StatusReview: 3, StatusWorking: 4, StatusBlocked: 5}
	if priority[right] > priority[left] {
		return right
	}
	return left
}
