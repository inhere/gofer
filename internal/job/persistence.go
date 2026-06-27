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
		WorkerID:    r.WorkerID,
		Status:      r.Status,
		ExitCode:    r.ExitCode,
		Cwd:         r.Cwd,
		ResultDir:   r.ResultDir,
		RequestJSON: r.RequestJSON,
		Error:       r.Error,
		StartedAt:   r.StartedAt,
		EndedAt:     r.EndedAt,
		UpdatedAt:   r.UpdatedAt,
		CallerID:    r.CallerID,
		RequestID:   r.RequestID,
		// 产出与审计字段（job-outcomes-audit）。
		RenderedCommand: r.RenderedCommand,
		ResultJSON:      r.ResultJSON,
		ArtifactsJSON:   r.ArtifactsJSON,
		DiffSummary:     r.DiffSummary,
		Source:          r.Source,
		TagsJSON:        marshalTags(r.Tags),
		// 工作流(job 链)：step-job 反向关联其 workflow + 1-based 步序号 + 重试 attempt。
		WorkflowID: r.WorkflowID,
		StepIndex:  r.StepIndex,
		Attempt:    r.Attempt,
		FanIndex:   r.FanIndex, // P2: fan-out 并行序号
		// session 捕获：底层 agent CLI 会话标识（注入/捕获）。
		SessionID: r.SessionID,
		// 提交来源（provenance）：渠道 + 来源主机/IP。
		Channel: r.Channel,
		Client:  r.Client,
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
		WorkerID:    rec.WorkerID,
		Status:      rec.Status,
		ExitCode:    rec.ExitCode,
		Cwd:         rec.Cwd,
		ResultDir:   rec.ResultDir,
		RequestJSON: rec.RequestJSON,
		StartedAt:   rec.StartedAt,
		EndedAt:     rec.EndedAt,
		UpdatedAt:   rec.UpdatedAt,
		Error:       rec.Error,
		CallerID:    rec.CallerID,
		RequestID:   rec.RequestID,
		// 产出与审计字段（job-outcomes-audit）。
		RenderedCommand: rec.RenderedCommand,
		ResultJSON:      rec.ResultJSON,
		ArtifactsJSON:   rec.ArtifactsJSON,
		DiffSummary:     rec.DiffSummary,
		Source:          rec.Source,
		Tags:            unmarshalTags(rec.TagsJSON),
		// 工作流(job 链)。
		WorkflowID: rec.WorkflowID,
		StepIndex:  rec.StepIndex,
		Attempt:    rec.Attempt,
		FanIndex:   rec.FanIndex, // P2: fan-out 并行序号
		// session 捕获：底层 agent CLI 会话标识（注入/捕获）。
		SessionID: rec.SessionID,
		// 提交来源（provenance）：渠道 + 来源主机/IP。
		Channel: rec.Channel,
		Client:  rec.Client,
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
