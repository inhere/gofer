package wsproto

// TerminalLogResumeMinProtocolVersion is the first protocol version whose resume ack
// (Registered.Resume) also lists a job the worker reported as already finished, with the
// hub's durable stdout/stderr byte counts. The worker rewinds to them and re-sends the
// log tail it wrote while disconnected BEFORE it replays the job's Outcome/Result. It
// does NOT raise MinProtocolVersion and adds no wire field: below it the ack lists only
// jobs that are still running, exactly as before.
const TerminalLogResumeMinProtocolVersion = 33

// SupportsTerminalLogResume reports whether a worker that registered with protocol
// version proto is sent resume entries for its finished-but-unconfirmed jobs.
func SupportsTerminalLogResume(proto int) bool { return proto >= TerminalLogResumeMinProtocolVersion }
