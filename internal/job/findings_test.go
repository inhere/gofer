package job

import (
	"reflect"
	"testing"
)

// The cases below are mirrored in web/src/utils/findings.test.ts.
func TestParseFindings(t *testing.T) {
	cases := []struct {
		name   string
		report string
		want   []string
	}{
		{"none", "# Report\n\nall done\n", nil},
		{"basic", "## 结果\nok\n\n## 发现但不碰\n\n- a.go:12：空指针\n- docs/x.md：过时\n", []string{"a.go:12：空指针", "docs/x.md：过时"}},
		{"stops at same level", "## 发现但不碰\n- one\n## 下一节\n- not this\n", []string{"one"}},
		{"keeps deeper headings", "## 发现但不碰\n- one\n### 细节\n- two\n# Top\n- no\n", []string{"one", "two"}},
		{"continuation lines", "### 发现但不碰\n- first line\n  second line\n* [ ] boxed\n1. numbered\n", []string{"first line second line", "boxed", "numbered"}},
		{"out of scope alias", "## Out of scope\n- x\n", []string{"x"}},
		{"out-of-scope findings alias", "#### Out-of-Scope Findings:\n+ y\n", []string{"y"}},
		{"decorated title", "## 「发现但不碰」：\n- z\n", []string{"z"}},
		{"last section wins", "## 发现但不碰\n- old\n## 发现但不碰\n- new\n", []string{"new"}},
		{"fenced heading ignored", "```\n## 发现但不碰\n- quoted\n```\nplain\n", nil},
		{"inline mention is not a heading", "写进汇报末尾的「## 发现但不碰」小节\n- not an item\n", nil},
		{"empty section", "## 发现但不碰\n\nnothing\n", nil},
	}
	for _, c := range cases {
		if got := ParseFindings(c.report); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: ParseFindings = %q, want %q", c.name, got, c.want)
		}
	}
}
