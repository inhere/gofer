package webpush

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

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
		s.dispatchTest(event.caller)
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
		base.Title = "等你审批/回答"
		base.Body = truncateRunes(found.Prompt, 120)
		urgency = "high"
		if found.Type == job.InteractionTypePermission {
			base.Actions = permissionActions(found.Options)
		}
	case job.EventJobNeedsReview:
		base.Title = "待评审"
		base.Body = truncateRunes(firstNonEmpty(result.Title, result.Error, result.ID), 120)
		status = job.StatusNeedsReview
	case job.EventJobAwaitingApproval:
		// gofer-9b1b: a held job waits for a person; the tap opens the job page, where the
		// approval panel is. Not filtered by the review rules (status stays empty): every
		// hold is a call to action.
		base.Title = "待批准"
		reason := ""
		if result.Hold != nil {
			reason = result.Hold.Reason
		}
		base.Body = truncateRunes(holdPushBody(result.Title, reason, result.ID), 120)
		base.URL = "/jobs/" + url.PathEscape(result.ID)
		urgency = "high"
	case job.EventJobTerminal:
		status, _ = stringDetail(event.detail, "status")
		if status == "" {
			status = result.Status
		}
		base.Title = terminalPushTitle(result)
		base.Body = truncateRunes(terminalPushBody(result), 120)
	case job.EventSessionAwaitingReply:
		base.Title, base.Body = sessionAwaitingReplyPush(result, event.detail)
		urgency = "high"
	case job.EventPlanBlocked:
		base.Title = "计划已阻塞"
		base.Body, _ = stringDetail(event.detail, "reason")
		base.Body = truncateRunes(base.Body, 120)
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

func (s *Service) dispatchTest(callerID string) {
	payload := PushPayload{
		Title: "gofer 测试通知",
		Body:  "此设备的 Web Push 已连通。",
		URL:   "/workbench",
		Tag:   "gofer-test",
	}
	s.dispatchPayload(normalizeCaller(callerID), "", payload, nil, "normal")
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
	agentName := result.Agent
	if agentName == "exec" && result.OriginAgent != "" {
		agentName = result.OriginAgent
	}
	return workbench.TerminalNeedsReview(status, agentName, hasChanges, seen)
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

func permissionActions(options []job.InteractionOption) []NotificationAction {
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
		out = append(out, NotificationAction{Action: "allow", Title: "允许", Option: allow.Value})
	}
	if reject != nil {
		out = append(out, NotificationAction{Action: "reject", Title: "拒绝", Option: reject.Value})
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

func terminalPushTitle(result job.JobResult) string {
	if result.Session {
		return "会话已结束：" + sessionEndReason(result.SessionEndReason)
	}
	return "任务已结束"
}

func terminalPushBody(result job.JobResult) string {
	if result.Session && result.SessionEndReason != "" {
		return sessionEndReason(result.SessionEndReason)
	}
	return firstNonEmpty(result.Title, result.Error, result.ID)
}

func sessionEndReason(reason string) string {
	switch reason {
	case "manual_end":
		return "手动结束"
	case "idle_timeout":
		return "空闲超时"
	case "max_session_timeout", "max_session":
		return "超过会话总时长"
	default:
		if reason == "" {
			return "失败原因未记录"
		}
		return "失败：" + reason
	}
}

func sessionAwaitingReplyPush(result job.JobResult, detail map[string]any) (string, string) {
	turn := intDetail(detail, "turn_no")
	preview, _ := detail["reply_preview"].(string)
	idleAt := int64Detail(detail, "idle_deadline_at")
	body := fmt.Sprintf("第 %d 轮 · %s · %s", turn, result.Agent, result.ProjectKey)
	if preview != "" {
		body += "\n" + truncateRunes(preview, 200)
	}
	if idleAt > 0 {
		body += "\n空闲将在 " + time.Unix(idleAt, 0).In(time.Local).Format("15:04") + " 自动结束"
	}
	return "会话等你回复：" + firstNonEmpty(result.Title, result.ID), body
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
