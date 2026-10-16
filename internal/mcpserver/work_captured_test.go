package mcpserver

import (
	"context"
	"strings"
	"testing"
)

// TestWorkToolDescriptionsDoNotOfferCaptured: captured is the capture box's own status
// and is never a value a report / update / steward tool may set, so the status enums in
// tool descriptions must not list it.
func TestWorkToolDescriptionsDoNotOfferCaptured(t *testing.T) {
	session, _ := connect(t)
	res, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	checked := 0
	for _, tl := range res.Tools {
		if !strings.HasPrefix(tl.Name, "gofer_work_") {
			continue
		}
		checked++
		if strings.Contains(tl.Description, "captured") {
			t.Errorf("%s description lists captured: %s", tl.Name, tl.Description)
		}
	}
	if checked == 0 {
		t.Fatal("no gofer_work_* tools found")
	}
}
