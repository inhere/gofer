package trigger

import (
	"context"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// PruneReceipts deletes receipts whose dedup window has passed.
func (e *Engine) PruneReceipts() (int, error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	return e.store.PruneTriggerReceipts(e.now().Unix())
}

// Reconcile settles receipts that stayed claimed longer than the claim timeout (the
// process died between the claim and the end of the submit; design 3.5). The receipt does
// not keep the payload, so the reconciler cannot rebuild the request; it can only find out
// what happened:
//   - the job layer holds a job under the receipt's request id: the submit did succeed,
//     the receipt becomes submitted with that job id;
//   - otherwise nothing was created: the receipt becomes failed, and the sender's
//     redelivery takes it over (Engine.claim) and submits under the same request id.
//
// The updates are compare-and-set on status=claimed, so a request that finishes meanwhile
// is never overwritten. It returns how many receipts were settled.
func (e *Engine) Reconcile(_ context.Context) (int, error) {
	if e == nil || e.store == nil {
		return 0, nil
	}
	stale := e.now().Add(-e.claimTimeout).Unix()
	rows, err := e.store.ListStaleTriggerReceipts(stale, 100)
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, r := range rows {
		status, jobID, msg := jobstore.TriggerReceiptFailed, "", "interrupted before the job was submitted"
		kind := "trigger.reconciled_failed"
		if rec, ok, err := e.store.GetJobByRequestID(r.RequestID); err == nil && ok {
			status, jobID, msg, kind = jobstore.TriggerReceiptSubmitted, rec.ID, "", "trigger.reconciled_submitted"
		} else if err != nil {
			continue
		}
		won, err := e.store.SettleTriggerReceipt(r.TriggerName, r.ExternalEventID, jobstore.TriggerReceiptClaimed, status, jobID, msg)
		if err != nil || !won {
			continue
		}
		settled++
		if e.audit != nil {
			tc := e.triggerByName(r.TriggerName)
			_ = e.audit(jobstore.TriggerAuditEvent{Kind: kind, Trigger: r.TriggerName, Owner: tc.OwnerCaller, Project: tc.Project, Provider: "generic", RequestID: r.RequestID, EventHash: r.ExternalEventID, JobID: jobID, Code: msg})
		}
	}
	return settled, nil
}

func (e *Engine) triggerByName(name string) config.TriggerConfig {
	if cfg := e.config(); cfg != nil {
		for _, t := range cfg.Server.Triggers {
			if t.Name == name {
				return t
			}
		}
	}
	return config.TriggerConfig{}
}

// Maintain runs one pass of the periodic work: settle interrupted claims, then prune
// expired receipts.
func (e *Engine) Maintain(ctx context.Context) (settled, pruned int, err error) {
	settled, err = e.Reconcile(ctx)
	if err != nil {
		return settled, 0, err
	}
	pruned, err = e.PruneReceipts()
	return settled, pruned, err
}

// Run calls Maintain once at start (the startup reconciler) and then every interval until
// stop closes.
func (e *Engine) Run(interval time.Duration, stop <-chan struct{}, onErr func(error)) {
	pass := func() {
		if _, _, err := e.Maintain(context.Background()); err != nil && onErr != nil {
			onErr(err)
		}
	}
	pass()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			pass()
		}
	}
}
