package tunnel

import (
	"bytes"
	"context"
	"github.com/coder/websocket"
	"io"
	"net"
	"os"
	"sync"
)

var datagramReadPool = sync.Pool{New: func() any { return make([]byte, ReadLimit+1) }}
var udpBufferPoolEnabled = os.Getenv("GOFER_UDP_BUFFER_POOL") != "0"

func readDatagramMessage(r io.Reader) ([]byte, func(), error) {
	if !udpBufferPoolEnabled {
		b, err := io.ReadAll(io.LimitReader(r, ReadLimit+1))
		return b, func() {}, err
	}
	buf := datagramReadPool.Get().([]byte)
	b := bytes.NewBuffer(buf[:0])
	_, err := io.CopyN(b, r, int64(ReadLimit+1))
	if err != nil && err != io.EOF {
		datagramReadPool.Put(buf)
		return nil, func() {}, err
	}
	return b.Bytes(), func() { datagramReadPool.Put(buf) }, nil
}

// DatagramBridge maps one UDP datagram to one binary websocket message.
//
// The device socket is deliberately UNCONNECTED. A connected UDP socket only
// receives datagrams whose source is exactly the target address, and devices
// commonly answer from a different port than the one they were asked on — those
// replies are dropped by the kernel and the exchange just times out, which looks
// exactly like a dead tunnel. Replies are therefore accepted from any port of the
// target's IP; anything from another host is ignored so an unrelated sender on the
// device network cannot inject traffic into someone's tunnel.
func DatagramBridge(ctx context.Context, ws *websocket.Conn, pc net.PacketConn, target net.Addr) (int64, int64, error) {
	r := DatagramBridgeWithOptions(ctx, ws, pc, target, BridgeOptions{})
	return r.ToWS, r.FromWS, r.Err
}

// DatagramBridgeWithOptions adds first-datagram callbacks and teardown cause.
func DatagramBridgeWithOptions(ctx context.Context, ws *websocket.Conn, pc net.PacketConn, target net.Addr, opt BridgeOptions) BridgeResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	var firstUp, firstDown sync.Once
	stop := func() { once.Do(func() { pc.Close(); ws.Close(websocket.StatusNormalClosure, "closed") }) }
	targetIP := addrIP(target)
	type result struct {
		n, pkts int64
		err     error
		out     bool
	}
	ch := make(chan result, 2)
	go func() {
		var n, pkts int64
		b := make([]byte, ReadLimit)
		for {
			nr, from, e := pc.ReadFrom(b)
			if nr > 0 {
				if !sameHost(targetIP, from) {
					continue // not our device: ignore rather than relay
				}
				if e2 := ws.Write(ctx, websocket.MessageBinary, b[:nr]); e2 != nil {
					e = e2
				} else {
					n += int64(nr)
					pkts++
					firstDown.Do(func() {
						if opt.OnFirstDown != nil {
							opt.OnFirstDown()
						}
					})
				}
			}
			if e != nil {
				ch <- result{n, pkts, e, true}
				stop()
				return
			}
		}
	}()
	go func() {
		var n, pkts int64
		for {
			typ, r, e := ws.Reader(ctx)
			if e != nil {
				ch <- result{n, pkts, e, false}
				stop()
				return
			}
			if typ != websocket.MessageBinary {
				io.Copy(io.Discard, r)
				continue
			}
			b, release, e := readDatagramMessage(r)
			if e == nil && len(b) <= ReadLimit {
				_, e = pc.WriteTo(b, target)
				release()
				if e == nil {
					n += int64(len(b))
					pkts++
					firstUp.Do(func() {
						if opt.OnFirstUp != nil {
							opt.OnFirstUp()
						}
					})
				}
			} else if e == nil {
				release()
				e = io.ErrShortBuffer
			}
			if e != nil {
				ch <- result{n, pkts, e, false}
				stop()
				return
			}
		}
	}()
	a, b := <-ch, <-ch
	out, in := a, b // out = the device->ws pump, in = the ws->device pump
	if !a.out {
		out, in = b, a
	}
	reason, err := closeReason(ctx, a.err, a.out)
	return BridgeResult{ToWS: out.n, FromWS: in.n, PacketsToWS: out.pkts, PacketsFromWS: in.pkts, Reason: reason, Err: err}
}

// addrIP extracts the IP of a UDP address, or nil when it cannot be determined
// (in which case sameHost accepts everything, matching a plain relay).
func addrIP(a net.Addr) net.IP {
	if u, ok := a.(*net.UDPAddr); ok {
		return u.IP
	}
	if host, _, err := net.SplitHostPort(a.String()); err == nil {
		return net.ParseIP(host)
	}
	return nil
}

// sameHost reports whether a reply came from the device we are talking to,
// ignoring the port it chose to answer from.
func sameHost(want net.IP, from net.Addr) bool {
	if want == nil {
		return true
	}
	got := addrIP(from)
	return got != nil && got.Equal(want)
}
