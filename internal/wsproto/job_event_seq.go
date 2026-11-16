package wsproto

// JobEventSeqMinProtocolVersion is the first protocol version that numbers a job's
// mirrored events (JobEvent.Seq) and carries the resume cursors (ResumeJob.EventSeq /
// AcpOff). It does NOT raise MinProtocolVersion: below it a worker keeps today's
// behaviour (events are sent once and lost on a dropped connection, the hub dedups per
// connection, the "acp" stream is never rewound).
const JobEventSeqMinProtocolVersion = 30

// SupportsJobEventSeq reports whether a peer that registered with protocol version
// proto takes part in job-event replay. BOTH ends must satisfy it before a worker
// buffers or replays events.
func SupportsJobEventSeq(proto int) bool { return proto >= JobEventSeqMinProtocolVersion }
