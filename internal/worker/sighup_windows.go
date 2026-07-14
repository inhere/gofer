//go:build windows

package worker

import "os"

// notifyReloadSignal is a no-op on Windows: there is no SIGHUP, so a worker there
// is reloaded through the remote path only (the hub sends a reload frame) — which
// is the primary path on every platform anyway. Nothing is ever delivered to ch,
// so the signal goroutine in Serve simply idles until shutdown.
func notifyReloadSignal(_ chan<- os.Signal) {}
