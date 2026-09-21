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
		ID:          r.ID,
		ProjectKey:  r.ProjectKey,
		Agent:       r.Agent,
		Runner:      r.Runner,
		Interactive: r.Interactive,
		ReadOnly:    r.ReadOnly,
		// JOB-11：同 cwd 独占决策（提交期定死，见 resolveDirExclusive）。
		DirExclusive: r.DirExclusive,
		// 人工验收（GATE-01 S3）：是否要求验收 + 验收决定审计。
		RequireReview:    r.RequireReview,
		ReviewedBy:       r.ReviewedBy,
		ReviewedAt:       r.ReviewedAt,
		ReviewNote:       r.ReviewNote,
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
		NDJSONTruncated: r.NDJSONTruncated,
		Source:          r.Source,
		TagsJSON:        marshalTags(r.Tags),
		// 工作流(job 链)：step-job 反向关联其 workflow + 1-based 步序号 + 重试 attempt。
		WorkflowID: r.WorkflowID,
		StepIndex:  r.StepIndex,
		Attempt:    r.Attempt,
		FanIndex:   r.FanIndex, // P2: fan-out 并行序号
		// session 捕获：底层 agent CLI 会话标识（注入/捕获）。
		SessionID:         r.SessionID,
		StopReason:        r.StopReason,
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
		// plan todo 联动 + 提交采集（SUP-01 C）。
		TodoID:      r.TodoID,
		BaseSHA:     r.BaseSHA,
		CommitsJSON: marshalCommits(r.Commits),
		// verify 步骤（SUP-01 P2）：无步骤 → ""（读回即"未跑验证"）。
		VerifyJSON: marshalVerify(r.Verify),
		// agent 故障转移（SUP-01 P3）：失败归类 + 转移链两端 + 调用方原请求的 agent +
		// 提交时冻结的候选计划。无候选 → ""（读回即"这条链没有候选"，不会伪造成转移过）。
		FailureClass:   r.FailureClass,
		FellBackFrom:   r.FellBackFrom,
		FellBackTo:     r.FellBackTo,
		RequestedAgent: r.RequestedAgent,
		FallbackJSON:   marshalFallback(r.Fallback),
		// 用量/成本（SUP-01 E）：未采集到 → ""（读回即"没有用量"）。
		UsageJSON: marshalUsage(r.Usage),
		// 文件传输（XFER-01 X2）：该 job 的 upload/collect 摘要；没带文件 → ""（读回即
		// "这个 job 没有传输"，不会伪造成一份空摘要）。
		XferJSON: marshalXfer(r.Xfer),
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

// marshalCommits serialises the captured commit list (SUP-01 C) into
// jobs.commits_json. Empty / failed → "" (the column then reads back as none).
func marshalCommits(commits []Commit) string {
	if len(commits) == 0 {
		return ""
	}
	b, err := json.Marshal(commits)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalCommits rebuilds the commit list from jobs.commits_json. A malformed
// blob yields no commits rather than failing the read of the whole job row.
func unmarshalCommits(s string) []Commit {
	if s == "" {
		return nil
	}
	var c []Commit
	if json.Unmarshal([]byte(s), &c) != nil {
		return nil
	}
	return c
}

// marshalVerify serialises the verify step's result (SUP-01 P2) into
// jobs.verify_json. A job without a step (or a marshal failure) stores "" — the
// column then reads back as "no verify step", which is exactly the pre-P2 meaning.
func marshalVerify(v *VerifyResult) string {
	if v == nil {
		return ""
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalVerify rebuilds the verify result from jobs.verify_json. A malformed blob
// yields none rather than failing the read of the whole job row.
func unmarshalVerify(s string) *VerifyResult {
	if s == "" {
		return nil
	}
	var v VerifyResult
	if json.Unmarshal([]byte(s), &v) != nil {
		return nil
	}
	return &v
}

// marshalFallback serialises a job's failover plan (SUP-01 P3) into
// jobs.fallback_json. A job with no candidates (or a marshal failure) stores "" —
// the column then reads back as "no chain", which is exactly the meaning for a job
// that never had one.
func marshalFallback(f *FallbackState) string {
	if f == nil || len(f.Candidates) == 0 {
		return ""
	}
	b, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalFallback rebuilds the failover plan from jobs.fallback_json. A malformed
// blob yields none rather than failing the read of the whole job row.
func unmarshalFallback(s string) *FallbackState {
	if s == "" {
		return nil
	}
	var f FallbackState
	if json.Unmarshal([]byte(s), &f) != nil || len(f.Candidates) == 0 {
		return nil
	}
	return &f
}

// marshalUsage serialises a job's token/cost accounting (SUP-01 E) into
// jobs.usage_json. A job that captured none (or a marshal failure) stores "" — the
// column then reads back as "no usage", never as a zero tally.
func marshalUsage(u *Usage) string {
	if u == nil {
		return ""
	}
	b, err := json.Marshal(u)
	if err != nil {
		return ""
	}
	return string(b)
}

// unmarshalUsage rebuilds the usage from jobs.usage_json. A malformed blob yields
// none rather than failing the read of the whole job row.
func unmarshalUsage(s string) *Usage {
	if s == "" {
		return nil
	}
	var u Usage
	if json.Unmarshal([]byte(s), &u) != nil {
		return nil
	}
	return &u
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
		ReadOnly:    rec.ReadOnly,
		// JOB-11：旧行 COALESCE 成 0 = "共享"，正是 JOB-11 之前的语义。
		DirExclusive: rec.DirExclusive,
		// 人工验收（GATE-01 S3）：旧行全为 0/空 = "未要求、未验收"。
		RequireReview: rec.RequireReview,
		ReviewedBy:    rec.ReviewedBy,
		ReviewedAt:    rec.ReviewedAt,
		ReviewNote:    rec.ReviewNote,
		WorkerID:      rec.WorkerID,
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
		NDJSONTruncated: rec.NDJSONTruncated,
		Source:          rec.Source,
		Tags:            unmarshalTags(rec.TagsJSON),
		// 工作流(job 链)。
		WorkflowID: rec.WorkflowID,
		StepIndex:  rec.StepIndex,
		Attempt:    rec.Attempt,
		FanIndex:   rec.FanIndex, // P2: fan-out 并行序号
		// session 捕获：底层 agent CLI 会话标识（注入/捕获）。
		SessionID:         rec.SessionID,
		StopReason:        rec.StopReason,
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
		// plan todo 联动 + 提交采集（SUP-01 C）：旧行空 = 未挂 todo / 未采集。
		TodoID:  rec.TodoID,
		BaseSHA: rec.BaseSHA,
		Commits: unmarshalCommits(rec.CommitsJSON),
		// verify 步骤（SUP-01 P2）：旧行 "" = 无步骤（nil），不伪造成"已验证"。
		Verify: unmarshalVerify(rec.VerifyJSON),
		// agent 故障转移（SUP-01 P3）：旧行 "" = 未分类 / 不在这条链上，不伪造成转移过的 job。
		FailureClass:   rec.FailureClass,
		FellBackFrom:   rec.FellBackFrom,
		FellBackTo:     rec.FellBackTo,
		RequestedAgent: rec.RequestedAgent,
		Fallback:       unmarshalFallback(rec.FallbackJSON),
		// 用量/成本（SUP-01 E）：旧行 "" = 未采集，不伪造成 0 用量。
		Usage: unmarshalUsage(rec.UsageJSON),
		// 文件传输（XFER-01 X2）：旧行 "" = 该 job 没带文件（nil），不伪造空摘要。
		Xfer: unmarshalXfer(rec.XferJSON),
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
