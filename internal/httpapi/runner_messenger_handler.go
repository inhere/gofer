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
	"github.com/inhere/gofer/internal/i18n"
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
	// The listing starts an exec job on the runner and answers with every session it can
	// see, across projects: a caller with a scope ceiling has no use for it.
	if s.ceilingOf(c) != nil {
		writeError(c, http.StatusForbidden, "caller ceiling",
			"a caller with a scope ceiling may not list a runner's resident sessions")
		return
	}
	name := strings.TrimSpace(c.Param("name"))
	refresh := c.Query("refresh") == "1" || c.Query("refresh") == "true"
	view, fail := s.messengerAgents(c.Req.Context(), s.requestLang(c), name, refresh)
	if fail != nil {
		writeAPIError(c, fail)
		return
	}
	c.JSON(http.StatusOK, view)
}

func (s *Server) messengerAgents(ctx context.Context, lang i18n.Lang, name string, refresh bool) (view messengerAgentsView, fail *apiError) {
	// Both the `error` phrase and the detail follow the request language: the phrases
	// here were never fixed English ones.
	failf := func(status int, phrase, code string, args ...any) *apiError {
		return newAPIError(lang, status, i18n.T(lang, errKeyPrefix+phrase), code, args...)
	}
	if s.msgr == nil || s.residentMessenger == nil {
		return view, failf(http.StatusConflict, "messenger_unavailable_error", "messenger_unavailable")
	}
	key := s.resolveRunnerName(name)
	var rc config.RunnerConfig
	if key != config.BuiltinLocalRunner {
		var ok bool
		if rc, ok = s.runners[key]; !ok {
			return view, &apiError{Status: http.StatusNotFound, Msg: "unknown runner", Detail: "runner " + name + " is not configured"}
		}
		if rc.Type != runnerTypeWorker {
			return view, failf(http.StatusConflict, "messenger_no_resident_error", "messenger_no_resident")
		}
	}
	lock := s.agentsCache.lockFor(key)
	lock.Lock()
	defer lock.Unlock()
	if e, ok := s.agentsCache.get(key); ok && !refresh && time.Since(e.at) < messengerAgentsTTL {
		return agentsView(key, e, true), nil
	}
	var list messenger.AgentList
	var err error
	if key == config.BuiltinLocalRunner {
		callCtx, cancel := context.WithTimeout(ctx, s.msgr.timeout())
		defer cancel()
		list, err = s.residentMessenger.ListAgents(callCtx, config.BuiltinLocalRunner, "", []string{s.residentMessenger.Command()})
	} else {
		if s.workers == nil {
			return view, failf(http.StatusConflict, "worker_offline_error", "messenger_no_worker_registry")
		}
		ws, ok := s.workers.WorkerStatus(rc.WorkerID)
		if !ok || !ws.Connected {
			return view, failf(http.StatusConflict, "worker_offline_error", "messenger_worker_offline", "worker", rc.WorkerID)
		}
		if !wsproto.SupportsMessengerList(ws.ProtocolVersion) {
			return view, failf(http.StatusConflict, "worker_too_old_error", "messenger_worker_too_old",
				"worker", rc.WorkerID, "have", ws.ProtocolVersion, "need", wsproto.MessengerListMinProtocolVersion)
		}
		list, err = s.msgr.listWorkerAgents(i18n.WithLang(ctx, lang), key, ws.Projects, s.residentMessenger.Command())
	}
	if err != nil {
		// The cause was rendered in lang by listWorkerAgents (it reads the language off
		// ctx), so it goes out as the detail unkeyed.
		return view, &apiError{Status: http.StatusBadGateway, Msg: i18n.T(lang, errKeyPrefix+"messenger_list_failed_error"), Detail: err.Error()}
	}
	e := agentsCacheEntry{at: time.Now(), list: list}
	s.agentsCache.put(key, e)
	return agentsView(key, e, false), nil
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
	// The failure reasons go out as the response's detail: they follow the request language.
	lang := i18n.FromContext(ctx)
	if len(projects) == 0 {
		return messenger.AgentList{}, errors.New(i18n.T(lang, "err.messenger_worker_no_project"))
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
		return messenger.AgentList{}, fmt.Errorf("%s: %w", i18n.T(lang, "err.messenger_dispatch_failed"), lastErr)
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
					output = i18n.T(lang, "err.messenger_job_status", "job", jobID, "status", status, "exit", code)
				}
				return messenger.AgentList{}, errors.New(strings.TrimSpace(output))
			}
			return messenger.ParseAgents(output), nil
		}
		if !time.Now().Before(deadline) {
			return messenger.AgentList{}, errors.New(i18n.T(lang, "err.messenger_list_timeout", "job", jobID))
		}
		select {
		case <-ctx.Done():
			return messenger.AgentList{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
