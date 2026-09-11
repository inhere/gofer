package tunnel

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Binding associates a rendezvous with its identities.
type Binding struct {
	TunnelID     string
	WorkerID     string
	InstanceID   string
	CallerID     string
	Target       string
	ClientRemote string
}

// Arrival is a worker websocket and hello.
type Arrival struct {
	Conn  *websocket.Conn
	Hello Hello
}

// ErrRendezvousTimeout indicates no worker arrived before the rendezvous deadline.
var ErrRendezvousTimeout = errors.New("rendezvous timeout")

// Pending tracks Begin -> Wait/Deliver -> Finish or Cancel; done closes when the rendezvous lifecycle ends.
type Pending struct {
	r     *Registry
	b     Binding
	nonce string
	ch    chan Arrival
	done  chan struct{}
	once  sync.Once
}

// Registry tracks pending and active tunnels.
type Registry struct {
	mu      sync.Mutex
	pending map[string]*Pending
	nonces  map[string]struct {
		binding Binding
		exp     time.Time
	}
	active map[string]*Info
}

// NewRegistry creates a registry.
func NewRegistry() *Registry {
	return &Registry{pending: map[string]*Pending{}, nonces: map[string]struct {
		binding Binding
		exp     time.Time
	}{}, active: map[string]*Info{}}
}

// Begin creates a pending rendezvous.
func (r *Registry) Begin(b Binding, ttl time.Duration) (*Pending, string) {
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		panic(err)
	}
	b.TunnelID = "t-" + hex.EncodeToString(id)
	nb := make([]byte, 32)
	if _, err := rand.Read(nb); err != nil {
		panic(err)
	}
	n := hex.EncodeToString(nb)
	p := &Pending{r: r, b: b, nonce: n, ch: make(chan Arrival, 1), done: make(chan struct{})}
	r.mu.Lock()
	now := time.Now()
	for k, v := range r.nonces {
		if now.After(v.exp) {
			delete(r.nonces, k)
		}
	}
	r.pending[b.TunnelID] = p
	r.nonces[n] = struct {
		binding Binding
		exp     time.Time
	}{b, time.Now().Add(ttl)}
	r.mu.Unlock()
	return p, n
}

// TunnelID returns id.
func (p *Pending) TunnelID() string { return p.b.TunnelID }

// Wait waits for worker arrival.
func (p *Pending) Wait(ctx context.Context, t time.Duration) (Arrival, error) {
	tm := time.NewTimer(t)
	defer tm.Stop()
	select {
	case a := <-p.ch:
		return a, nil
	case <-tm.C:
		p.Cancel()
		return Arrival{}, ErrRendezvousTimeout
	case <-ctx.Done():
		p.Cancel()
		return Arrival{}, ctx.Err()
	}
}

// Cancel removes pending rendezvous.
func (p *Pending) Cancel() {
	p.once.Do(func() {
		p.r.mu.Lock()
		delete(p.r.pending, p.b.TunnelID)
		delete(p.r.nonces, p.nonce)
		p.r.mu.Unlock()
		close(p.done)
	})
}

// Finish closes completion signal.
func (p *Pending) Finish() {
	p.once.Do(func() {
		p.r.mu.Lock()
		delete(p.r.pending, p.b.TunnelID)
		delete(p.r.nonces, p.nonce)
		p.r.mu.Unlock()
		close(p.done)
	})
}

// Consume validates and consumes a nonce once; expired or unknown nonces return ok=false.
func (r *Registry) Consume(n string, now time.Time) (Binding, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.nonces[n]
	if !ok || !now.Before(v.exp) {
		delete(r.nonces, n)
		return Binding{}, false
	}
	delete(r.nonces, n)
	return v.binding, true
}

// Deliver supplies a worker arrival once; expired or unknown rendezvous, or an already delivered arrival, return ok=false. Callers must block on the returned done channel.
func (r *Registry) Deliver(id string, a Arrival) (<-chan struct{}, bool) {
	r.mu.Lock()
	p, ok := r.pending[id]
	r.mu.Unlock()
	if !ok {
		return nil, false
	}
	select {
	case p.ch <- a:
		return p.done, true
	default:
		return nil, false
	}
}

// Info describes active tunnel.
type Info struct {
	ID, CallerID, WorkerID, Target, ClientRemote string
	StartedAt                                    time.Time
	BytesUp, BytesDown                           int64
}

// Activate adds an active tunnel. Progress values are cumulative, driven by Splice OnProgress.
func (r *Registry) Activate(i Info) (func(int64, int64), func()) {
	r.mu.Lock()
	r.active[i.ID] = &i
	r.mu.Unlock()
	return func(u, d int64) {
		r.mu.Lock()
		if x := r.active[i.ID]; x != nil {
			x.BytesUp = u
			x.BytesDown = d
		}
		r.mu.Unlock()
	}, func() { r.mu.Lock(); delete(r.active, i.ID); r.mu.Unlock() }
}

// List returns active tunnels.
func (r *Registry) List() []Info {
	r.mu.Lock()
	defer r.mu.Unlock()
	o := make([]Info, 0, len(r.active))
	for _, i := range r.active {
		o = append(o, *i)
	}
	return o
}
