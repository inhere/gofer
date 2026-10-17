// Package workerenroll is the business layer of worker self-enrollment (design
// 2026-10-17-worker-self-enrollment): the request state machine
// (pending -> accepting -> accepted -> collected, or rejected / expired / superseded /
// cancelled), the pairing code and enroll-secret checks, and the audit trail.
//
// It sits on the data layer (jobstore, config helpers) and reaches everything else
// through narrow interfaces (Registrar, AuditSink, ConfigSource, Limiter,
// SourceBlocker), so it never imports httpapi or core (G022 / G024). The periodic
// sweep, startup reconcile, per-source rate limiting and the hub first-connect hook
// are wired on top of the exported seams (Expire, Settings, MarkCollectedByWorker).
package workerenroll
