//go:build !windows

package worker

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyReloadSignal subscribes ch to the local config-reload signal (SIGHUP), the
// unix way to ask a running worker to re-read its config: `kill -HUP <pid>`. The
// signal only ENQUEUES a reload — it goes through the very same serial executor as
// a hub-issued reload request, so the two can never apply concurrently or out of
// order (there is exactly one reload path in the worker).
func notifyReloadSignal(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGHUP)
}
