// Package proctree holds one spawned process and the descendants it spawns, so a
// caller can kill the WHOLE tree instead of only the direct child.
//
// Why it exists (ACP-02 real-machine finding F12): a job's agent process is often a
// launcher (npx → node → the adapter itself). Killing only the direct child leaves the
// grandchildren ALIVE — and typically still holding the job's stdout/stderr pipes — so
// os/exec's Wait never returns (its pipe-copy goroutine waits for an EOF that cannot
// come) and the job never reaches a terminal state: a cancelled, long-timed-out ACP job
// stayed `running` and kept its working-directory lock until the tree was killed by
// hand.
//
// Platform containment:
//
//   - Windows: the process is put in a JOB OBJECT created with
//     JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE (CreateJobObject +
//     AssignProcessToJobObject), so every descendant joins it unless it explicitly
//     breaks away, and TerminateJobObject / closing the handle ends the whole tree in
//     one call;
//   - unix: the process becomes its own process GROUP leader (Setpgid) and the group
//     is signalled as a whole (kill(-pgid, SIGKILL)).
//
// Usage is always the same three steps:
//
//	tree := proctree.New()
//	tree.Configure(cmd)   // BEFORE cmd.Start
//	... cmd.Start() ...
//	_ = tree.Attach(cmd)  // AFTER cmd.Start
//	defer tree.Release()
//	... tree.Kill() ...   // whenever the whole tree must die
//
// Every method tolerates a nil receiver and a failed Attach, so a caller that could not
// build the containment still runs — degraded to the direct-child-only behaviour it had
// before. This package is a PRIMITIVE, not a policy: the caller decides whether ending
// the containment may take survivors with it (see Release).
package proctree
