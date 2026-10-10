package ptyrelay

import (
	"sync"
	"testing"
)

// fanout snapshots the viewers and then sends without the relay lock, so a viewer
// closed in between (Relay.Close / removeViewer) used to receive a send on a closed
// channel: a data race under -race and a panic otherwise (gofer-r7am, Linux
// acceptance run 2, TestE2EPtyExternalDisconnectFailsJob).
func TestFanoutRacingViewerCloseNeverSendsOnClosedChannel(t *testing.T) {
	for i := 0; i < 200; i++ {
		v := &Viewer{id: 1, out: make(chan []byte, 1)}
		r := &Relay{viewers: map[int]*Viewer{1: v}}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				r.fanout([]byte("x"))
			}
		}()
		go func() {
			defer wg.Done()
			v.closeOut()
		}()
		wg.Wait()
	}
}
