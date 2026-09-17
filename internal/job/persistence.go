package job

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/jobstore"
)

// persist upserts one JobResult snapshot into the metadata store, stamping
// UpdatedAt with the current time. It returns the write error so finish can gate
// eviction on a durable terminal write; non-terminal callers (queued/running/
// interaction snapshots, where the entry stays in memory) ignore it best-effort.
func (s *Service) persist(snap JobResult) error {
	snap.UpdatedAt = s.nowFn().Unix()
	return s.meta.UpsertJob(toRecord(snap))
}

// toRecord projects a JobResult onto the neutral jobstore.JobRecord written to
// SQLite. SP5 carries RequestJSON into the request_json column (the on-disk
// request.json file is no longer written). WorkerID is mapped through for
// ws-worker jobs (jobs.worker_id already exists from C1; no migration).
func toRecord(r JobResult) jobstore.JobRecord {
	return jobstore.JobRecord{
		ID:               r.ID,
		ProjectKey:       r.ProjectKey,
		Agent:            r.Agent,
		Runner:           r.Runner,
		Interactive:      r.Interactive,
		WorkerID:         r.WorkerID,
		WorkerInstanceID: r.WorkerInstanceID,
		Status:           r.Status,
		ExitCode:         r.ExitCode,
		Cwd:              r.Cwd,
		ResultDir:        r.ResultDir,
		RequestJSON:      r.RequestJSON,
		Error:            r.Error,
		StartedAt:        r.StartedAt,
		EndedAt:          r.EndedAt,
		UpdatedAt:        r.UpdatedAt,
		CallerID:         r.CallerID,
		RequestID:        r.RequestID,
		// 产出与审计字段（job-outcomes-audit）。
		RenderedCommand: r.RenderedCommand,
		ResultJSON:      r.ResultJSON,
		ArtifactsJSON:   r.ArtifactsJSON,
		DiffSummary:     r.DiffSummary,
		NDJSONKept:      r.NDJSONKept,
		NDJSONDropped:   r.NDJSONDropped,
		Source:          r.Source,
		TagsJSON:        marshalTags(r.Tags),
		// 工作流(job 链)：step-job 反向关联其 workflow + 1-based 步序号 + 重试 attempt。
		WorkflowID: r.WorkflowID,
		StepIndex:  r.StepIndex,
		Attempt:    r.Attempt,
		FanIndex:   r.FanIndex, // P2: fan-out 并行序号
		// session 捕获：底层 agent CLI 会话标识（注入/捕获）。
		SessionID:         r.SessionID,
		ResumedFrom:       r.ResumedFrom,
		AutoResumeAttempt: r.AutoResumeAttempt,
		AutoResumedBy:     r.AutoResumedBy,
		// 提交来源（provenance）：渠道 + 来源主机/IP。
		Channel: r.Channel,
		Client:  r.Client,
		// 监督分层升级路由（supervisor-routing P1.1）：owner agent_id + 可选 job 级覆盖。
		OriginAgent: r.OriginAgent,
		EscalateTo:  r.EscalateTo,
		// 套娃防护（supervisor-routing P2.2）：角色预设名（如 supervisor），路由器据此识别 sup 自身的 interaction。
		Role:        r.Role,
		PlanID:      r.PlanID,
		SourceJobID: r.SourceJobID,
		// job 超时上限可配（bd h-aii-s9ck）：生效 deadline + 请求值 + 截断标记三元组。
		TimeoutSec:          r.TimeoutSec,
		RequestedTimeoutSec: r.RequestedTimeoutSec,
		TimeoutClamped:      r.TimeoutClamped,
		// RECOV-01：进入 recovering 的时刻（0=不在 recovering）。
		RecoveringSince: r.RecoveringSince,
		// WT-01：受管 worktree 的交付物位置与分支状态。
		WorktreePath:    r.WorktreePath,
		WorktreeBranch:  r.WorktreeBranch,
		WorktreeBaseSHA: r.WorktreeBaseSHA,
		WorktreeHeadSHA: r.WorktreeHeadSHA,
		CommitsAhead:    r.CommitsAhead,
	}
}

// marshalTags 把 tags 序列化为 tags_json 入库原文（E5）。best-effort：空/失败存 ""。
func marshalTags(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalTags 把 tags_json 原文反序列化为 tags（E5）。空/非法返回 nil（omitempty 不出现）。
func unmarshalTags(s string) []string {
	if s == "" {
		return nil
	}
	var t []string
	if json.Unmarshal([]byte(s), &t) != nil {
		return nil
	}
	return t
}

// fromRecord rebuilds a JobResult from a persisted jobstore.JobRecord. It is the
// read path for ListJobs/Get when a job is not (or no longer) in memory.
func fromRecord(rec jobstore.JobRecord) JobResult {
	return JobResult{
		ID:          rec.ID,
		ProjectKey:  rec.ProjectKey,
		Agent:       rec.Agent,
		Runner:      rec.Runner,
		Title:       TitleFromRequestJSON(rec.RequestJSON),
		Interactive: rec.Interactive,
		WorkerID:    rec.WorkerID,
		// RECOV-01 R4：dispatch 时记录的 worker 进程 nonce（旧行 ""＝无法被收养）。
		WorkerInstanceID: rec.WorkerInstanceID,
		Status:           rec.Status,
		ExitCode:         rec.ExitCode,
		Cwd:              rec.Cwd,
		ResultDir:        rec.ResultDir,
		RequestJSON:      rec.RequestJSON,
		StartedAt:        rec.StartedAt,
		EndedAt:          rec.EndedAt,
		UpdatedAt:        rec.UpdatedAt,
		Error:            rec.Error,
		CallerID:         rec.CallerID,
		RequestID:        rec.RequestID,
		// 产出与审计字段（job-outcomes-audit）。
		RenderedCommand: rec.RenderedCommand,
		ResultJSON:      rec.ResultJSON,
		ArtifactsJSON:   rec.ArtifactsJSON,
		DiffSummary:     rec.DiffSummary,
		NDJSONKept:      rec.NDJSONKept,
		NDJSONDropped:   rec.NDJSONDropped,
		Source:          rec.Source,
		Tags:            unmarshalTags(rec.TagsJSON),
		// 工作流(job 链)。
		WorkflowID: rec.WorkflowID,
		StepIndex:  rec.StepIndex,
		Attempt:    rec.Attempt,
		FanIndex:   rec.FanIndex, // P2: fan-out 并行序号
		// session 捕获：底层 agent CLI 会话标识（注入/捕获）。
		SessionID:         rec.SessionID,
		ResumedFrom:       rec.ResumedFrom,
		AutoResumeAttempt: rec.AutoResumeAttempt,
		AutoResumedBy:     rec.AutoResumedBy,
		// 提交来源（provenance）：渠道 + 来源主机/IP。
		Channel: rec.Channel,
		Client:  rec.Client,
		// 监督分层升级路由（supervisor-routing P1.1）：owner agent_id + 可选 job 级覆盖。
		OriginAgent: rec.OriginAgent,
		EscalateTo:  rec.EscalateTo,
		// 套娃防护（supervisor-routing P2.2）：角色预设名，路由器据此识别 sup 自身的 interaction。
		Role:        rec.Role,
		PlanID:      rec.PlanID,
		SourceJobID: rec.SourceJobID,
		// job 超时上限可配（bd h-aii-s9ck）：生效 deadline + 请求值 + 截断标记。旧行全为
		// 0/false = "未记录"（旧 job 早于该列），不会伪装成"被截断"。
		TimeoutSec:          rec.TimeoutSec,
		RequestedTimeoutSec: rec.RequestedTimeoutSec,
		TimeoutClamped:      rec.TimeoutClamped,
		// RECOV-01：进入 recovering 的时刻（旧行 0 = 从未 recovering）。
		RecoveringSince: rec.RecoveringSince,
		// WT-01：受管 worktree（旧行空/0 = 该 job 没有 worktree）。
		WorktreePath:    rec.WorktreePath,
		WorktreeBranch:  rec.WorktreeBranch,
		WorktreeBaseSHA: rec.WorktreeBaseSHA,
		WorktreeHeadSHA: rec.WorktreeHeadSHA,
		CommitsAhead:    rec.CommitsAhead,
	}
}

// TitleFromRequestJSON recovers the optional job Title from the persisted
// request_json blob. The jobs table has no title column (SP5), so the DB read
// path (fromRecord) parses it back out of the stored JobRequest to keep Title
// round-tripping through Get/ListJobs.
func TitleFromRequestJSON(s string) string {
	if s == "" {
		return ""
	}
	var t struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal([]byte(s), &t)
	return t.Title
}
