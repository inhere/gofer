package messenger

import (
	"testing"

	"github.com/inhere/gofer/internal/i18n"
)

// TestListAgentsPromptLanguages pins the zh-CN prompt to the constant it was before it
// moved into the catalog, checks the English one has no CJK and that the manager follows
// the language it was given.
func TestListAgentsPromptLanguages(t *testing.T) {
	if got, want := ListAgentsPromptIn(i18n.ZhCN), "调用 ListAgents 并原样输出结果。不要调用其他工具，不要添加解释。"; got != want {
		t.Errorf("zh-CN prompt = %q", got)
	}
	en := ListAgentsPromptIn(i18n.EN)
	for _, r := range en {
		if r >= 0x4e00 && r <= 0x9fff {
			t.Errorf("English prompt carries CJK: %q", en)
		}
	}
	m := New("claude", 0)
	if m.ListAgentsPrompt() != ListAgentsPromptIn(i18n.Default) {
		t.Errorf("an unset manager must use the default language")
	}
	m.SetLanguage(func() i18n.Lang { return i18n.EN })
	if m.ListAgentsPrompt() != en {
		t.Errorf("manager prompt = %q, want %q", m.ListAgentsPrompt(), en)
	}
	var nilM *Manager
	if nilM.ListAgentsPrompt() != ListAgentsPromptIn(i18n.Default) {
		t.Errorf("a nil manager must use the default language")
	}
}
