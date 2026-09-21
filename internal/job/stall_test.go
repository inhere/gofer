package job

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// AUTO-05 输出停滞检测的测试。
//
// 看门狗的真实间隔是 30s，阈值是秒级；两者在测试里都缩到毫秒级（Service.stallTick
// 注入 + 1s 阈值），证据仍然是"job 到底死没死、以什么原因死"：被杀的 job 一定是
// failed + error 前缀 `stalled:`，没被杀的 job 一定跑完它自己的 argv。

// stallService 建一个跑停滞测试的 Service：项目 "self" 落在 root，"codex" 是一个驱动
// testcmd 的 cli-agent（用内置名 codex 以便吃到内置 transient 模式），stall 是
// server.stall_timeout_sec，agentStall 是 agents.codex.stall_timeout_sec。mutate 在装配前
// 调整配置。看门狗间隔压到 50ms，让 1s 级阈值能在测试时间里生效。
func stallService(t *testing.T, root string, args []string, stall, agentStall *int, mutate func(*config.Config)) *Service {
	t.Helper()
	cfg := &config.Config{
		Server:  config.ServerConfig{StallTimeoutSec: stall},
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {
				HostPath:       root,
				AllowedAgents:  []string{"codex", "exec"},
				AllowedRunners: []string{"local"},
				AllowExec:      true,
			},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {
				Type:            agent.TypeCLIAgent,
				Command:         testcmd.Path(t),
				Args:            args,
				StallTimeoutSec: agentStall,
			},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	s := newServiceFromCfg(t, root, cfg)
	s.stallTick = 50 * time.Millisecond
	return s
}

// intPtr returns a pointer to n (the tri-state config fields need one).
func intPtr(n int) *int { return &n }

// stallEvents returns the job.stalled events of a job with their decoded details.
func stallEvents(t *testing.T, s *Service, jobID string) []map[string]any {
	t.Helper()
	evs, err := s.ListJobEvents(jobID, 0)
	if err != nil {
		t.Fatalf("ListJobEvents(%s): %v", jobID, err)
	}
	var out []map[string]any
	for _, e := range evs {
		if e.Type != EventJobStalled {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("decode %s detail %q: %v", e.Type, e.Detail, err)
		}
		out = append(out, d)
	}
	return out
}

// TestStallKillsSilentAgentJob: 一个只打印一行就长时间没声音的 agent job 在窗口后被杀：
// 状态 failed、error 以 `stalled:` 开头、事件 job.stalled 带静默秒数与阈值、failure_class
// 是 transient（于是自动续投/故障转移链正常接管）。
func TestStallKillsSilentAgentJob(t *testing.T) {
	root := t.TempDir()
	s := stallService(t, root, []string{"stdout-sleep", "working", "30s"}, intPtr(1), nil, nil)

	res := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "do the thing", Cwd: ".", TimeoutSec: 60,
	})
	final := waitStatus(t, s, res.ID, 20*time.Second, StatusFailed)
	if !strings.HasPrefix(final.Error, "stalled: no output for ") {
		t.Fatalf("error = %q, want a %q prefix", final.Error, "stalled: no output for ")
	}
	if final.FailureClass != FailureClassTransient {
		t.Fatalf("failure_class = %q, want %q (a stall is a provider-side hang)", final.FailureClass, FailureClassTransient)
	}
	evs := stallEvents(t, s, final.ID)
	if len(evs) != 1 {
		t.Fatalf("job.stalled events = %d, want exactly 1", len(evs))
	}
	if silent, _ := evs[0]["silent_sec"].(float64); silent < 1 {
		t.Fatalf("job.stalled silent_sec = %v, want >= 1", evs[0]["silent_sec"])
	}
	if limit, _ := evs[0]["stall_timeout_sec"].(float64); limit != 1 {
		t.Fatalf("job.stalled stall_timeout_sec = %v, want 1", evs[0]["stall_timeout_sec"])
	}
}

// TestStallResetsOnOutput: 每 0.4s 输出一行的 job 在 1s 窗口下不会被杀——输出活动重置
// 静默计时（这就是"卡死"与"慢"的区别）。
func TestStallResetsOnOutput(t *testing.T) {
	root := t.TempDir()
	s := stallService(t, root, []string{"stdout-lines", "tick", "6", "400ms"}, intPtr(1), nil, nil)

	res := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "steady progress", Cwd: ".", TimeoutSec: 60,
	})
	final, ok := s.Wait(res.ID)
	if !ok {
		t.Fatalf("job %s vanished", res.ID)
	}
	if final.Status != StatusDone {
		t.Fatalf("status = %s (%s), want done: a job that keeps printing must never be stalled", final.Status, final.Error)
	}
	if evs := stallEvents(t, s, final.ID); len(evs) != 0 {
		t.Fatalf("job.stalled events = %v, want none", evs)
	}
}

// TestStallDisabledForExecByDefault: exec job 默认不设停滞窗口（构建/测试本来就会长时间
// 没输出），server.stall_timeout_sec=1 也管不到它。
func TestStallDisabledForExecByDefault(t *testing.T) {
	root := t.TempDir()
	s := stallService(t, root, nil, intPtr(1), nil, nil)

	res := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "stdout-sleep", "silent build", "3s"},
		Cwd: ".", TimeoutSec: 60,
	})
	final, ok := s.Wait(res.ID)
	if !ok {
		t.Fatalf("job %s vanished", res.ID)
	}
	if final.Status != StatusDone {
		t.Fatalf("exec job status = %s (%s), want done (the watchdog must be off for exec by default)", final.Status, final.Error)
	}
	if evs := stallEvents(t, s, final.ID); len(evs) != 0 {
		t.Fatalf("job.stalled events = %v, want none", evs)
	}

	// ...but an EXPLICIT window applies to an exec job too: the request wins over the
	// exec default.
	explicit := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "exec", Runner: "local",
		Cmd: []string{testcmd.Path(t), "stdout-sleep", "silent build", "30s"},
		Cwd: ".", TimeoutSec: 60, StallTimeoutSec: intPtr(2),
	})
	killed := waitStatus(t, s, explicit.ID, 20*time.Second, StatusFailed)
	if !strings.HasPrefix(killed.Error, "stalled: no output for ") {
		t.Fatalf("--stall-timeout exec job error = %q, want a stalled prefix", killed.Error)
	}
}

// TestStallPausedDuringPendingInteraction: 等人作答期间不计时——一个静默超过窗口的 job
// 只要还停在一个待答交互上就不该被杀（否则它会带着一个永不被消费的 pending 行死掉），
// 答完之后计时从头开始（等答案的那段时间不算在 agent 头上）。
func TestStallPausedDuringPendingInteraction(t *testing.T) {
	root := t.TempDir()
	// stdout-sleep started 5s：t0 打印一行、此后一直静默——足够长的静默期让"暂停/不暂停"
	// 两种行为都落在测试窗口里。
	s := stallService(t, root, []string{"stdout-sleep", "started", "5s"}, intPtr(1), nil, nil)

	res := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "needs an answer", Cwd: ".", TimeoutSec: 60,
	})
	waitForStatus(t, s, res.ID, StatusRunning, 10*time.Second)
	it, err := s.CreateInteraction(res.ID, InteractionInput{Type: InteractionTypeQuestion, Prompt: "which env?"})
	if err != nil {
		t.Fatalf("CreateInteraction: %v", err)
	}
	waitForStatus(t, s, res.ID, StatusPendingInteraction, 10*time.Second)

	// 静默早已超过 1s 窗口：暂停没生效的话，这个 job 现在已经是 failed 了。
	time.Sleep(1500 * time.Millisecond)
	cur, _ := s.Get(res.ID)
	if cur.Status != StatusPendingInteraction {
		t.Fatalf("status = %s (%s), want the job parked in %s while a human thinks",
			cur.Status, cur.Error, StatusPendingInteraction)
	}
	if evs := stallEvents(t, s, res.ID); len(evs) != 0 {
		t.Fatalf("job.stalled events = %v, want none while an interaction is pending", evs)
	}

	// 答得上就证明 job 还活着（一个已被判停滞并杀掉的 job 会拒绝作答），而且计时从此刻
	// 重新开始：之后再静默超过窗口才该被杀——silent_sec 只能是窗口级的，不可能回溯到 t0。
	if _, err := s.AnswerInteraction(res.ID, it.ID, "prod"); err != nil {
		t.Fatalf("AnswerInteraction: %v", err)
	}
	final := waitStatus(t, s, res.ID, 20*time.Second, StatusFailed)
	if !strings.HasPrefix(final.Error, "stalled: no output for ") {
		t.Fatalf("error after the answer = %q, want the watchdog to judge the silence that followed it", final.Error)
	}
	evs := stallEvents(t, s, final.ID)
	if len(evs) != 1 {
		t.Fatalf("job.stalled events = %d, want exactly 1", len(evs))
	}
	if silent, _ := evs[0]["silent_sec"].(float64); silent > 2 {
		t.Fatalf("job.stalled silent_sec = %v, want the clock restarted at the answer (<= 2s)", evs[0]["silent_sec"])
	}
}

// TestStallTriggersAutoResume: 停滞按 transient 处理，于是自动续投照常发生——源 job 记下
// 续投 id，续投用同一个 session 跑（并成功），无需任何停滞专用通路。
func TestStallTriggersAutoResume(t *testing.T) {
	root := t.TempDir()
	s := stallService(t, root, []string{"stdout-sleep", "working", "30s"}, intPtr(1), nil, func(cfg *config.Config) {
		// The continuation runs the same binary in a mode that exits 0.
		a := cfg.Agents["codex"]
		a.SessionResume = []string{"printf", "resumed {{session_id}}: {{prompt}}"}
		cfg.Agents["codex"] = a
	})

	src := mustSubmit(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "long task", Cwd: ".", TimeoutSec: 60, SessionID: "sess-stall",
	})
	failed := waitStatus(t, s, src.ID, 20*time.Second, StatusFailed)
	if !strings.HasPrefix(failed.Error, "stalled: no output for ") {
		t.Fatalf("source error = %q, want a stalled prefix", failed.Error)
	}
	src = waitAutoResumed(t, s, src.ID, true)

	cont, ok := s.Get(src.AutoResumedBy)
	if !ok {
		t.Fatalf("continuation %s not found", src.AutoResumedBy)
	}
	if cont.ResumedFrom != src.ID || cont.SessionID != "sess-stall" {
		t.Fatalf("continuation lineage = resumed_from %q session %q, want %s/sess-stall", cont.ResumedFrom, cont.SessionID, src.ID)
	}
	final, _ := s.Wait(cont.ID)
	if final.Status != StatusDone {
		t.Fatalf("continuation status = %s (%s), want done", final.Status, final.Error)
	}
}
