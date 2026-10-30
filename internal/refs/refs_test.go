package refs

import (
	"reflect"
	"testing"
)

func TestExtract(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []Reference
	}{
		{"all contexts and dedupe", "gofer-w9b2 [job-20261028-235959-0123abcd] plan-demo todo-1 wf-chain `gofer-w9b2` https://x/job-20261028-235959-0123abcd", []Reference{{KindIssue, "gofer-w9b2"}, {KindJob, "job-20261028-235959-0123abcd"}, {KindPlan, "plan-demo"}, {KindTodo, "todo-1"}, {KindWF, "wf-chain"}}},
		{"not partial job", "job-20261028-235959-short", []Reference{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Extract(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Extract() = %#v, want %#v", got, tc.want)
			}
		})
	}
}
