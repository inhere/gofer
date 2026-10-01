package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInteractionCreatedIMMessage(t *testing.T) {
	body, err := RenderMessage(KindDingTalk, InteractionMessage(
		"interaction.created", "允许 agent 执行 shell 命令", []string{"允许", "拒绝"}, "https://gofer.example/jobs/j1", 1,
	))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Markdown map[string]string `json:"markdown"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	text := got.Markdown["text"]
	for _, want := range []string{"需要审批：允许 agent 执行 shell 命令", "允许", "拒绝", "https://gofer.example/jobs/j1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("message %q does not contain %q", text, want)
		}
	}
}
