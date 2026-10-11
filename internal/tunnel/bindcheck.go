package tunnel

import (
	"fmt"
	"net"
)

// interfaceAddrs lists this machine's addresses; a seam so the precheck is testable
// without touching the host's interfaces.
var interfaceAddrs = net.InterfaceAddrs

// CheckBindAddrs verifies, before any listener starts, that every rule binds an
// address this machine actually has. A same-address rule (`=ip:port`) needs the
// target IP added to a local (loopback) adapter first; without this check the
// operator gets a bare "cannot assign requested address" — and with several rules
// the ones before the bad one would already be listening.
//
// Loopback, unspecified (0.0.0.0 / ::) and host-name binds are not checked.
func CheckBindAddrs(specs []ForwardSpec) error {
	var have map[string]bool
	for i, sp := range specs {
		ip := net.ParseIP(sp.Bind)
		if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		if have == nil {
			addrs, err := interfaceAddrs()
			if err != nil {
				return nil // cannot tell: let the listen itself report
			}
			have = make(map[string]bool, len(addrs))
			for _, a := range addrs {
				if n, ok := a.(*net.IPNet); ok {
					have[n.IP.String()] = true
				}
			}
		}
		if !have[ip.String()] {
			return fmt.Errorf("spec #%d (listen %s): %w", i+1, sp.ListenAddr(), bindAddrError(sp.Bind))
		}
	}
	return nil
}

// bindAddrError is the actionable message for a bind address the machine lacks.
func bindAddrError(bind string) error {
	mask := "/32"
	if ip := net.ParseIP(bind); ip != nil && ip.To4() == nil {
		mask = "/128"
	}
	return fmt.Errorf("bind address %s is not assigned to this machine; add it to a loopback adapter first "+
		"(Linux: sudo ip addr add %s%s dev lo; macOS: sudo ifconfig lo0 alias %s%s; Windows: Microsoft KM-TEST Loopback Adapter)",
		bind, bind, mask, bind, mask)
}

// listenError adds the bind-address hint to a listen failure caused by an address
// the machine does not have (EADDRNOTAVAIL / WSAEADDRNOTAVAIL) — the fallback for a
// listener started without CheckBindAddrs.
func listenError(spec ForwardSpec, err error) error {
	if err == nil || !isAddrNotAvail(err) {
		return err
	}
	return fmt.Errorf("%w: %v", err, bindAddrError(spec.Bind))
}
