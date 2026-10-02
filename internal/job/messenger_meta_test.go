package job

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessengerMetaPersistsTargetMessageAndChannel(t *testing.T) {
	raw, err := json.Marshal(JobRequest{MessengerMeta: &MessengerMeta{
		TargetSession: "claude-main", Message: "请继续检查", Channel: "resident",
	}})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"target_session":"claude-main"`, `"message":"请继续检查"`, `"channel":"resident"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("request json %q missing %s", text, want)
		}
	}
}
