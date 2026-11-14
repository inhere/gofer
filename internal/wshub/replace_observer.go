package wshub

// ReplaceInfo describes one worker connection taking over another with a DIFFERENT
// instance_id: two worker processes sharing a worker_id (or a restart that raced its
// predecessor's teardown). A same-process reconnect is not reported.
type ReplaceInfo struct {
	WorkerID    string
	OldInstance string
	NewInstance string
	OldRemote   string
	NewRemote   string
}

// SetReplaceObserver installs (nil clears) the callback fired when a registration
// replaces a live connection whose instance_id differs. Like the presence observer
// it runs on the connection goroutine and must only record / enqueue; the hub does
// not know about event stores.
func (h *Hub) SetReplaceObserver(fn func(ReplaceInfo)) {
	if fn == nil {
		h.replaceObs.Store(nil)
		return
	}
	h.replaceObs.Store(&fn)
}

// notifyReplaced reports old being replaced by wc when their instance_ids differ.
func (h *Hub) notifyReplaced(old, wc *workerConn) {
	if old == nil || old.instanceID == wc.instanceID {
		return
	}
	if fn := h.replaceObs.Load(); fn != nil {
		(*fn)(ReplaceInfo{WorkerID: wc.workerID, OldInstance: old.instanceID, NewInstance: wc.instanceID,
			OldRemote: old.remoteAddr, NewRemote: wc.remoteAddr})
	}
}
