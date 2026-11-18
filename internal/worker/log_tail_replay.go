package worker

import (
	"context"
	"path/filepath"

	"github.com/coder/websocket"

	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/wsproto"
)

// inflightSetLogDir records the local job's log directory on its in-flight entry.
func (cl *Client) inflightSetLogDir(remoteID, dir string) {
	cl.inflMu.Lock()
	if f := cl.inflight[remoteID]; f != nil {
		f.logDir = dir
	}
	cl.inflMu.Unlock()
}

func (cl *Client) inflightLogDir(remoteID string) string {
	cl.inflMu.Lock()
	defer cl.inflMu.Unlock()
	if f := cl.inflight[remoteID]; f != nil {
		return f.logDir
	}
	return ""
}

// replayLogTail re-sends the stdout/stderr (and acp mirror) bytes of a FINISHED job that
// never reached the hub: the tailer's final drain failed while the connection was down,
// and the resume ack has since rewound the offsets to what the hub persisted. It runs on
// the handshake connection before the job's Outcome/Result, so the hub has the whole log
// when the Result finishes the job. Offsets advance only after a successful write, so a
// failure (reported as false) keeps the rest for the next reconnect. A job without a
// recorded log directory has nothing to send.
func (cl *Client) replayLogTail(ctx context.Context, conn *websocket.Conn, remoteID string) bool {
	dir := cl.inflightLogDir(remoteID)
	if dir == "" {
		return true
	}
	for _, ent := range []struct{ stream, file string }{
		{string(store.StreamStdout), store.StdoutFile},
		{string(store.StreamStderr), store.StderrFile},
	} {
		chunk, next := tailFrom(filepath.Join(dir, ent.file), cl.inflightOffset(remoteID, ent.stream))
		if len(chunk) == 0 {
			continue
		}
		seq := cl.inflightSeq(remoteID) + 1
		if err := cl.writeFrameOn(ctx, conn, wsproto.TypeLog, remoteID, wsproto.Log{
			JobID: remoteID, Stream: ent.stream, Seq: int(seq), Text: string(chunk),
		}); err != nil {
			return false
		}
		cl.inflightCommit(remoteID, ent.stream, next, seq)
	}
	if !wsproto.SupportsACPMirror(int(cl.serverProto.Load())) {
		return true
	}
	acpPath := runner.ACPArtifactPath(dir)
	for {
		chunk, next := acpMirrorChunk(acpPath, cl.inflightOffset(remoteID, wsproto.LogStreamACP), maxACPMirrorFrameBytes)
		if len(chunk) == 0 {
			return true
		}
		seq := cl.inflightSeq(remoteID) + 1
		if err := cl.writeFrameOn(ctx, conn, wsproto.TypeLog, remoteID, wsproto.Log{
			JobID: remoteID, Stream: wsproto.LogStreamACP, Seq: int(seq), Text: string(chunk),
		}); err != nil {
			return false
		}
		cl.inflightCommit(remoteID, wsproto.LogStreamACP, next, seq)
	}
}
