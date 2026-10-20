package steward

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gookit/goutil/x/assert"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
	"github.com/inhere/gofer/internal/work"
)

// promptEnv is the standard env with the extension settings wired over the test store.
func promptEnv(t *testing.T) (*env, *settings.Service) {
	t.Helper()
	e := newEnv(t)
	set := settings.NewService(e.st)
	e.svc.SetSettings(set)
	return e, set
}

// errSettings is a settings service that cannot read: the prompts must fall back to the
// code defaults instead of failing a prime / review.
type errSettings struct{}

func (errSettings) GetString(string) (string, error) { return "", errors.New("db is gone") }

func primeText(t *testing.T, e *env, now time.Time) string {
	t.Helper()
	p, err := e.svc.BuildPrime(now)
	assert.NoErr(t, err)
	return p.Text
}

func TestStewardRolePromptDefault(t *testing.T) {
	e, _ := promptEnv(t)
	text := primeText(t, e, time.Now())
	assert.True(t, strings.HasPrefix(text, rolePrompt))
	// The rewritten role tells the person where to put their own preferences (K4).
	assert.True(t, strings.Contains(text, "使用者可以在「设置 → 扩展设置」里补充偏好"))
	assert.False(t, strings.Contains(text, settings.ExtraHeading))
}

func TestStewardRolePromptReplacementAndExtra(t *testing.T) {
	e, set := promptEnv(t)
	_, _, err := set.Set(settings.KeyPromptStewardRole, "# 你是我的调度助手\n只做整理，不干活。", "human:me")
	assert.NoErr(t, err)
	_, _, err = set.Set(settings.KeyPromptStewardRoleExtra, "偏好：列表按「等我」在前。", "human:me")
	assert.NoErr(t, err)

	text := primeText(t, e, time.Now())
	assert.True(t, strings.HasPrefix(text, "# 你是我的调度助手\n只做整理，不干活。\n\n"+settings.ExtraHeading+"\n偏好：列表按「等我」在前。"))
	assert.False(t, strings.Contains(text, "# 你是工作管家"))
	// The sections that follow the role are still the code's.
	assert.True(t, strings.Index(text, settings.ExtraHeading) < strings.Index(text, "## 现在"))
	assert.True(t, strings.Contains(text, "## 未结工作项"))
}

// Extra without a replacement follows the default role (the recommended way to tune it).
func TestStewardRolePromptExtraOnly(t *testing.T) {
	e, set := promptEnv(t)
	_, _, err := set.Set(settings.KeyPromptStewardRoleExtra, "偏好：现场的事周三处理。", "human:me")
	assert.NoErr(t, err)

	text := primeText(t, e, time.Now())
	assert.True(t, strings.HasPrefix(text, rolePrompt))
	assert.True(t, strings.Contains(text, rolePrompt+"\n\n"+settings.ExtraHeading+"\n偏好：现场的事周三处理。"))
}

func TestReviewPromptDefaultHasTheNoiseRule(t *testing.T) {
	e, _ := promptEnv(t)
	p := e.svc.reviewPrompt(TriggerDaily, "2026-10-06", nil, 0, nil, false, time.Now(), -1, nil)
	// The rewritten default (2026-10-20 决策 7): no advice for try-out work, and a
	// suggestion names a next action.
	for _, want := range []string{
		"测试、试用、演示性质的会话和工作项不给建议",
		"优先处理真的卡住、在等人、已经到期的事项",
		"建议要具体到下一步动作",
	} {
		assert.True(t, strings.Contains(p, want), want)
	}
	assert.True(t, strings.Contains(p, "最后调用 gofer_steward_notes"))
	assert.False(t, strings.Contains(p, settings.ExtraHeading))
}

func TestStewardReviewPromptReplacementForEveryTrigger(t *testing.T) {
	e, set := promptEnv(t)
	_, _, err := set.Set(settings.KeyPromptStewardReview, "自己写的巡检指令。", "human:me")
	assert.NoErr(t, err)
	_, _, err = set.Set(settings.KeyPromptStewardReviewExtra, "补充：只看卡住的项。", "human:me")
	assert.NoErr(t, err)

	now := time.Now()
	e.item(t, "等设备")
	items, err := e.work.List(work.WorkListOptsOpen())
	assert.NoErr(t, err)
	events := []jobstore.StewardEvent{{Kind: "session", Detail: "会话已离线"}}

	for _, trigger := range []string{TriggerDaily, TriggerManual, TriggerEvent} {
		p := e.svc.reviewPrompt(trigger, "2026-10-06", items, 1, events, false, now, -1, nil)
		assert.True(t, strings.Contains(p, "自己写的巡检指令。"), trigger)
		assert.False(t, strings.Contains(p, "你要做的（只整理"), trigger)
		// The dynamic material (picked items, events) is still appended by the code, before
		// the replaceable instruction.
		assert.True(t, strings.Index(p, "等设备") < strings.Index(p, "自己写的巡检指令。"), trigger)
		assert.True(t, strings.Contains(p, "会话已离线"), trigger)
		assert.True(t, strings.Contains(p, "最后调用 gofer_steward_notes"), trigger)
		assert.True(t, strings.HasSuffix(p, settings.ExtraHeading+"\n补充：只看卡住的项。"), trigger)
	}
}

// A replacement that numbers its own steps keeps the code-owned sections in sequence.
func TestReviewInstructionContinuesTheStepNumbers(t *testing.T) {
	e, set := promptEnv(t)
	_, _, err := set.Set(settings.KeyPromptStewardReview, "1. 看工作项。\n2. 记笔记。", "human:me")
	assert.NoErr(t, err)

	p := e.svc.reviewPrompt(TriggerDaily, "2026-10-06", nil, 0, nil, false, time.Now(), 2, nil)
	assert.True(t, strings.Contains(p, "3. 「今天」待决策队列里有 2 张卡"))
	assert.True(t, strings.Contains(p, "4. 最后调用 gofer_steward_notes"))
}

// A settings read failure falls back to the code defaults on both prompts.
func TestStewardPromptSettingsFailureFallsBack(t *testing.T) {
	e := newEnv(t)
	e.svc.SetSettings(errSettings{})

	text := primeText(t, e, time.Now())
	assert.True(t, strings.HasPrefix(text, rolePrompt))
	assert.False(t, strings.Contains(text, settings.ExtraHeading))

	p := e.svc.reviewPrompt(TriggerDaily, "2026-10-06", nil, 0, nil, false, time.Now(), -1, nil)
	assert.True(t, strings.Contains(p, "你要做的（只整理，不替用户决定）"))
	assert.False(t, strings.Contains(p, settings.ExtraHeading))
}
