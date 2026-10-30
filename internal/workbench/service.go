package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/sessionrelay"
	"github.com/inhere/gofer/internal/util"
)

const defaultWindow = 7 * 24 * time.Hour

type Store interface {
	LoadWorkbenchSnapshot(callerID string, since int64, includeInternal bool) (jobstore.WorkbenchSnapshot, error)
	ListWorkbenchThreadPrefs(callerID string) ([]jobstore.WorkbenchThreadPref, error)
	UpsertWorkbenchThreadPref(pref jobstore.WorkbenchThreadPref) error
	GetOrCreateWorkbenchSeenBaseline(callerID string, observedAt int64) (int64, error)
	SetWorkbenchSeenBaseline(callerID string, observedAt int64) (int64, error)
	GetWorkbenchLayout(callerID string) (jobstore.WorkbenchLayout, bool, error)
	PutWorkbenchLayout(callerID string, expectedVersion int64, bodyJSON string, updatedAt int64) (jobstore.WorkbenchLayout, bool, error)
	ListJobs(query jobstore.ListQuery) ([]jobstore.JobRecord, error)
	GetJob(id string) (jobstore.JobRecord, bool, error)
}

type Jobs interface {
	ResumeJob(jobID, prompt, runner, callerID string) (job.JobResult, error)
	// ResumeJobWithImages continues a session with image paths riding the continuation's
	// argv (image_input: arg).
	ResumeJobWithImages(jobID, prompt, runner, callerID string, imagePaths []string) (job.JobResult, error)
	SaySession(jobID, message string) error
}

type Relay interface {
	Say(sessionID, answer, callerID string) (jobstore.PlanDecision, error)
}

// RelayMessenger is what the session drawer's web message calls (sessionrelay.Service): a
// session waiting for a reply is answered, one with a Claude SendMessage address goes
// through the messenger, any other takes the deliver_command / tmux ladder. A Relay that has
// it is used for workbench turns instead of Say, which only answers an OPEN turn.
type RelayMessenger interface {
	// SendMessage takes the display label (operator) and the caller-scope ceiling owner of the
	// message (origin) separately; see sessionrelay's ceiling_origin.go.
	SendMessage(ctx context.Context, sid, text, operator, origin string) (jobstore.SessionMessage, error)
	CheckDeliverable(sid string) error
	FollowWeb(sid, actor string)
}

type RelayImageMessenger interface {
	SendMessageWithImages(ctx context.Context, sid, text, operator, origin string, imagePaths []string) (jobstore.SessionMessage, error)
	FollowWeb(sid, actor string)
}

type Service struct {
	store Store
	jobs  Jobs
	relay Relay
	now   func() time.Time
	att   AttachmentDeps
}

func NewService(store Store, jobs Jobs, relay Relay) *Service {
	return &Service{store: store, jobs: jobs, relay: relay, now: time.Now}
}

func (s *Service) GetLayout(callerID string) (Layout, error) {
	if s == nil || s.store == nil {
		return Layout{}, ErrUnavailable
	}
	record, ok, err := s.store.GetWorkbenchLayout(normalizeCaller(callerID))
	if err != nil {
		return Layout{}, err
	}
	if !ok {
		return Layout{Version: 0, Body: json.RawMessage(`{}`)}, nil
	}
	return layoutFromRecord(record), nil
}

func (s *Service) PutLayout(callerID string, expectedVersion int64, body json.RawMessage) (Layout, error) {
	if s == nil || s.store == nil {
		return Layout{}, ErrUnavailable
	}
	if expectedVersion < 0 {
		return Layout{}, fmt.Errorf("%w: version must be non-negative", ErrInvalidLayout)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || !json.Valid(trimmed) {
		return Layout{}, fmt.Errorf("%w: body must be valid JSON", ErrInvalidLayout)
	}
	if len(trimmed) > MaxLayoutBodyBytes {
		return Layout{}, ErrLayoutTooLarge
	}
	record, updated, err := s.store.PutWorkbenchLayout(
		normalizeCaller(callerID), expectedVersion, string(trimmed), s.now().Unix(),
	)
	if err != nil {
		return Layout{}, err
	}
	current := layoutFromRecord(record)
	if !updated {
		return Layout{}, &LayoutVersionConflict{Current: current}
	}
	return current, nil
}

func layoutFromRecord(record jobstore.WorkbenchLayout) Layout {
	body := json.RawMessage(record.BodyJSON)
	if len(body) == 0 {
		body = json.RawMessage(`{}`)
	}
	return Layout{Version: record.Version, Body: append(json.RawMessage(nil), body...)}
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
	snapshot, err := s.store.LoadWorkbenchSnapshot(callerID, since, query.IncludeInternal)
	if err != nil {
		return Response{}, err
	}
	projections := projectThreads(snapshot, seenBaseline)

	groups := make(map[string]*ProjectGroup)
	attention := make([]AttentionItem, 0)
	needle := strings.ToLower(strings.TrimSpace(query.Q))
	for _, p := range projections {
		thread := p.thread
		s.annotateAttachments(&thread)
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
		// The workbench is a session surface. Batch jobs, one-shot ACP turns, and
		// resume carriers remain available from Board/job detail but do not create
		// workbench threads. Interactive PTY jobs and resident ACP jobs are the two
		// job-backed session kinds; relay sessions are projected below.
		if !rec.Interactive && rec.SessionStateJSON == "" {
			continue
		}
		id := JobThreadID(rec.ID, rec.SessionID)
		jobGroups[id] = append(jobGroups[id], rec)
	}
	out := make([]projection, 0, util.CapSum(len(jobGroups), len(snapshot.Sessions)))
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
		Agent:       first.Agent,
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
	if latest.SessionStateJSON != "" {
		var state struct {
			TurnNo int `json:"turn_no"`
		}
		if json.Unmarshal([]byte(latest.SessionStateJSON), &state) == nil && state.TurnNo > 0 {
			thread.Turns = state.TurnNo
		}
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
		case job.StatusAwaitingInput:
			thread.Status = StatusAwaitingInput
			thread.WaitingSince = latest.UpdatedAt
		case job.StatusNeedsReview, job.StatusDone, job.StatusFailed, job.StatusTimeout, job.StatusRejected, job.StatusCancelled:
			thread.WaitingSince = recordWaitAt(latest)
			if TerminalNeedsReview(latest.Status, thread.Agent, jobHasChanges(latest),
				terminalSeen(pref.SeenAt, seenBaseline, thread.WaitingSince)) {
				thread.Status = StatusReview
			} else {
				thread.Status = StatusDone
			}
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
	// A thread the user can see (internal ones too, when shown) must be patchable.
	snapshot, err := s.store.LoadWorkbenchSnapshot(callerID, 0, true)
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
	return s.TurnWithAttachments(callerID, threadID, text, nil)
}

// TurnWithAttachments is Turn with images: attachmentIDs are uploaded attachments of
// callerID that were not sent yet. They are delivered inline or by file (see
// RouteAttachments) or, when the receiver takes no images, the whole send is refused with
// ErrAttachmentsUnsupported; a failed placement sends nothing (ErrAttachmentDelivery).
func (s *Service) TurnWithAttachments(callerID, threadID, text string, attachmentIDs []string) (TurnResult, error) {
	kind, rawID, err := ParseThreadID(threadID)
	if err != nil {
		return TurnResult{}, err
	}
	text = strings.TrimSpace(text)
	if text == "" && len(attachmentIDs) == 0 {
		return TurnResult{}, ErrEmptyTurn
	}
	callerID = normalizeCaller(callerID)
	var atts []jobstore.Attachment
	if len(attachmentIDs) > 0 {
		if atts, err = s.loadAttachments(callerID, attachmentIDs); err != nil {
			return TurnResult{}, err
		}
	}
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
		for _, row := range rows {
			if row.SessionStateJSON == "" || job.IsTerminal(row.Status) {
				continue
			}
			if row.Status != job.StatusAwaitingInput {
				return TurnResult{}, fmt.Errorf("%w: 持续会话当前未等待输入", ErrNotResumable)
			}
			if len(atts) > 0 {
				return s.sayWithImages(callerID, threadID, row, text, atts)
			}
			if err := s.jobs.SaySession(row.ID, text); err != nil {
				return TurnResult{}, err
			}
			return TurnResult{ThreadID: threadID, JobID: row.ID}, nil
		}
		if len(atts) > 0 {
			return s.resumeWithImages(callerID, threadID, rows[0], text, atts)
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
		if len(atts) > 0 {
			return s.relayWithImages(callerID, threadID, rawID, text, atts)
		}
		if d, ok := s.relay.(RelayMessenger); ok {
			m, err := d.SendMessage(context.Background(), rawID, text, callerID, callerID)
			if err != nil {
				return TurnResult{}, relaySendError(m, err)
			}
			d.FollowWeb(rawID, callerID)
			return relayTurnResult(threadID, m), nil
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

// rowRouteInput is the route input of a job-backed thread: the session's own project decides
// whether the placement goes into a project or into the executing machine's own attachment
// directory.
func (s *Service) rowRouteInput(row jobstore.JobRecord) (RouteInput, string, string) {
	return RouteInput{
			Kind: KindAgent, Agent: row.Agent, Runner: row.Runner, Project: row.ProjectKey, Pty: row.Interactive,
			OwnDir: s.ownDir(row.Runner),
		},
		row.Runner, row.ProjectKey
}

// sayWithImages sends images into the live resident session job row.
func (s *Service) sayWithImages(callerID, threadID string, row jobstore.JobRecord, text string, atts []jobstore.Attachment) (TurnResult, error) {
	in, runner, project := s.rowRouteInput(row)
	// A live resident session takes an image as a protocol block or not at all: its next
	// turn has no argv of its own, so the arg delivery (which rides a fresh job's argv)
	// does not apply here and the route reports the agent as file-only/none instead.
	if s.imageInputAgent(in.Agent) == config.ImageInputPath {
		in.FilePath = true
	}
	if s.att.Jobs != nil && !in.Pty {
		in.ACPAccepts, _ = s.att.Jobs.SessionAcceptsImages(row.ID)
	}
	m := imageSend{
		callerID: callerID, threadID: threadID, atts: atts, in: in, runner: runner, project: project,
		// The next prompt of the resident session is turn N+1: the ACP history joins on it.
		earlyKind: RefKindPrompt, earlyRef: PromptRef(row.ID, rowTurnNo(row)+1),
		text: func(t string) (string, string, error) { return "", "", s.jobs.SaySession(row.ID, t) },
	}
	if s.att.Jobs != nil {
		m.inline = func(t string, images []job.ImageInput) error {
			return s.att.Jobs.SaySessionBlocks(row.ID, t, images)
		}
	}
	if _, _, err := s.deliverWithImages(m, text); err != nil {
		return TurnResult{}, err
	}
	return TurnResult{ThreadID: threadID, JobID: row.ID}, nil
}

// resumeWithImages continues an ended session with a new job; the images are placed on the
// executing machine and reach the agent either by path in the text (file) or as argv flags
// (arg).
func (s *Service) resumeWithImages(callerID, threadID string, row jobstore.JobRecord, text string, atts []jobstore.Attachment) (TurnResult, error) {
	in, runner, project := s.rowRouteInput(row)
	s.applyImageInput(&in)
	var result job.JobResult
	m := imageSend{
		callerID: callerID, threadID: threadID, atts: atts, in: in, runner: runner, project: project,
		text: func(t string) (string, string, error) {
			r, err := s.jobs.ResumeJob(row.ID, t, "", callerID)
			if err != nil {
				return "", "", err
			}
			result = r
			return RefKindPrompt, PromptRef(r.ID, 1), nil
		},
		// arg delivery: the paths ride the continuation's argv, so the body stays as typed.
		paths: func(t string, paths []string) (string, string, error) {
			r, err := s.jobs.ResumeJobWithImages(row.ID, t, "", callerID, paths)
			if err != nil {
				return "", "", err
			}
			result = r
			return RefKindPrompt, PromptRef(r.ID, 1), nil
		},
	}
	kind, ref, err := s.deliverWithImages(m, text)
	if err != nil {
		return TurnResult{}, err
	}
	s.bindAfterSend(atts, threadID, kind, ref)
	return TurnResult{ThreadID: threadID, JobID: result.ID}, nil
}

// ErrRelayDelivery wraps a failed message to a terminal session (the outbox already holds
// the failed record); the HTTP layer answers it as 409 with a readable reason.
var ErrRelayDelivery = errors.New("workbench: session message not delivered")

// relaySendError keeps the sentinel errors (unknown session) and wraps every other failure
// of SendMessage as ErrRelayDelivery.
func relaySendError(_ jobstore.SessionMessage, err error) error {
	if errors.Is(err, sessionrelay.ErrUnknownSession) {
		return err
	}
	return &RelayDeliveryError{Err: err}
}

// RelayDeliveryError is the cause of a failed terminal-session message; it matches
// ErrRelayDelivery and unwraps to the relay's own error (so sessionrelay.DeliverReason works).
type RelayDeliveryError struct{ Err error }

func (e *RelayDeliveryError) Error() string        { return e.Err.Error() }
func (e *RelayDeliveryError) Unwrap() error        { return e.Err }
func (e *RelayDeliveryError) Is(target error) bool { return target == ErrRelayDelivery }

// relayTurnResult maps the session message SendMessage recorded: Path is its channel (turn |
// command | tmux | messenger …), MessageID its outbox id, JobID the delivery job.
func relayTurnResult(threadID string, m jobstore.SessionMessage) TurnResult {
	return TurnResult{ThreadID: threadID, JobID: m.JobID, DecisionID: m.DecisionID, MessageID: m.ID, Path: m.Channel,
		Delivering: m.Status == jobstore.SessionMessageDelivering}
}

// relayWithImages sends a message with images to a terminal session by file delivery. The
// session is checked for deliverability BEFORE anything is placed (an unreachable session
// must not cost a transfer), and the text goes out the same ladder the drawer uses.
func (s *Service) relayWithImages(callerID, threadID, sid, text string, atts []jobstore.Attachment) (TurnResult, error) {
	sessions, ok := s.relay.(RelaySessions)
	if !ok {
		return TurnResult{}, ErrUnavailable
	}
	sess, err := sessions.Session(sid)
	if err != nil {
		return TurnResult{}, err
	}
	deliverer, ladder := s.relay.(RelayMessenger)
	if ladder {
		if err := deliverer.CheckDeliverable(sid); err != nil {
			return TurnResult{}, relaySendError(jobstore.SessionMessage{}, err)
		}
	}
	in := RouteInput{Kind: KindRelay, Agent: sess.Agent, Runner: sess.Runner, Project: sess.ProjectKey,
		OwnDir: s.ownDir(sess.Runner)}
	// The session's own delivery channel (stdin / a queue command) carries text only, so an
	// arg agent is not advertised as image-capable on this path; only path-readers get the
	// file delivery here.
	if s.imageInputAgent(sess.Agent) == config.ImageInputPath {
		in.FilePath = true
	}
	if len(s.deliverImageArgs(sess.Agent)) > 0 {
		in.Arg = true
	}
	var result TurnResult
	m := imageSend{
		callerID: callerID, threadID: threadID, atts: atts, in: in, runner: sess.Runner, project: sess.ProjectKey,
		oneLine: true,
		text: func(t string) (string, string, error) {
			if !ladder {
				d, err := s.relay.Say(sid, t, callerID)
				if err != nil {
					return "", "", err
				}
				result = TurnResult{ThreadID: threadID, DecisionID: d.ID}
				return RefKindDecision, d.ID, nil
			}
			msg, err := deliverer.SendMessage(context.Background(), sid, t, callerID, callerID)
			if err != nil {
				return "", "", relaySendError(msg, err)
			}
			deliverer.FollowWeb(sid, callerID)
			result = relayTurnResult(threadID, msg)
			// History joins on the decision (the session's turns[]: the answered turn or the
			// ladder's audit row) when there is one, else on the outbox message itself.
			if msg.DecisionID != "" {
				return RefKindDecision, msg.DecisionID, nil
			}
			return RefKindMessage, msg.ID, nil
		},
	}
	if imageRelay, ok := s.relay.(RelayImageMessenger); ok {
		m.paths = func(t string, paths []string) (string, string, error) {
			msg, err := imageRelay.SendMessageWithImages(context.Background(), sid, t, callerID, callerID, paths)
			if err != nil {
				return "", "", relaySendError(msg, err)
			}
			imageRelay.FollowWeb(sid, callerID)
			result = relayTurnResult(threadID, msg)
			if msg.DecisionID != "" {
				return RefKindDecision, msg.DecisionID, nil
			}
			return RefKindMessage, msg.ID, nil
		}
	}
	kind, ref, err := s.deliverWithImages(m, text)
	if err != nil {
		return TurnResult{}, err
	}
	s.bindAfterSend(atts, threadID, kind, ref)
	return result, nil
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

// JobThreadID is the canonical job-event to workbench-thread mapping. A captured
// agent session groups every resume turn; a one-shot job stands alone.
func JobThreadID(jobID, sessionID string) string {
	if sessionID != "" {
		return "s:" + sessionID
	}
	return "j:" + jobID
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

// TerminalNeedsReview is F16's single review predicate, shared by the workbench
// projection and Web Push dispatcher.
func TerminalNeedsReview(status, agent string, hasChanges, seen bool) bool {
	switch status {
	case job.StatusNeedsReview:
		return true
	case job.StatusDone:
		return !seen && agent != "exec" && hasChanges
	case job.StatusFailed, job.StatusTimeout, job.StatusRejected:
		return !seen
	default:
		return false
	}
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
		AnsweredBy: rec.AnsweredBy, NeedsHuman: rec.NeedsHuman,
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
	case StatusIdle, StatusAwaitingInput:
		counts.Idle++
	}
}

func higherStatus(left, right Status) Status {
	priority := map[Status]int{StatusIdle: 1, StatusAwaitingInput: 2, StatusDone: 3, StatusReview: 4, StatusWorking: 5, StatusBlocked: 6}
	if priority[right] > priority[left] {
		return right
	}
	return left
}
