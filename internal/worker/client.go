// Package worker is the ws-worker client (main plan §4, §6): a `gofer worker`
// process dials the central hub over a single WebSocket, registers, and then
// receives job dispatches which it runs LOCALLY with its own job.Service /
// local runner (review #8: the worker re-validates project/agent/exec with its
// own config). It streams each local job's stdout/stderr back to the hub as log
// frames and pushes the authoritative terminal result.
//
// WP1 scope: single hub URL (URLs[0]); no reconnect/heartbeat (C7/P3). The read
// loop exits on disconnect.
package worker

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// maxWSReadBytes caps a single inbound message on the worker side (mirrors the
// hub). Var so tests can shrink it.
var maxWSReadBytes int64 = 8 << 20

// builtinLocalRunner is the runner the worker always executes with locally.
const builtinLocalRunner = "local"

// Jobs is the subset of job.Service the worker client needs. job.Service
// satisfies it; an interface keeps the client testable.
type Jobs interface {
	Submit(req job.JobRequest) (job.JobResult, error)
	Get(id string) (job.JobResult, bool)
	Wait(id string) (job.JobResult, bool)
	// Cancel cancels a running local job (P2 cancel frame). Stable no-op for a
	// terminal job, error only for an unknown id (mirrors job.Service.Cancel).
	Cancel(id string) error
	// GetInteractions returns the local job's interactions so the client can bridge
	// new pending ones to the hub as interaction{open} frames (P2).
	GetInteractions(id string) ([]job.Interaction, error)
	// AnswerInteraction delivers the hub's answer to the local job so it resumes
	// (P2 answer frame).
	AnswerInteraction(jobID, interactionID, answer string) (job.Interaction, error)
}

// Client connects one worker to the hub. It is constructed with a resolved
// dial URL + token + the worker's identity and local job service.
type Client struct {
	workerID string
	url      string
	token    string
	labels   []string
	projects []string
	agents   []string
	maxConc  int

	jobs Jobs

	conn    *websocket.Conn
	writeMu sync.Mutex

	// jobMap maps the hub-side job_id (the wire id) to the worker's LOCAL job id,
	// so an inbound cancel/answer frame (keyed by the hub id) targets the right
	// local job. handleDispatch registers the entry once the local job is submitted
	// and removes it when the dispatch finishes.
	jobMu  sync.Mutex
	jobMap map[string]string

	// pollInterval is how often streamLocalJob tails the local log files. Var per
	// instance so tests can speed it up.
	pollInterval time.Duration
}

// Config is the resolved worker-client wiring (the command resolves env/URLs).
type Config struct {
	WorkerID string
	URL      string
	Token    string
	Labels   []string
	Projects []string
	Agents   []string
	MaxConc  int
}

// New builds a worker client. jobs is the worker's local job service (built from
// its own config by the command).
func New(cfg Config, jobs Jobs) *Client {
	return &Client{
		workerID:     cfg.WorkerID,
		url:          cfg.URL,
		token:        cfg.Token,
		labels:       cfg.Labels,
		projects:     cfg.Projects,
		agents:       cfg.Agents,
		maxConc:      cfg.MaxConc,
		jobs:         jobs,
		jobMap:       map[string]string{},
		pollInterval: 200 * time.Millisecond,
	}
}

// putJobMapping records the hub job_id → local job id mapping (handleDispatch).
func (cl *Client) putJobMapping(remoteID, localID string) {
	cl.jobMu.Lock()
	cl.jobMap[remoteID] = localID
	cl.jobMu.Unlock()
}

// localJobID resolves the hub job_id to the worker's local job id (empty if the
// dispatch is unknown / already cleaned up).
func (cl *Client) localJobID(remoteID string) string {
	cl.jobMu.Lock()
	defer cl.jobMu.Unlock()
	return cl.jobMap[remoteID]
}

// dropJobMapping removes the mapping once a dispatch finishes.
func (cl *Client) dropJobMapping(remoteID string) {
	cl.jobMu.Lock()
	delete(cl.jobMap, remoteID)
	cl.jobMu.Unlock()
}

// Run dials the hub, registers and enters the dispatch-receive loop. It returns
// when the connection drops or ctx ends (WP1: no reconnect).
func (cl *Client) Run(ctx context.Context) error {
	header := http.Header{}
	if cl.token != "" {
		header.Set("Authorization", "Bearer "+cl.token)
	}
	conn, _, err := websocket.Dial(ctx, cl.url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return fmt.Errorf("dial hub: %w", err)
	}
	conn.SetReadLimit(maxWSReadBytes)
	cl.conn = conn
	defer conn.Close(websocket.StatusNormalClosure, "worker shutdown")

	// register → registered.
	if err := cl.writeFrame(ctx, wsproto.TypeRegister, "", wsproto.Register{
		WorkerID:      cl.workerID,
		Labels:        cl.labels,
		Projects:      cl.projects,
		Agents:        cl.agents,
		MaxConcurrent: cl.maxConc,
	}); err != nil {
		return fmt.Errorf("send register: %w", err)
	}
	env, err := cl.readEnvelope(ctx)
	if err != nil {
		return fmt.Errorf("read registered: %w", err)
	}
	reg, _ := wsproto.As[wsproto.Registered](env)
	if !reg.Accepted {
		return fmt.Errorf("register rejected: %s", reg.Reason)
	}

	// dispatch-receive loop (single read goroutine; each dispatch handled in its
	// own goroutine so the worker runs multiple jobs concurrently).
	for {
		env, err := cl.readEnvelope(ctx)
		if err != nil {
			return err // disconnect / ctx done
		}
		switch env.Type {
		case wsproto.TypeDispatch:
			d, derr := wsproto.As[wsproto.Dispatch](env)
			if derr != nil {
				continue
			}
			go cl.handleDispatch(ctx, d)
		case wsproto.TypeCancel:
			// P2: cancel the matching local job. job.Service.Cancel is a stable no-op
			// for a terminal/unknown local job, so an unmapped/late cancel is safe.
			cf, derr := wsproto.As[wsproto.Cancel](env)
			if derr != nil {
				continue
			}
			if localID := cl.localJobID(cf.JobID); localID != "" {
				_ = cl.jobs.Cancel(localID)
			}
		case wsproto.TypeAnswer:
			// P2: deliver the hub answer to the local job so it resumes. The
			// interaction id is the LOCAL id (the worker generated it on the open
			// frame), so it maps 1:1.
			af, derr := wsproto.As[wsproto.Answer](env)
			if derr != nil {
				continue
			}
			if localID := cl.localJobID(af.JobID); localID != "" {
				_, _ = cl.jobs.AnswerInteraction(localID, af.InteractionID, af.Answer)
			}
		case wsproto.TypePing:
			// P3 placeholder: heartbeat.
		}
	}
}

// writeFrame marshals a typed payload into an envelope and writes it under
// writeMu (coder/websocket requires a single concurrent writer).
func (cl *Client) writeFrame(ctx context.Context, t wsproto.FrameType, jobID string, payload any) error {
	cl.writeMu.Lock()
	defer cl.writeMu.Unlock()
	return wsjson.Write(ctx, cl.conn, wsproto.Envelope{Type: t, JobID: jobID, Payload: mustRaw(payload)})
}

func (cl *Client) readEnvelope(ctx context.Context) (wsproto.Envelope, error) {
	var env wsproto.Envelope
	if err := wsjson.Read(ctx, cl.conn, &env); err != nil {
		return wsproto.Envelope{}, err
	}
	return env, nil
}
