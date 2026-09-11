package tunnel

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ForwardSpec describes a local listener and remote target.
type ForwardSpec struct {
	Bind      string
	LocalPort int
	Target    string
}

// ParseForwardSpec parses [bind:]lport:host:port.
func ParseForwardSpec(s string) (ForwardSpec, error) {
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return ForwardSpec{}, fmt.Errorf("invalid spec")
	}
	port, targetPart := s[idx+1:], s[:idx]
	var targetHost, prefix string
	if strings.HasSuffix(targetPart, "]") {
		open := strings.LastIndex(targetPart, "[")
		if open < 0 || open == 0 || open+1 >= len(targetPart) {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		targetHost, prefix = targetPart[open:], targetPart[:open]
		if !strings.HasSuffix(prefix, ":") {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		prefix = strings.TrimSuffix(prefix, ":")
	} else {
		colon := strings.LastIndex(targetPart, ":")
		if colon < 0 {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		targetHost, prefix = targetPart[colon+1:], targetPart[:colon]
	}
	f := ForwardSpec{Bind: "127.0.0.1"}
	local := prefix
	if strings.HasPrefix(prefix, "[") {
		j := strings.Index(prefix, "]:")
		if j < 0 {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		f.Bind = strings.Trim(prefix[1:j+1], "[]")
		local = prefix[j+2:]
	} else if j := strings.LastIndex(prefix, ":"); j >= 0 {
		f.Bind = prefix[:j]
		local = prefix[j+1:]
	}
	var err error
	f.LocalPort, err = strconv.Atoi(local)
	if err != nil {
		return ForwardSpec{}, fmt.Errorf("invalid local port %q", local)
	}
	f.Target = targetHost + ":" + port
	if f.LocalPort < 0 || f.LocalPort > 65535 {
		return f, fmt.Errorf("invalid local port")
	}
	if e := ValidateTarget(f.Target); e != nil {
		return f, e
	}
	return f, nil
}

// ListenAddr returns bind:localport.
func (f ForwardSpec) ListenAddr() string { return net.JoinHostPort(f.Bind, strconv.Itoa(f.LocalPort)) }
