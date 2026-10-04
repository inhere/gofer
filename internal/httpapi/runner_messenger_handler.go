package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gookit/rux/v2"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/messenger"
	runnerpkg "github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/wsproto"
)

// messengerAgentsTTL is how long a runner's ListAgents answer is reused. Listing
// costs a model turn, so a console that polls or re-opens the drawer must not
// trigger one each time; ?refresh=1 bypasses the cache.
var messengerAgentsTTL = 30 * time.Second

// messengerAgentsView is GET /v1/runners/{name}/messenger/agents.
type messengerAgentsView struct {
	Runner    string            `json:"runner"`
	FetchedAt int64             `json:"fetched_at"` // unix millis
	Cached    bool              `json:"cached"`
	Self      string            `json:"self,omitempty"`
	Agents    []messenger.Agent `json:"agents"`
	RawOutput string            `json:"raw_output"`
}

type agentsCacheEntry struct {
	at   time.Time
	list messenger.AgentList
}

// agentsCache memoizes ListAgents per runner and serializes the fetch of one
// runner, so concurrent opens share one model turn.
type agentsCache struct {
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
	entries map[string]agentsCacheEntry
}

func (c *agentsCache) lockFor(name string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.locks == nil {
		c.locks, c.entries = map[string]*sync.Mutex{}, map[string]agentsCacheEntry{}
	}
	if l := c.locks[name]; l != nil {
		return l
	}
	l := &sync.Mutex{}
	c.locks[name] = l
	return l
}

func (c *agentsCache) get(name string) (agentsCacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[name]
	return e, ok
}

func (c *agentsCache) put(name string, e agentsCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[name] = e
}

// handleRunnerMessengerAgents lists the sessions a runner's resident messenger can
// message (Claude's ListAgents). 404 unknown runner, 409 when that runner cannot
// do it (messaging off, peer-http, worker offline / too old), 502 when the
// listing itself failed.
func (s *Server) handleRunnerMessengerAgents(c *rux.Context) {
	name := strings.TrimSpace(c.Param("name"))
	refresh := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	view, status, msg, detail := s.messengerAgents(c.Req.Context(), name, refresh)
	if status != http.StatusOK {
		writeError(c, status, msg, detail)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) messengerAgents(ctx context.Context, name string, refresh bool) (view messengerAgentsView, status int, msg, detail string) {
	if s.msgr == nil || s.residentMessenger == nil {
		return view, http.StatusConflict, "传话人不可用", "该 server 没有启用会话传话（没有任务存储或传话配置）"
	}
	key := s.resolveRunnerName(name)
	var rc config.RunnerConfig
	if key != config.BuiltinLocalRunner {
		var ok bool
		if rc, ok = s.runners[key]; !ok {
			return view, http.StatusNotFound, "unknown runner", "runner " + name + " is not configured"
		}
		if rc.Type != runnerTypeWorker {
			return view, http.StatusConflict, "该 runner 没有传话人", "只有本机和 worker 类型的 runner 有常驻传话人"
		}
	}
	lock := s.agentsCache.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	if e, ok := s.agentsCache.get(key); ok && !refresh && time.Since(e.at) < messengerAgentsTTL {
		return agentsView(key, e, true), http.StatusOK, "", ""
	}
	var list messenger.AgentList
	var err error
	if key == config.BuiltinLocalRunner {
		callCtx, cancel := context.WithTimeout(ctx, s.msgr.timeout())
		defer cancel()
		list, err = s.residentMessenger.ListAgents(callCtx, config.BuiltinLocalRunner, "", []string{s.residentMessenger.Command()})
	} else {
		if s.workers == nil {
			return view, http.StatusConflict, "worker 不在线", "server 没有接入 worker 注册表"
		}
		ws, ok := s.workers.WorkerStatus(rc.WorkerID)
		if !ok || !ws.Connected {
			return view, http.StatusConflict, "worker 不在线", "worker " + rc.WorkerID + " 当前没有连接，无法列出它的会话"
		}
		if !wsproto.SupportsMessengerList(ws.ProtocolVersion) {
			return view, http.StatusConflict, "worker 版本过旧",
				fmt.Sprintf("worker %s 的协议版本是 v%d，列出会话需要 v%d 及以上；请先升级该 worker（gofer worker upgrade）",
					rc.WorkerID, ws.ProtocolVersion, wsproto.MessengerListMinProtocolVersion)
		}
		list, err = s.msgr.listWorkerAgents(ctx, key, ws.Projects, s.residentMessenger.Command())
	}
	if err != nil {
		return view, http.StatusBadGateway, "列出会话失败", err.Error()
	}
	e := agentsCacheEntry{at: time.Now(), list: list}
	s.agentsCache.put(key, e)
	return agentsView(key, e, false), http.StatusOK, "", ""
}

func agentsView(runner string, e agentsCacheEntry, cached bool) messengerAgentsView {
	agents := e.list.Agents
	if agents == nil {
		agents = []messenger.Agent{}
	}
	return messengerAgentsView{Runner: runner, FetchedAt: e.at.UnixMilli(), Cached: cached,
		Self: e.list.Self, Agents: agents, RawOutput: e.list.RawOutput}
}

func (x sessionInjector) timeout() time.Duration {
	if x.messengerTimeout > 0 {
		return x.messengerTimeout
	}
	return 90 * time.Second
}

// listWorkerAgents asks a worker's resident messenger for its sessions through an
// ordinary tagged job (so the dispatch, the log and the result use the same
// plumbing as a message delivery) and returns the parsed listing. The job needs a
// project the worker serves; the first one that accepts it wins.
func (x sessionInjector) listWorkerAgents(ctx context.Context, runner string, projects []string, command string) (messenger.AgentList, error) {
	if len(projects) == 0 {
		return messenger.AgentList{}, errors.New("该 worker 当前没有可用项目，无法派发列出会话的任务（先给它分配一个项目）")
	}
	keys := append([]string(nil), projects...)
	sort.Strings(keys)
	timeout := x.timeout()
	var lastErr error
	var jobID string
	for _, project := range keys {
		out, err := x.jobs.Submit(job.JobRequest{
			ProjectKey: project, Agent: agent.ExecAgentKey, Runner: runner,
			Cmd: []string{command, "-p", messenger.ListAgentsPrompt}, Cwd: ".", Title: "messenger · 列出可见会话",
			Tags: []string{job.MessengerJobTag}, Env: map[string]string{"GOFER_MESSENGER": "1"},
			MessengerMeta: &job.MessengerMeta{TargetSession: "(列出可见会话)", Channel: "resident"},
			TimeoutSec:    int(timeout / time.Second), EnvDenyExtra: []string{"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT"},
			Messenger: &runnerpkg.MessengerDispatch{
				Op: "list_agents", Command: []string{command, "-p", messenger.ListAgentsPrompt},
				TimeoutSec: int(timeout / time.Second), IdleSec: int(x.messengerIdle / time.Second),
			},
		})
		if err == nil {
			jobID = out.ID
			break
		}
		lastErr = err
	}
	if jobID == "" {
		return messenger.AgentList{}, fmt.Errorf("派发列出会话任务失败: %w", lastErr)
	}
	deadline := time.Now().Add(timeout + 5*time.Second)
	for {
		done, status, code, output, err := x.MessengerJob(jobID)
		if err != nil {
			return messenger.AgentList{}, err
		}
		if done {
			if status != job.StatusDone || code != 0 {
				if strings.TrimSpace(output) == "" {
					output = fmt.Sprintf("任务 %s 状态=%s exit=%d", jobID, status, code)
				}
				return messenger.AgentList{}, errors.New(strings.TrimSpace(output))
			}
			return messenger.ParseAgents(output), nil
		}
		if !time.Now().Before(deadline) {
			return messenger.AgentList{}, fmt.Errorf("列出会话任务 %s 超时", jobID)
		}
		select {
		case <-ctx.Done():
			return messenger.AgentList{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
