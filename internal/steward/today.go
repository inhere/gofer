package steward

import (
	"fmt"
	"strings"
)

// N3 T4 管家建议: the steward advises on the 「今天」 decision queue during a review. The
// queue lives in internal/today (which reads the steward's status, so the steward cannot
// import it): the server wires a counter of the cards that still have no advice.

// SetTodayUnadvised supplies the count of 「今天」 cards without advice. A review with no
// changed work item still runs when the count is positive; nil (or an error) leaves the
// advice step in every review and never forces one.
func (s *Service) SetTodayUnadvised(fn func() (int, error)) { s.todayUnadvised = fn }

// todayPending is the count of unadvised cards, -1 when unknown.
func (s *Service) todayPending() int {
	if s.todayUnadvised == nil {
		return -1
	}
	n, err := s.todayUnadvised()
	if err != nil {
		return -1
	}
	return n
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
