package tunnel

import (
	"strings"
	"testing"
)

func TestParseForwardSpecSOCKSAndSameAddr(t *testing.T) {
	cases := []struct {
		in   string
		want ForwardSpec
	}{
		{"socks/1080", ForwardSpec{Network: NetworkSOCKS, Bind: "127.0.0.1", LocalPort: 1080, Target: SOCKSTarget}},
		{"SOCKS/1080", ForwardSpec{Network: NetworkSOCKS, Bind: "127.0.0.1", LocalPort: 1080, Target: SOCKSTarget}},
		{"socks/127.0.0.2:1081", ForwardSpec{Network: NetworkSOCKS, Bind: "127.0.0.2", LocalPort: 1081, Target: SOCKSTarget}},
		{"socks/[::1]:1080", ForwardSpec{Network: NetworkSOCKS, Bind: "::1", LocalPort: 1080, Target: SOCKSTarget}},
		{"socks/localhost:1080", ForwardSpec{Network: NetworkSOCKS, Bind: "localhost", LocalPort: 1080, Target: SOCKSTarget}},
		{"socks/0", ForwardSpec{Network: NetworkSOCKS, Bind: "127.0.0.1", LocalPort: 0, Target: SOCKSTarget}},
		{"=10.20.1.5:502", ForwardSpec{Network: "tcp", Bind: "10.20.1.5", LocalPort: 502, Target: "10.20.1.5:502"}},
		{"udp/=10.20.1.5:1740", ForwardSpec{Network: "udp", Bind: "10.20.1.5", LocalPort: 1740, Target: "10.20.1.5:1740"}},
		{"=[fd00::5]:502", ForwardSpec{Network: "tcp", Bind: "fd00::5", LocalPort: 502, Target: "[fd00::5]:502"}},
	}
	for _, c := range cases {
		got, err := ParseForwardSpec(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("%q: got %+v want %+v", c.in, got, c.want)
		}
	}
}

func TestParseForwardSpecSOCKSAndSameAddrErrors(t *testing.T) {
	cases := map[string]string{
		"socks/0.0.0.0:1080":   "loopback",
		"socks/10.20.1.5:1080": "loopback",
		"socks/":               "invalid local port",
		"socks/70000":          "invalid local port",
		"socks/127.0.0.1:x":    "invalid local port",
		"=plc.local:502":       "must be an IP address",
		"=10.20.1.5":           "want =ip:port",
		"=10.20.1.5:0":         "invalid port",
		"=fd00::5:502":         "want =ip:port",
		"udp/=10.20.1.5:99999": "invalid port",
	}
	for in, want := range cases {
		_, err := ParseForwardSpec(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err=%v, want it to mention %q", in, err, want)
		}
	}
}

func TestSOCKSSpecsSplitAlongsideStaticRules(t *testing.T) {
	flat, err := SplitSpecs([]string{"socks/1080,=10.20.1.5:502, 1502:10.20.1.6:502"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseSpecs(flat)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !got[0].IsSOCKS() || got[1].Bind != "10.20.1.5" || got[2].Target != "10.20.1.6:502" {
		t.Fatalf("got %+v", got)
	}
}
