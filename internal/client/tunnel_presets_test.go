package client

import "testing"

func TestTunnelSpecViewDisplay(t *testing.T) {
	cases := []struct {
		in   TunnelSpecView
		want string
	}{
		{TunnelSpecView{Network: "tcp", Bind: "127.0.0.1", LocalPort: 1502, Target: "10.0.0.5:502"}, "1502 -> 10.0.0.5:502"},
		{TunnelSpecView{Network: "udp", Bind: "127.0.0.1", LocalPort: 1740, Target: "10.0.0.5:1740"}, "udp/1740 -> 10.0.0.5:1740"},
		{TunnelSpecView{Network: "tcp", Bind: "10.0.0.5", LocalPort: 502, Target: "10.0.0.5:502"}, "10.0.0.5:502 -> 10.0.0.5:502"},
		{TunnelSpecView{Network: "socks", Bind: "127.0.0.1", LocalPort: 1080, Target: "*"}, "socks/1080 -> *"},
	}
	for _, c := range cases {
		if got := c.in.Display(); got != c.want {
			t.Errorf("%+v: got %q want %q", c.in, got, c.want)
		}
	}
}
