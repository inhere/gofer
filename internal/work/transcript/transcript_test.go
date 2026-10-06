package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	assert.NoErr(t, err)
	return b
}

func TestParseClaude(t *testing.T) {
	turns := Parse(DialectFor("claude"), load(t, "claude.jsonl"))
	var roles []string
	for _, tr := range turns {
		roles = append(roles, tr.Role)
	}
	// user, assistant text, tool notes merged, assistant, user (injected block dropped).
	assert.Eq(t, []string{"user", "assistant", "tool", "assistant", "user"}, roles)
	assert.Eq(t, "给订单页加导出按钮，导出 CSV", turns[0].Text)
	assert.True(t, strings.Contains(turns[2].Text, "[tool Read: /ws/shop/src/views/Orders.vue]"))
	assert.True(t, strings.Contains(turns[2].Text, "[tool Bash: npm run lint]"))
	assert.Eq(t, "好的，后端明天给；先把按钮样式调好", turns[4].Text)
	for _, tr := range turns {
		assert.False(t, strings.Contains(tr.Text, "system-reminder"))
	}
}

func TestParseCodexSkipsInjectedAndDuplicates(t *testing.T) {
	turns := Parse(DialectFor("codex-acp"), load(t, "codex.jsonl"))
	assert.Eq(t, 3, len(turns))
	assert.Eq(t, "把设备固件升级脚本跑一遍并记录结果", turns[0].Text)
	assert.Eq(t, RoleTool, turns[1].Role)
	assert.True(t, strings.Contains(turns[1].Text, "shell"))
	assert.True(t, strings.Contains(turns[2].Text, "需要现场插网线"))
}

func TestParseOmp(t *testing.T) {
	turns := Parse(DialectFor("omp"), load(t, "omp.jsonl"))
	assert.Eq(t, 4, len(turns))
	assert.Eq(t, RoleUser, turns[0].Role)
	assert.True(t, strings.Contains(turns[2].Text, "[tool read: README.md]"))
	assert.Eq(t, "安装章节已重写，剩下 Windows 部分待验证。", turns[3].Text)
}

func TestParseSniffsUnknownAgent(t *testing.T) {
	assert.Eq(t, "", DialectFor("jcode"))
	for _, name := range []string{"claude.jsonl", "codex.jsonl", "omp.jsonl"} {
		turns := Parse("", load(t, name))
		assert.True(t, len(turns) >= 3)
	}
	assert.Eq(t, 0, len(Parse("", []byte("not json\n{broken\n"))))
}

func TestReadTailDropsPartialFirstLine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.jsonl")
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString(`{"type":"user","message":{"role":"user","content":"line-` + strings.Repeat("x", 20) + `"}}` + "\n")
	}
	assert.NoErr(t, os.WriteFile(p, []byte(sb.String()), 0o600))
	raw, err := ReadTail(p, 500)
	assert.NoErr(t, err)
	assert.True(t, len(raw) <= 500)
	assert.True(t, strings.HasPrefix(string(raw), `{"type"`))
	assert.True(t, len(Parse(DialectClaude, raw)) >= 4)
	_, err = ReadTail(filepath.Join(t.TempDir(), "missing"), 10)
	assert.Err(t, err)
	_, err = ReadTail(t.TempDir(), 10)
	assert.Err(t, err)
}

func TestFormatKeepsNewestWithinBounds(t *testing.T) {
	var turns []Turn
	for i := 0; i < 100; i++ {
		turns = append(turns, Turn{Role: RoleUser, Text: strings.Repeat("问", 50)})
		turns = append(turns, Turn{Role: RoleAssistant, Text: strings.Repeat("答", 3000)})
	}
	turns = append(turns, Turn{Role: RoleAssistant, Text: "最后的结论"})
	out := Format(turns, FormatOpts{MaxTurns: 10, MaxBytes: 4000, MaxTurnRunes: 100})
	assert.True(t, len(out) <= 4000+200)
	assert.True(t, strings.HasSuffix(out, "最后的结论"))
	assert.True(t, strings.Contains(out, "中间省略"))
}
