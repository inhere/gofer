package tunnel

import "log/slog"

// processEvents are the per-connection steps of OPENING one tunnel: the request,
// the rendezvous, the first byte in each direction. They are useful when a tunnel
// hangs, and pure noise otherwise — one forwarder serves every local connection, so
// a busy listener wrote hundreds of these lines a day and buried everything else.
// opened / closed describe the tunnel itself and stay at INFO.
var processEvents = map[string]struct{}{
	"tunnel.requested":          {},
	"tunnel.rendezvous_started": {},
	"tunnel.worker_connected":   {},
	"tunnel.first_up":           {},
	"tunnel.first_down":         {},
	"session.opened":            {},
}

// LogLevel is the level an observation sink should log one Forwarder event at
// (Forwarder.OnEvent / the server's and worker's tunnel logs share this vocabulary):
// DEBUG for a process step, INFO for the lines that describe a tunnel on their own.
//
// An event this table does not know stays INFO: a new event should be visible until
// someone decides it is noise.
func LogLevel(event string) slog.Level {
	if _, ok := processEvents[event]; ok {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}
