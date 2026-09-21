package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/xfer"
)

// TestXferEventDeliveredToWebhook: a transfer's audit event reaches the SAME
// notification pipeline a job event does (XFER-01 X2) — a webhook that subscribes to
// `xfer.put` is enqueued a delivery for it, and the delivery's source event is the
// transfer's own (recorded under the synthetic `xfer:<id>` scope). Before X2 the
// transfer's event only landed in the event log, so an operator could see it in the
// stream but never subscribe an IM/webhook to it.
func TestXferEventDeliveredToWebhook(t *testing.T) {
	target := "https://hooks.example.test/xfer"
	s, mgr, _ := newXferServer(t, config.ServerConfig{
		Token: "tok",
		Notification: &config.NotificationConfig{
			Webhooks: []config.WebhookConfig{{URL: target, Events: []string{"xfer.put"}}},
		},
	}, xfer.Limits{MaxBytes: 1 << 20})

	payload := []byte("firmware")
	sum := sha256.Sum256(payload)
	rec, err := mgr.StagePut("tester", "local", "demo", "tmp/in/a.bin", int64(len(payload)), hex.EncodeToString(sum[:]), false)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}
	w, err := mgr.Store().Writer(rec.ID)
	if err != nil {
		t.Fatalf("Store().Writer: %v", err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatalf("write staged payload: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close staged payload: %v", err)
	}
	if err := mgr.CommitPut(rec.ID, int64(len(payload)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("CommitPut: %v", err)
	}
	if err := mgr.MarkDone(rec.ID); err != nil {
		t.Fatalf("MarkDone: %v", err)
	}

	scope := xfer.EventJobID(rec.ID)
	evs, err := s.jobs.ListJobEvents(scope, 0)
	if err != nil {
		t.Fatalf("ListJobEvents: %v", err)
	}
	if len(evs) != 1 || evs[0].Type != "xfer.put" {
		t.Fatalf("events = %+v, want one xfer.put", evs)
	}
	deliveries, err := s.jobs.ListDeliveriesByJob(scope)
	if err != nil {
		t.Fatalf("ListDeliveriesByJob: %v", err)
	}
	if len(deliveries) != 1 {
		t.Fatalf("deliveries = %+v, want one for the subscribed webhook", deliveries)
	}
	if deliveries[0].Target != target || deliveries[0].Status != jobstore.DeliveryPending {
		t.Fatalf("delivery = %+v, want a pending delivery to %s", deliveries[0], target)
	}
	if deliveries[0].EventSeq != evs[0].Seq {
		t.Fatalf("delivery event seq = %d, want the xfer.put event's %d", deliveries[0].EventSeq, evs[0].Seq)
	}

	// The default trigger set did NOT grow: a transfer event nobody subscribed to
	// enqueues nothing.
	getRec, err := mgr.StageGet("tester", "local", "demo", "tmp/out/b.csv")
	if err != nil {
		t.Fatalf("StageGet: %v", err)
	}
	if err := mgr.MarkDone(getRec.ID); err != nil {
		t.Fatalf("MarkDone(get): %v", err)
	}
	if ds, err := s.jobs.ListDeliveriesByJob(xfer.EventJobID(getRec.ID)); err != nil || len(ds) != 0 {
		t.Fatalf("deliveries for an unsubscribed xfer.get = %+v (err=%v), want none", ds, err)
	}
}
