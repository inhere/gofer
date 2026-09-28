package tracker

import (
	"reflect"
	"strings"
	"testing"
)

func TestPrimeIncludesPlanHandoff(t *testing.T) {
	root := t.TempDir()
	s, _, err := Init(root, "prime-handoff", true)
	if err != nil {
		t.Fatalf("init tracker: %v", err)
	}

	// Keep the test source-compatible before the feature exists while making the
	// missing behavior an ordinary assertion failure rather than a compile error.
	method := reflect.ValueOf(s).MethodByName("PrimeWithHandoffSection")
	if !method.IsValid() {
		t.Fatalf("PrimeWithHandoffSection is not implemented")
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(strings.Repeat("handoff\n", 3))})
	if len(results) != 2 {
		t.Fatalf("PrimeWithHandoffSection returned %d values, want body and error", len(results))
	}
	if errValue := results[1].Interface(); errValue != nil {
		t.Fatalf("prime with handoff: %v", errValue)
	}
	body, ok := results[0].Interface().(string)
	if !ok || !strings.Contains(body, "进行中 plan 的交接说明") {
		t.Fatalf("prime output missing handoff heading: %q", body)
	}
	if len([]byte(body)) > PrimeMaxBytes {
		t.Fatalf("prime output is %d bytes, exceeds %d", len([]byte(body)), PrimeMaxBytes)
	}
}
