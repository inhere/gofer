package worker

import (
	"os"
	"sort"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/messenger"
	"github.com/inhere/gofer/internal/wsproto"
)

// DirsFunc reports where this worker runs things: its roots mapping (POLICY mode)
// and the directory every served project currently resolves to. It is injected by
// the command layer, which owns worker.yaml and the live config (G021); existence
// is checked here, at report time, so a directory that vanishes later shows up on
// the next heartbeat.
type DirsFunc func() (roots []config.WorkerRoot, projects map[string]string)

// stateReport builds the optional worker state a heartbeat ping carries (v16):
// the resident messenger snapshot and the working-directory report.
func (cl *Client) stateReport() (*wsproto.MessengerSnapshot, *wsproto.WorkDirs) {
	return messengerSnapshot(cl.residentMessenger.Snapshot(builtinLocalRunner)), cl.workDirs()
}

func messengerSnapshot(s messenger.Snapshot) *wsproto.MessengerSnapshot {
	out := &wsproto.MessengerSnapshot{Status: s.Status, StartedAt: s.StartedAt, LastUsedAt: s.LastUsedAt,
		IdleDeadline: s.IdleDeadline, StderrTail: s.StderrTail}
	for _, d := range s.Deliveries {
		out.Deliveries = append(out.Deliveries, wsproto.MessengerDelivery{At: d.At, Op: d.Op, Target: d.Target,
			Message: d.Message, OK: d.OK, Error: d.Error, DurationMS: d.DurationMS})
	}
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (cl *Client) workDirs() *wsproto.WorkDirs {
	out := &wsproto.WorkDirs{}
	if ws, err := config.WorkspaceDir(""); err == nil && ws != "" {
		out.Workspace = &wsproto.WorkDir{Path: ws, Exists: dirExists(ws)}
	}
	if cl.dirsFn == nil {
		return out
	}
	roots, projects := cl.dirsFn()
	for _, r := range roots {
		out.Roots = append(out.Roots, wsproto.WorkRoot{From: r.From, To: r.To, Exists: dirExists(r.To)})
	}
	keys := make([]string, 0, len(projects))
	for k := range projects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Projects = append(out.Projects, wsproto.ProjectDir{Key: k, Path: projects[k], Exists: dirExists(projects[k])})
	}
	return out
}
