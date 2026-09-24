package tunnel

import (
	"reflect"
	"strings"
	"testing"
)

// TestSpecsAcceptCommaAndSpace: TUN-04 — a spec argument may carry several
// comma-separated rules (the natural way operators write them), and space-separated
// arguments stay supported. Splitting trims whitespace, drops empty items and keeps
// the order the operator wrote, so `spec #N` in an error names a real position.
func TestSpecsAcceptCommaAndSpace(t *testing.T) {
	got, err := SplitSpecs([]string{
		"udp/21845:192.168.0.253:21845,1502:192.168.0.205:502",
		" 11217:127.0.0.1:1217 , 11740:192.168.0.205:11740",
	})
	if err != nil {
		t.Fatalf("SplitSpecs: %v", err)
	}
	want := []string{
		"udp/21845:192.168.0.253:21845",
		"1502:192.168.0.205:502",
		"11217:127.0.0.1:1217",
		"11740:192.168.0.205:11740",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SplitSpecs = %#v, want %#v", got, want)
	}

	// An IPv6 target is written [::1]:port and carries no comma of its own, so it must
	// survive the split as ONE entry.
	v6, err := SplitSpecs([]string{"9000:[::1]:9000,1502:10.0.0.5:502"})
	if err != nil {
		t.Fatalf("SplitSpecs(ipv6): %v", err)
	}
	if !reflect.DeepEqual(v6, []string{"9000:[::1]:9000", "1502:10.0.0.5:502"}) {
		t.Fatalf("an IPv6 target was split apart: %#v", v6)
	}

	// A trailing comma and a blank argument are noise, not an empty rule.
	sparse, err := SplitSpecs([]string{"", "  ", "1502:10.0.0.5:502,"})
	if err != nil {
		t.Fatalf("SplitSpecs(blanks): %v", err)
	}
	if !reflect.DeepEqual(sparse, []string{"1502:10.0.0.5:502"}) {
		t.Fatalf("blank items must be dropped, got %#v", sparse)
	}

	// Nothing usable at all is an error: "no rule" must not be silently accepted as an
	// empty, successful command.
	if _, err := SplitSpecs([]string{"", " , "}); err == nil {
		t.Fatal("a spec list with no usable entry must be an error")
	}
}

// TestSpecErrorNamesTheEntry: a malformed rule must be reported with its POSITION and
// its original text. A comma-joined list of four rules that answers only "invalid
// spec" leaves the operator bisecting the line by hand.
func TestSpecErrorNamesTheEntry(t *testing.T) {
	specs, err := SplitSpecs([]string{"1502:192.168.0.205:502,11740:192.168.0.205:11740,not-a-spec"})
	if err != nil {
		t.Fatalf("SplitSpecs: %v", err)
	}
	_, err = ParseSpecs(specs)
	if err == nil {
		t.Fatal("a malformed entry must fail the parse")
	}
	msg := err.Error()
	if !strings.Contains(msg, "spec #3") {
		t.Errorf("error must name the offending entry's position: %v", err)
	}
	if !strings.Contains(msg, `"not-a-spec"`) {
		t.Errorf("error must quote the offending entry's text: %v", err)
	}

	// The valid neighbours still parse, and a healthy list comes back in order.
	ok, err := ParseSpecs([]string{"1502:192.168.0.205:502", "udp/21845:192.168.0.253:21845"})
	if err != nil {
		t.Fatalf("ParseSpecs(valid): %v", err)
	}
	if len(ok) != 2 || ok[0].Network != "tcp" || ok[1].Network != "udp" || ok[1].LocalPort != 21845 {
		t.Fatalf("ParseSpecs(valid) = %#v", ok)
	}
}
