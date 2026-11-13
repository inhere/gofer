package job

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/i18n/prompts"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/util"
)

// Cross-run context of a scheduled job (design 2026-11-12 §3, plan R7): every run of a
// schedule with context=on learns which run it is and how the previous one ended, through
// environment variables, template variables (only for a request that names a template)
// and, for an agent job, a fixed section at the start of its prompt.

// Environment variables of a scheduled job with context on.
const (
	EnvScheduleID     = "GOFER_SCHEDULE_ID"
	EnvScheduleRun    = "GOFER_SCHEDULE_RUN"
	EnvSchedulePrevID = "GOFER_PREV_JOB_ID"
	EnvSchedulePrevSt = "GOFER_PREV_STATUS"
)

// Template variables (the same facts, lower-case) of a request that names a template.
const (
	scheduleVarID     = "schedule_id"
	scheduleVarRun    = "schedule_run"
	scheduleVarPrevID = "prev_job_id"
	scheduleVarPrevSt = "prev_status"
)

// schedulePrevReplyMax bounds the quoted end of the previous run's last reply.
const schedulePrevReplyMax = 2048

// ScheduleContext is what the schedule dispatcher knows about this run, carried on the
// request in memory only (never in request_json): the prompt section is rendered from it
// at submit time, in the job's language.
type ScheduleContext struct {
	ScheduleID string
	Name       string
	// Run is this run's number when the timer started it (the timer runs submitted so far
	// plus one); for a run-now or webhook run it is the timer runs submitted so far.
	Run     int
	Trigger string
	// Prev is the schedule's latest earlier job; nil on the first run.
	Prev *SchedulePrevRun
	// PrecheckOutput is the (masked) output of the schedule's pre-run check, "" = none.
	PrecheckOutput string
}

// SchedulePrevRun is the previous job of a schedule.
type SchedulePrevRun struct {
	JobID   string
	Status  string
	EndedAt int64
	// Reply is the end of the previous run's last reply (masked, at most 2KB), "" = none.
	Reply string
}

// attachScheduleContext fills the request's schedule context and, with it, the
// environment and template variables. It does nothing for a schedule whose context is
// off. precheckOutput rides along when a pre-run check produced one.
func (d *ScheduleDispatcher) attachScheduleContext(rec jobstore.ScheduleRecord, trigger string, req *JobRequest) {
	if rec.Context != jobstore.ScheduleContextOn {
		req.ScheduleContext = nil
		return
	}
	sc := &ScheduleContext{ScheduleID: rec.ID, Name: rec.Name, Trigger: trigger, Run: rec.RunCount}
	if trigger == jobstore.ScheduleTriggerTimer && rec.ScheduleType != "once" {
		sc.Run = rec.RunCount + 1
	}
	if req.ScheduleContext != nil {
		sc.PrecheckOutput = req.ScheduleContext.PrecheckOutput
	}
	if prev, found, err := d.s.meta.LatestScheduleJob(rec.ID); err == nil && found {
		_, secrets, _ := resolveSecrets(d.s.config(), req.Secrets)
		sc.Prev = &SchedulePrevRun{
			JobID: prev.ID, Status: prev.Status, EndedAt: prev.EndedAt,
			Reply: maskSecrets(previousReplyTail(prev.ResultDir), secrets),
		}
	}
	req.ScheduleContext = sc

	// The previous-run facts are always set (empty on the first run), so a script or a
	// template can name them without a first-run special case.
	prevID, prevStatus := "", ""
	if sc.Prev != nil {
		prevID, prevStatus = sc.Prev.JobID, sc.Prev.Status
	}
	// gofer's own keys: a stored env entry of the same name must not shadow them.
	req.Env = util.EnvWith(req.Env, map[string]string{
		EnvScheduleID: rec.ID, EnvScheduleRun: strconv.Itoa(sc.Run),
		EnvSchedulePrevID: prevID, EnvSchedulePrevSt: prevStatus,
	})
	if req.Template != "" {
		req.TemplateVars = util.EnvWith(req.TemplateVars, map[string]string{
			scheduleVarID: rec.ID, scheduleVarRun: strconv.Itoa(sc.Run),
			scheduleVarPrevID: prevID, scheduleVarPrevSt: prevStatus,
		})
	}
}

// previousReplyTail is the end (at most schedulePrevReplyMax bytes) of the previous job's
// last reply: the text of the last assistant turn of its turns.jsonl when the agent wrote
// one, else the tail of stdout. Best-effort: anything unreadable gives "". The text was
// masked when the previous run wrote it; the caller masks it again with the new job's
// granted secrets.
func previousReplyTail(resultDir string) string {
	if resultDir == "" {
		return ""
	}
	if text := lastTurnText(filepath.Join(resultDir, store.TurnsFile)); text != "" {
		return tailUTF8(text, schedulePrevReplyMax)
	}
	raw, err := readFileTail(filepath.Join(resultDir, store.StdoutFile), schedulePrevReplyMax*2)
	if err != nil {
		return ""
	}
	return tailUTF8(strings.TrimSpace(string(raw)), schedulePrevReplyMax)
}

// lastTurnText returns the text of the last assistant record of a turns.jsonl. Only the
// end of the file is read (the file is capped at 8MB, a turn at 256KB).
func lastTurnText(path string) string {
	raw, err := readFileTail(path, 512<<10)
	if err != nil || len(raw) == 0 {
		return ""
	}
	last := ""
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var rec struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}
		// The first line of a tail read may be cut in half: it simply fails to decode.
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Role != "assistant" || strings.TrimSpace(rec.Text) == "" {
			continue
		}
		last = rec.Text
	}
	return strings.TrimSpace(last)
}

func readFileTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > max {
		if _, err := f.Seek(-max, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

// tailUTF8 returns the last max bytes of s without cutting a character in half.
func tailUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// scheduleFence returns a code fence longer than any run of backticks in the quoted
// texts, so quoted text cannot close the fence and pass itself off as prompt.
func scheduleFence(texts ...string) string {
	longest := 0
	for _, t := range texts {
		run := 0
		for _, r := range t {
			if r == '`' {
				run++
				longest = max(longest, run)
				continue
			}
			run = 0
		}
	}
	return strings.Repeat("`", max(3, longest+1))
}

// injectScheduleContext puts the scheduled-run section at the start of an agent job's
// prompt. Skipped for an exec job (its argv is not a prompt), for a request without a
// schedule context, and for one whose prompt another side already fixed (RulesResolved).
func injectScheduleContext(cfg *config.Config, req *JobRequest) {
	sc := req.ScheduleContext
	if sc == nil || req.RulesResolved || isExecAgentJob(cfg, req) {
		return
	}
	data := prompts.ScheduleContextData{
		Name: sc.Name, Run: sc.Run, Timer: sc.Trigger == jobstore.ScheduleTriggerTimer,
		Trigger: sc.Trigger, Submitted: sc.Run, PrecheckOutput: sc.PrecheckOutput,
	}
	if sc.Prev != nil {
		data.HasPrev = true
		data.PrevJobID, data.PrevStatus, data.PrevReply = sc.Prev.JobID, sc.Prev.Status, sc.Prev.Reply
		if sc.Prev.EndedAt > 0 {
			data.PrevEnded = time.Unix(sc.Prev.EndedAt, 0).UTC().Format(time.RFC3339)
		}
	}
	data.Fence = scheduleFence(data.PrevReply, data.PrecheckOutput)
	section := strings.TrimSpace(prompts.MustRender(promptLanguage(cfg, req), prompts.NameScheduleContext, data))
	if section == "" {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		req.Prompt = section
		return
	}
	req.Prompt = section + "\n\n" + req.Prompt
}
