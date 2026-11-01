package job

import (
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

// TestHumanSubmitted pins the provenance judgement for every submission source.
func TestHumanSubmitted(t *testing.T) {
	t.Parallel()
	base := func(mut func(*jobstore.JobRecord)) jobstore.JobRecord {
		r := jobstore.JobRecord{ID: "j1", Channel: ChannelCLI, CallerID: "alice", OriginCaller: "alice"}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	cases := []struct {
		name string
		rec  jobstore.JobRecord
		want bool
	}{
		{"cli user", base(nil), true},
		{"web user", base(func(r *jobstore.JobRecord) { r.Channel = ChannelWeb }), true},
		{"mcp user", base(func(r *jobstore.JobRecord) { r.Channel = ChannelMCP }), true},
		{"no caller id (single-user setup)", base(func(r *jobstore.JobRecord) { r.CallerID = "" }), true},
		{"legacy empty channel", base(func(r *jobstore.JobRecord) { r.Channel = "" }), false},
		{"schedule", base(func(r *jobstore.JobRecord) { r.Channel = "cron" }), false},
		{"webhook", base(func(r *jobstore.JobRecord) { r.Channel = "webhook" }), false},
		{"plan dispatch", base(func(r *jobstore.JobRecord) { r.Channel = "plan" }), false},
		{"comment dispatch", base(func(r *jobstore.JobRecord) { r.Channel = "comment" }), false},
		{"steward", base(func(r *jobstore.JobRecord) { r.Channel = "steward" }), false},
		{"job credential (tag)", base(func(r *jobstore.JobRecord) { r.CallerID = "job-9"; r.TagsJSON = `["x","submitted_by_job:job-9"]` }), false},
		{"agent session (header tag)", base(func(r *jobstore.JobRecord) { r.TagsJSON = `["submitted_by_session:s-1"]` }), false},
		{"trusted source session", base(func(r *jobstore.JobRecord) { r.SourceSessionID = "s-1" }), false},
		{"notifies an agent session", base(func(r *jobstore.JobRecord) { r.RequestJSON = `{"notify":true,"notify_session":"s-1"}` }), false},
		{"automatic continuation", base(func(r *jobstore.JobRecord) { r.AutoResumeAttempt = 1 }), false},
		{"failover takeover", base(func(r *jobstore.JobRecord) { r.FellBackFrom = "j0" }), false},
		{"unrelated tags and request", base(func(r *jobstore.JobRecord) { r.TagsJSON = `["release"]`; r.RequestJSON = `{"prompt":"p"}` }), true},
	}
	for _, tc := range cases {
		if got := HumanSubmitted(tc.rec); got != tc.want {
			t.Errorf("%s: HumanSubmitted = %v, want %v", tc.name, got, tc.want)
		}
	}
}
