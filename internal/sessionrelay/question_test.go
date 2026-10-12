package sessionrelay

import (
	"errors"
	"strings"
	"testing"
)

func TestParseQuestionAnswer(t *testing.T) {
	qs := []Question{{Question: "颜色？", Options: []QuestionOption{{Label: "红"}, {Label: "蓝"}}}, {Question: "名字？"}}
	got, err := ParseQuestionAnswer(`answers:{"颜色？":" 蓝 ","名字？":"gofer","多余":"x"}`, qs)
	if err != nil {
		t.Fatal(err)
	}
	if got != `answers:{"名字？":"gofer","颜色？":"蓝"}` {
		t.Fatalf("canonical = %s", got)
	}
	for _, bad := range []string{`allow`, `answers:{"颜色？":"蓝"}`, `answers:not-json`, `answers:{"颜色？":"","名字？":"a"}`} {
		if _, err := ParseQuestionAnswer(bad, qs); err == nil || !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: err=%v", bad, err)
		}
	}
	if !strings.HasPrefix(QuestionMessage("颜色？"), "等你回答：") {
		t.Fatal("question message")
	}
}
