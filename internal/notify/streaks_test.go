package notify

import (
	"errors"
	"testing"
)

// TestChannelStreaksAccumulateAndClear pins the notification self-check's input: failures
// extend one channel's streak, each channel counts on its own, and ONE success clears that
// channel so a recovered channel stops being reported.
func TestChannelStreaksAccumulateAndClear(t *testing.T) {
	streaks := NewChannelStreaks()
	boom := errors.New("refused")

	streaks.Record("dingtalk:oapi.dingtalk.com", boom)
	streaks.Record("dingtalk:oapi.dingtalk.com", boom)
	streaks.Record("generic:hooks.example", nil)
	if got := streaks.Fails(); got["dingtalk:oapi.dingtalk.com"] != 2 || len(got) != 1 {
		t.Fatalf("after 2 failures = %v, want only the failing channel at 2", got)
	}

	streaks.Record("dingtalk:oapi.dingtalk.com", nil)
	if got := streaks.Fails(); got != nil {
		t.Fatalf("a success must clear the channel, got %v", got)
	}
}

// TestChannelStreaksNilAndEmptySafe keeps the "nothing wired" case free of call-site
// guards: a nil tracker records nothing and reports nothing.
func TestChannelStreaksNilAndEmptySafe(t *testing.T) {
	var nilStreaks *ChannelStreaks
	nilStreaks.Record("x", errors.New("boom"))
	if got := nilStreaks.Fails(); got != nil {
		t.Fatalf("nil tracker Fails = %v, want nil", got)
	}
	streaks := NewChannelStreaks()
	streaks.Record("", errors.New("boom"))
	if got := streaks.Fails(); got != nil {
		t.Fatalf("an empty channel name must not be counted, got %v", got)
	}
}

// TestChannelName pins the label the health report shows: kind plus host (the config
// names a webhook by URL only, and its path carries the bot token), kind alone otherwise.
func TestChannelName(t *testing.T) {
	cases := map[string]struct {
		kind, target, want string
	}{
		"url":          {"dingtalk", "https://oapi.dingtalk.com/robot/send?access_token=secret", "dingtalk:oapi.dingtalk.com"},
		"kind missing": {"", "https://hooks.example/x", "generic:hooks.example"},
		"not a url":    {"feishu", "not a url", "feishu"},
		"uppercase":    {"DingTalk", "https://oapi.dingtalk.com/robot/send", "dingtalk:oapi.dingtalk.com"},
	}
	for name, tc := range cases {
		if got := ChannelName(tc.kind, tc.target); got != tc.want {
			t.Errorf("%s: ChannelName = %q, want %q", name, got, tc.want)
		}
	}
}
