package tunnel

import "testing"

func TestParseForwardSpec(t *testing.T) {
	tests := []struct {
		name, in, bind, target string
		port                   int
		wantErr                bool
	}{
		{"default", "1502:192.168.1.10:502", "127.0.0.1", "192.168.1.10:502", 1502, false},
		{"bind", "0.0.0.0:1502:10.0.0.1:502", "0.0.0.0", "10.0.0.1:502", 1502, false},
		{"zero", "0:h:502", "127.0.0.1", "h:502", 0, false},
		{"hostname", "1502:plc.local:502", "127.0.0.1", "plc.local:502", 1502, false},
		{"ipv6target", "1502:[fe80::1]:502", "127.0.0.1", "[fe80::1]:502", 1502, false},
		{"ipv6bind", "[::1]:1502:[fe80::1]:502", "::1", "[fe80::1]:502", 1502, false},
		{"explicit", "127.0.0.1:11217:127.0.0.1:1217", "127.0.0.1", "127.0.0.1:1217", 11217, false},
		{"badlocal", "abc:h:502", "", "", 0, true}, {"highlocal", "70000:h:502", "", "", 0, true},
		{"zeroTarget", "1502:h:0", "", "", 0, true}, {"highTarget", "1502:h:65536", "", "", 0, true},
		{"nonNumeric", "1502:h:http", "", "", 0, true}, {"short", "h:502", "", "", 0, true},
		{"emptyhost", "1502::502", "", "", 0, true}, {"empty", "", "", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseForwardSpec(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Bind != tt.bind || got.Target != tt.target || got.LocalPort != tt.port {
				t.Fatalf("got %+v", got)
			}
			if tt.name == "ipv6bind" && got.ListenAddr() != "[::1]:1502" {
				t.Fatalf("listen %q", got.ListenAddr())
			}
		})
	}
}
