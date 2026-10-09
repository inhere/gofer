package steward

import (
	"fmt"
	"strings"
)

// N3 T4 管家建议: the steward advises on the 「今天」 decision queue during a review. The
// queue lives in internal/today (which reads the steward's status, so the steward cannot
// import it): the server wires a counter of the cards that still have no advice.

// SetTodayUnadvised supplies the keys of the 「今天」 cards without advice. A review with
// no changed work item still runs when one of them was never put before the steward
// (a card it already saw and chose to skip does not wake it again); nil (or an error)
// leaves the advice step in every review and never forces one.
func (s *Service) SetTodayUnadvised(fn func() ([]string, error)) { s.todayUnadvised = fn }

// kvTodayPresented holds the unadvised card keys the last review put before the steward.
const kvTodayPresented = "steward.today_presented"

// todayPending returns the unadvised cards (-1 when unknown), whether any of them is
// new to the steward, and the keys to remember once a review runs.
func (s *Service) todayPending() (pending int, fresh bool, keys []string) {
	if s.todayUnadvised == nil {
		return -1, false, nil
	}
	keys, err := s.todayUnadvised()
	if err != nil {
		return -1, false, nil
	}
	seen := map[string]bool{}
	for _, k := range strings.Split(s.kv(kvTodayPresented), "\n") {
		seen[k] = true
	}
	for _, k := range keys {
		if !seen[k] {
			fresh = true
			break
		}
	}
	return len(keys), fresh, keys
}

// rememberTodayPresented records the cards a review has put before the steward.
func (s *Service) rememberTodayPresented(keys []string) {
	if keys != nil {
		s.setKV(kvTodayPresented, strings.Join(keys, "\n"))
	}
}

// todaySection is the review step that asks for advice; empty when nothing waits.
func todaySection(step, pending int) string {
	if pending == 0 {
		return ""
	}
	var b strings.Builder
	if pending > 0 {
		fmt.Fprintf(&b, "%d. 「今天」待决策队列里有 %d 张卡还没有建议：", step, pending)
	} else {
		fmt.Fprintf(&b, "%d. 「今天」待决策队列：", step)
	}
	b.WriteString("用 gofer_today_list（unadvised=true）读卡，逐张用 gofer_today_advise 写建议（text 一行 ≤60 字，写理由）。\n")
	b.WriteString("   - review（待验收）：用 gofer_today_card 读汇报、diff 统计、提交与 verify，digest 写 3–5 行（改动分组 / 风险 / 测试），action_id 给 accept（通过）或 rerun（退回重跑）；\n")
	b.WriteString("   - suggestion / merge：给 adopt（采纳）或 dismiss（忽略）；interaction：只在选项明确时给 answer:<value>，不确定就只写 text；\n")
	b.WriteString("   - decision：只补背景，**不给 action_id、不替人选**。建议只是建议：你不执行任何动作，由用户点「按建议」。\n")
	return b.String()
}
