package webpush

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/workbench"
)

const mergeWindow = 30 * time.Second

// NotificationAction maps one OS notification button to the exact interaction
// option value bound into its action token.
type NotificationAction struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Option string `json:"option"`
}

// PushPayload is intentionally small: no logs, full prompts or secrets.
type PushPayload struct {
	Title       string               `json:"title"`
	Body        string               `json:"body,omitempty"`
	ThreadID    string               `json:"thread_id,omitempty"`
	URL         string               `json:"url"`
	Tag         string               `json:"tag,omitempty"`
	ActionToken string               `json:"action_token,omitempty"`
	Actions     []NotificationAction `json:"actions,omitempty"`
}

func (s *Service) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case event := <-s.queue:
			s.dispatch(event)
		case <-ticker.C:
			s.actions.prune()
			s.pruneMergeWindows()
		}
	}
}

func (s *Service) dispatch(event queuedEvent) {
	if event.kind == "test" {
		s.dispatchTest(event.caller, event.lang)
		return
	}
	if event.kind != job.EventInteractionCreated &&
		event.kind != job.EventJobNeedsReview &&
		event.kind != job.EventJobAwaitingApproval &&
		event.kind != job.EventJobTerminal &&
		event.kind != job.EventPlanBlocked &&
		event.kind != job.EventSessionAwaitingReply {
		return
	}

	jobID := event.scope
	if event.kind == job.EventPlanBlocked {
		jobID, _ = stringDetail(event.detail, "job")
	}
	result, ok := s.jobs.Get(jobID)
	if !ok {
		slog.Warn("webpush event job unavailable", "scope", event.scope, "type", event.kind)
		return
	}
	if event.kind == job.EventJobTerminal && result.Session && result.SessionEndReason == "manual_end" {
		return
	}
	threadID := workbench.JobThreadID(result.ID, result.SessionID)
	base := PushPayload{
		ThreadID: threadID,
		URL:      "/workbench?thread=" + url.QueryEscape(threadID),
		Tag:      threadID,
	}
	urgency := "normal"
	var (
		interaction *job.Interaction
		status      string
	)
	switch event.kind {
	case job.EventInteractionCreated:
		interactionID, _ := stringDetail(event.detail, "interaction_id")
		found, err := s.findInteraction(result.ID, interactionID)
		if err != nil || found == nil {
			slog.Warn("webpush interaction unavailable", "job_id", result.ID, "interaction_id", interactionID)
			return
		}
		interaction = found
		base.Title, base.Body = pushTitleBody(s.lang(result.ProjectKey), event.kind, result, event.detail, found)
		urgency = "high"
		if found.Type == job.InteractionTypePermission {
			base.Actions = permissionActions(s.lang(result.ProjectKey), found.Options)
		}
	case job.EventJobNeedsReview:
		base.Title, base.Body = pushTitleBody(s.lang(result.ProjectKey), event.kind, result, event.detail, nil)
		status = job.StatusNeedsReview
	case job.EventJobAwaitingApproval:
		// gofer-9b1b: a held job waits for a person; the tap opens the job page, where the
		// approval panel is. Not filtered by the review rules (status stays empty): every
		// hold is a call to action.
		base.Title, base.Body = pushTitleBody(s.lang(result.ProjectKey), event.kind, result, event.detail, nil)
		base.URL = "/jobs/" + url.PathEscape(result.ID)
		urgency = "high"
	case job.EventJobTerminal:
		status, _ = stringDetail(event.detail, "status")
		if status == "" {
			status = result.Status
		}
		base.Title, base.Body = pushTitleBody(s.lang(result.ProjectKey), event.kind, result, event.detail, nil)
	case job.EventSessionAwaitingReply:
		base.Title, base.Body = pushTitleBody(s.lang(result.ProjectKey), event.kind, result, event.detail, nil)
		urgency = "high"
	case job.EventPlanBlocked:
		base.Title, base.Body = pushTitleBody(s.lang(result.ProjectKey), event.kind, result, event.detail, nil)
		urgency = "high"
	}

	seenCallers := make(map[string]struct{})
	for _, rawCaller := range s.userCallers() {
		callerID := normalizeCaller(rawCaller)
		if _, duplicate := seenCallers[callerID]; duplicate {
			continue
		}
		seenCallers[callerID] = struct{}{}
		if !s.visible(callerID, result.ProjectKey) {
			continue
		}
		if status != "" && !s.reviewForCaller(callerID, threadID, result, status) {
			continue
		}
		s.dispatchPayload(callerID, result.ProjectKey, base, interaction, urgency)
	}
}

// pushTitleBody is the title and body of the push for one event kind: the text the person
// reads in the OS notification. found is the interaction of an interaction event.
func pushTitleBody(lang i18n.Lang, kind string, result job.JobResult, detail map[string]any, found *job.Interaction) (string, string) {
	switch kind {
	case job.EventInteractionCreated:
		return i18n.T(lang, "webpush.title.interaction"), truncateRunes(found.Prompt, 120)
	case job.EventJobNeedsReview:
		return i18n.T(lang, "webpush.title.review"), truncateRunes(firstNonEmpty(result.Title, result.Error, result.ID), 120)
	case job.EventJobAwaitingApproval:
		reason := ""
		if result.Hold != nil {
			reason = result.Hold.Reason
		}
		return i18n.T(lang, "webpush.title.approval"), truncateRunes(holdPushBody(result.Title, reason, result.ID), 120)
	case job.EventJobTerminal:
		return terminalPushTitle(lang, result), truncateRunes(terminalPushBody(lang, result), 120)
	case job.EventSessionAwaitingReply:
		return sessionAwaitingReplyPush(lang, result, detail)
	case job.EventPlanBlocked:
		reason, _ := stringDetail(detail, "reason")
		return i18n.T(lang, "webpush.title.plan_blocked"), truncateRunes(reason, 120)
	}
	return "", ""
}

func (s *Service) dispatchTest(callerID string, lang i18n.Lang) {
	s.dispatchPayload(normalizeCaller(callerID), "", testPayload(lang), nil, "normal")
}

// testPayload is the push the "send a test notification" button queues.
func testPayload(lang i18n.Lang) PushPayload {
	return PushPayload{
		Title: i18n.T(lang, "webpush.title.test"),
		Body:  i18n.T(lang, "webpush.body.test"),
		URL:   "/workbench",
		Tag:   "gofer-test",
	}
}

func (s *Service) dispatchPayload(callerID, _ string, base PushPayload, interaction *job.Interaction, urgency string) {
	subscriptions, err := s.store.ListPushSubscriptions(callerID)
	if err != nil {
		slog.Warn("webpush list subscriptions", "caller_id", callerID, "err", err)
		return
	}
	if len(subscriptions) == 0 {
		return
	}
	if base.ThreadID != "" && !s.claimMergeWindow(callerID, base.ThreadID) {
		return
	}
	payload := base
	if interaction != nil && len(base.Actions) > 0 {
		// The action buttons are the permission-gated part of a notification (design §6
		// 「动作按钮在签发和兑现时都检查」): a caller that may not answer the interaction still
		// gets the notification — its tap opens the workbench — but never a button it could
		// not use, so the payload drops the actions along with the missing token.
		if !s.mayAct(callerID) {
			payload.Actions = nil
		} else {
			options := make([]string, 0, len(base.Actions))
			for _, action := range base.Actions {
				options = append(options, action.Option)
			}
			token, err := s.NewActionToken(callerID, interaction.JobID, interaction.ID, options)
			if err != nil {
				slog.Warn("webpush mint action token", "job_id", interaction.JobID, "interaction_id", interaction.ID, "err", err)
				return
			}
			payload.ActionToken = token
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("webpush encode payload", "thread_id", base.ThreadID, "err", err)
		return
	}
	topic := ""
	if base.ThreadID != "" {
		topic = topicForThread(base.ThreadID)
	}
	for _, subscription := range subscriptions {
		status, err := s.sender.send(subscription, body, urgency, topic)
		if err != nil {
			slog.Warn("webpush delivery failed", "caller_id", callerID, "endpoint_host", endpointHost(subscription.Endpoint), "err", err)
			continue
		}
		switch {
		case status >= 200 && status < 300:
			if err := s.store.MarkPushSubscriptionOK(subscription.Endpoint, s.now().Unix()); err != nil {
				slog.Warn("webpush mark subscription ok", "endpoint_host", endpointHost(subscription.Endpoint), "err", err)
			}
		case status == http.StatusNotFound || status == http.StatusGone:
			if err := s.store.DeletePushSubscriptionEndpoint(subscription.Endpoint); err != nil {
				slog.Warn("webpush delete expired subscription", "endpoint_host", endpointHost(subscription.Endpoint), "err", err)
			}
		default:
			slog.Warn("webpush endpoint rejected delivery", "endpoint_host", endpointHost(subscription.Endpoint), "status", status)
		}
	}
}

func (s *Service) findInteraction(jobID, interactionID string) (*job.Interaction, error) {
	list, err := s.jobs.GetInteractions(jobID)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if list[i].ID == interactionID {
			copy := list[i]
			return &copy, nil
		}
	}
	return nil, job.ErrUnknownInteraction
}

func (s *Service) reviewForCaller(callerID, threadID string, result job.JobResult, status string) bool {
	pref, _, err := s.store.GetWorkbenchThreadPref(callerID, threadID)
	if err != nil {
		slog.Warn("webpush read thread preference", "caller_id", callerID, "thread_id", threadID, "err", err)
		return false
	}
	baseline, _, err := s.store.GetWorkbenchSeenBaseline(callerID)
	if err != nil {
		slog.Warn("webpush read seen baseline", "caller_id", callerID, "err", err)
		return false
	}
	terminalAt := s.now().Unix()
	seen := (pref.SeenAt != 0 && pref.SeenAt >= terminalAt) ||
		(baseline != 0 && terminalAt < baseline)
	hasChanges := strings.TrimSpace(result.DiffSummary) != "" || len(result.Commits) > 0
	return workbench.TerminalNeedsReview(status, result.Agent, hasChanges, seen)
}

func (s *Service) claimMergeWindow(callerID, threadID string) bool {
	key := callerID + "\x00" + threadID
	now := s.now()
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	if previous, ok := s.sentAt[key]; ok && now.Sub(previous) < mergeWindow {
		return false
	}
	s.sentAt[key] = now
	return true
}

func (s *Service) pruneMergeWindows() {
	cutoff := s.now().Add(-mergeWindow)
	s.sentMu.Lock()
	for key, at := range s.sentAt {
		if at.Before(cutoff) {
			delete(s.sentAt, key)
		}
	}
	s.sentMu.Unlock()
}

func permissionActions(lang i18n.Lang, options []job.InteractionOption) []NotificationAction {
	var allow, reject *job.InteractionOption
	for i := range options {
		option := &options[i]
		switch option.Kind {
		case "allow_once":
			allow = option
		case "reject_once":
			reject = option
		}
	}
	if allow == nil {
		for i := range options {
			if strings.HasPrefix(options[i].Kind, "allow_") {
				allow = &options[i]
				break
			}
		}
	}
	if reject == nil {
		for i := range options {
			if strings.HasPrefix(options[i].Kind, "reject_") {
				reject = &options[i]
				break
			}
		}
	}
	out := make([]NotificationAction, 0, 2)
	if allow != nil {
		out = append(out, NotificationAction{Action: "allow", Title: i18n.T(lang, "webpush.action.allow"), Option: allow.Value})
	}
	if reject != nil {
		out = append(out, NotificationAction{Action: "reject", Title: i18n.T(lang, "webpush.action.reject"), Option: reject.Value})
	}
	return out
}

func stringDetail(detail map[string]any, key string) (string, bool) {
	if detail == nil {
		return "", false
	}
	value, ok := detail[key].(string)
	return value, ok
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func endpointHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

func terminalPushTitle(lang i18n.Lang, result job.JobResult) string {
	if result.Session {
		return i18n.T(lang, "webpush.title.terminal_session", "reason", sessionEndReason(lang, result.SessionEndReason))
	}
	return i18n.T(lang, "webpush.title.terminal_job")
}

// terminalPushBody is the body of a finished-job / finished-session push: where it ran,
// then the title (or the session's end reason).
func terminalPushBody(lang i18n.Lang, result job.JobResult) string {
	if result.Session && result.SessionEndReason != "" {
		return withRunner(sessionEndReason(lang, result.SessionEndReason), result.Runner)
	}
	return withRunner(firstNonEmpty(result.Title, result.Error, result.ID), result.Runner)
}

// withRunner appends "· runner <name>" (G043 display name) to a one-line push body;
// an empty body or runner keeps the body as it is.
func withRunner(body, runner string) string {
	name := config.RunnerDisplayName(runner)
	if name == "" || strings.TrimSpace(body) == "" {
		return body
	}
	return body + " · runner " + name
}

func sessionEndReason(lang i18n.Lang, reason string) string {
	switch reason {
	case "manual_end":
		return i18n.T(lang, "webpush.end_reason.manual_end")
	case "idle_timeout":
		return i18n.T(lang, "webpush.end_reason.idle_timeout")
	case "max_session_timeout", "max_session":
		return i18n.T(lang, "webpush.end_reason.max_session")
	default:
		if reason == "" {
			return i18n.T(lang, "webpush.end_reason.unrecorded")
		}
		return i18n.T(lang, "webpush.end_reason.failed", "reason", reason)
	}
}

func sessionAwaitingReplyPush(lang i18n.Lang, result job.JobResult, detail map[string]any) (string, string) {
	turn := intDetail(detail, "turn_no")
	preview, _ := detail["reply_preview"].(string)
	idleAt := int64Detail(detail, "idle_deadline_at")
	body := i18n.T(lang, "webpush.body.turn", "turn", turn, "agent", result.Agent, "project", result.ProjectKey)
	if runner := config.RunnerDisplayName(result.Runner); runner != "" {
		body += " · runner " + runner
	}
	if preview != "" {
		body += "\n" + truncateRunes(preview, 200)
	}
	if idleAt > 0 {
		body += "\n" + i18n.T(lang, "webpush.body.idle_end", "time", time.Unix(idleAt, 0).In(time.Local).Format("15:04"))
	}
	return i18n.T(lang, "webpush.title.awaiting_reply", "title", firstNonEmpty(result.Title, result.ID)), body
}

func intDetail(detail map[string]any, key string) int {
	if n, ok := detail[key].(int); ok {
		return n
	}
	if n, ok := detail[key].(float64); ok {
		return int(n)
	}
	return 0
}

func int64Detail(detail map[string]any, key string) int64 {
	if n, ok := detail[key].(int64); ok {
		return n
	}
	if n, ok := detail[key].(int); ok {
		return int64(n)
	}
	if n, ok := detail[key].(float64); ok {
		return int64(n)
	}
	return 0
}

// holdPushBody is the body of a 「待批准」 push: the job's title and the submitter's
// reason, whichever exist.
func holdPushBody(title, reason, id string) string {
	title, reason = strings.TrimSpace(title), strings.TrimSpace(reason)
	switch {
	case title != "" && reason != "":
		return title + " · " + reason
	case title != "":
		return title
	case reason != "":
		return reason
	}
	return id
}
