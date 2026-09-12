package tunnel

import (
	"context"
	"github.com/coder/websocket"
	"io"
	"net"
	"sync"
)

// DatagramBridge maps one UDP datagram to one binary websocket message.
//
// The device socket is deliberately UNCONNECTED. A connected UDP socket only
// receives datagrams whose source is exactly the target address, and devices
// commonly answer from a different port than the one they were asked on — those
// replies are dropped by the kernel and the exchange just times out, which looks
// exactly like a dead tunnel. Replies are therefore accepted from any port of the
// target's IP; anything from another host is ignored so an unrelated sender on the
// device network cannot inject traffic into someone's tunnel.
func DatagramBridge(ctx context.Context, ws *websocket.Conn, pc net.PacketConn, target net.Addr) (toWS, fromWS int64, err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var once sync.Once
	stop := func() { once.Do(func() { pc.Close(); ws.Close(websocket.StatusNormalClosure, "closed") }) }
	targetIP := addrIP(target)
	type result struct {
		n   int64
		err error
		out bool
	}
	ch := make(chan result, 2)
	go func() {
		var n int64
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
				}
			}
			if e != nil {
				ch <- result{n, e, true}
				stop()
				return
			}
		}
	}()
	go func() {
		var n int64
		for {
			typ, r, e := ws.Reader(ctx)
			if e != nil {
				ch <- result{n, e, false}
				stop()
				return
			}
			if typ != websocket.MessageBinary {
				io.Copy(io.Discard, r)
				continue
			}
			b, e := io.ReadAll(io.LimitReader(r, ReadLimit+1))
			if e == nil && len(b) <= ReadLimit {
				_, e = pc.WriteTo(b, target)
				if e == nil {
					n += int64(len(b))
				}
			} else if e == nil {
				e = io.ErrShortBuffer
			}
			if e != nil {
				ch <- result{n, e, false}
				stop()
				return
			}
		}
	}()
	a, b := <-ch, <-ch
	if a.out {
		toWS = a.n
		fromWS = b.n
	} else {
		fromWS = a.n
		toWS = b.n
	}
	if a.err == nil || a.err == io.EOF || a.err == net.ErrClosed || websocket.CloseStatus(a.err) == websocket.StatusNormalClosure {
		return toWS, fromWS, nil
	}
	return toWS, fromWS, a.err
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
