package tunnel

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func stubInterfaceAddrs(t *testing.T, ips ...string) {
	t.Helper()
	prev := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = prev })
	interfaceAddrs = func() ([]net.Addr, error) {
		out := make([]net.Addr, 0, len(ips))
		for _, s := range ips {
			out = append(out, &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(32, 32)})
		}
		return out, nil
	}
}

func TestCheckBindAddrs(t *testing.T) {
	stubInterfaceAddrs(t, "127.0.0.1", "10.20.1.5")
	ok := []ForwardSpec{
		{Bind: "127.0.0.1", LocalPort: 1}, {Bind: "127.0.0.9", LocalPort: 1}, {Bind: "::1", LocalPort: 1},
		{Bind: "0.0.0.0", LocalPort: 1}, {Bind: "::", LocalPort: 1}, {Bind: "localhost", LocalPort: 1},
		{Bind: "10.20.1.5", LocalPort: 502},
	}
	if err := CheckBindAddrs(ok); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	err := CheckBindAddrs(append(ok, ForwardSpec{Bind: "10.20.1.6", LocalPort: 502}))
	if err == nil {
		t.Fatal("want an error for an address this machine lacks")
	}
	for _, want := range []string{"spec #8", "10.20.1.6:502", "not assigned to this machine", "ip addr add 10.20.1.6/32 dev lo", "KM-TEST Loopback"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
}

func TestCheckBindAddrsInterfaceErrorDefersToListen(t *testing.T) {
	prev := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = prev })
	interfaceAddrs = func() ([]net.Addr, error) { return nil, errors.New("boom") }
	if err := CheckBindAddrs([]ForwardSpec{{Bind: "10.20.1.6", LocalPort: 502}}); err != nil {
		t.Fatalf("an unreadable interface list must not block: %v", err)
	}
}

// A listener started without the precheck still explains a missing bind address.
// 192.0.2.0/24 (TEST-NET-1) is never assigned to a real interface.
func TestForwarderListenMissingAddressHint(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		f := &Forwarder{Spec: ForwardSpec{Network: network, Bind: "192.0.2.77", LocalPort: 0, Target: "192.0.2.77:1"}, Ready: make(chan error, 1)}
		err := f.Run(t.Context())
		if err == nil {
			t.Fatalf("%s: listening on 192.0.2.77 unexpectedly succeeded", network)
		}
		if !strings.Contains(err.Error(), "not assigned to this machine") {
			t.Errorf("%s: error %q lacks the bind-address hint", network, err)
		}
	}
}
