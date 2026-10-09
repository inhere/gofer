package steward

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// rolePrompt is the steward's standing instruction: who it is, what it may and may not do
// (the credential enforces the same limits — this only keeps it from trying), how to write.
const rolePrompt = `# 你是工作管家（gofer steward）

你负责替用户**调度和整理**工作项（用户在多个终端会话里同时推进很多事，经常做到一半缺设备、等资源、要去现场）。你的全部信息来自 gofer 里的工作项、日志、会话和请求账本；你自己的上下文随时会被丢弃，所以重要的东西必须落库（工作项日志 / 管家笔记）。

## 原则
- **只调度和整理，不干活、不拍板**：你不能把工作项设为 done / dropped（完成与放弃只有用户能定），不能提交执行类 job，不能改配置，不能删除，不能合并工作项（只能 gofer_work_merge_suggest 记建议，由用户确认）。
- **派活异步**：请会话汇报 / 写交接用 gofer_work_request_report（走请求账本），之后用 gofer_work_requests 查结果，**不要阻塞等待**；需要从会话记录提炼用 gofer_work_summarize。
- **改写而非转发**：不要把用户的原话甩给会话；你的发言会被标注为 steward(<agent>)，别伪装成用户。
- **用事实回答**：这份提示里的清单只是快照，回答前先用 gofer_work_list / gofer_work_get 核对最新数据；查不到就说查不到，不要编。
- 风格：中文，简洁；列清单时一项一行，带工作项 id。

## 你的工具（gofer MCP，凭据层面只放行这些）
- 读：gofer_work_list、gofer_work_get、gofer_work_requests、gofer_session_list、gofer_session_get、gofer_session_tail（只读会话记录尾部）、gofer_list_jobs、gofer_get_job、gofer_issue_list / gofer_issue_get（只读 issue 镜像，按 project / 状态 / 标签 / 关键字查）、gofer_today_list / gofer_today_card（「今天」待决策队列与单卡详情）
- 写：gofer_work_update（描述类字段；状态不含 done/dropped，人手动设的状态优先）、gofer_work_note（记一笔）、gofer_work_remind（设/清提醒）、gofer_work_merge_suggest（只记合并建议）、gofer_work_request_report、gofer_work_summarize、gofer_session_ask（带话：给**在线**会话捎一句话，如“资源到了可以继续”；会话离线会报错，此时记一笔 / 设提醒，不要重试）、gofer_steward_notes（读写管家笔记）、gofer_today_advise（给「今天」待决策卡写建议：一行理由 + 可选的建议动作 / 验收摘要；只是建议，由用户点「按建议」执行）

## 管家笔记
笔记是你的长期记忆（用户的偏好和约定，例如“某地现场一般周三去”）。发现新的长期有效信息就用 gofer_steward_notes 更新（set 要带 version，冲突就先 get 再合并）；笔记超过 8KB 时，巡检中重写成精简版（旧版本会保留）。`

// primeStats says how much of the picture the prime carries.
type primeStats struct {
	ItemsTotal   int
	ItemsShown   int
	RequestsShow int
	JournalShown int
	Truncated    bool
}

// Prime is the first message a steward session gets.
type Prime struct {
	Text  string
	Bytes int
	Stats primeStats
}

// priorityClass orders work items for the prime: waiting-on-me first, then due ones, then
// those needing on-site / resources, then the rest (design §14.4).
func priorityClass(it work.ItemView) int {
	switch {
	case it.Status == jobstore.WorkNeedsMe:
		return 0
	case it.Due:
		return 1
	case it.Status == jobstore.WorkNeedsOnsite || it.Status == jobstore.WorkWaitingResource:
		return 2
	}
	return 3
}

func sortForPrime(items []work.ItemView) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := priorityClass(items[i]), priorityClass(items[j])
		if a != b {
			return a < b
		}
		if items[i].LastActivityAt != items[j].LastActivityAt {
			return items[i].LastActivityAt > items[j].LastActivityAt
		}
		return items[i].ID < items[j].ID
	})
}

func runesCap(s string, n int) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "…"
}

func ago(now time.Time, at int64) string {
	if at <= 0 {
		return "无"
	}
	d := now.Sub(time.Unix(at, 0))
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d分钟前", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d小时前", int(d.Hours()))
	}
	return fmt.Sprintf("%d天前", int(d.Hours()/24))
}

func requestKindLabel(k string) string {
	switch k {
	case jobstore.WorkRequestReport:
		return "汇报请求"
	case jobstore.WorkRequestHandoff:
		return "交接请求"
	case jobstore.WorkRequestSummarize:
		return "整理"
	}
	return k
}

func requestStateLabel(st string) string {
	switch st {
	case jobstore.WorkRequestPending:
		return "待发送"
	case jobstore.WorkRequestSent:
		return "已发送待回复"
	case jobstore.WorkRequestAnswered:
		return "已回复"
	case jobstore.WorkRequestFailed:
		return "失败"
	case jobstore.WorkRequestExpired:
		return "未回应(已转整理)"
	}
	return st
}

// itemLine renders one work item on one line: id / status / title / blocker / next /
// last activity / in-flight requests.
func itemLine(it work.ItemView, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "- %s [%s] %s", it.ID, work.StatusLabel(it.Status), runesCap(it.Title, 60))
	if it.Unsorted {
		b.WriteString("（草稿）")
	}
	if t := runesCap(it.BlockerText, 60); t != "" {
		b.WriteString(" | 阻塞：" + t)
	}
	if t := runesCap(it.NextStep, 60); t != "" {
		b.WriteString(" | 下一步：" + t)
	}
	if it.Due {
		b.WriteString(" | 已到期")
	} else if it.RemindAt > 0 {
		b.WriteString(" | 提醒：" + time.Unix(it.RemindAt, 0).In(now.Location()).Format("01-02 15:04"))
	}
	fmt.Fprintf(&b, " | 最后活动：%s", ago(now, it.LastActivityAt))
	if it.SessionOffline {
		b.WriteString(" | 会话已离线")
	}
	var inflight []string
	for _, r := range it.Requests {
		if jobstore.WorkRequestActive(r.State) {
			inflight = append(inflight, fmt.Sprintf("%s(%s)", requestKindLabel(r.Kind), requestStateLabel(r.State)))
		}
	}
	if len(inflight) > 0 {
		b.WriteString(" | 在途：" + strings.Join(inflight, "、"))
	}
	return b.String()
}

// BuildPrime renders the steward's first message: role + notes + open work items (priority
// truncated) + the last 24h of journal + the request ledger, within primeMax bytes. The
// role and the notes are never cut (the notes are capped on their own); the items take up
// to 60% of what is left, the ledger and the journal share the rest.
func (s *Service) BuildPrime(now time.Time) (Prime, error) {
	items, err := s.work.List(work.WorkListOptsOpen())
	if err != nil {
		return Prime{}, err
	}
	sortForPrime(items)

	var out strings.Builder
	out.WriteString(rolePrompt)
	fmt.Fprintf(&out, "\n\n## 现在\n%s（%s）", now.Format("2006-01-02 15:04"), weekdayCN(now.Weekday()))

	// Notes.
	notesText, notesVer := "", 0
	if h, ok, nerr := s.store.GetPlanHandoff(jobstore.StewardNotesKey, 0); nerr == nil && ok {
		notesText, notesVer = h.Body, h.Version
	}
	out.WriteString("\n\n## 管家笔记")
	if notesVer > 0 {
		fmt.Fprintf(&out, "（v%d，%.1fKB）", notesVer, float64(len(notesText))/1024)
	}
	out.WriteString("\n")
	switch txt := strings.TrimSpace(notesText); {
	case txt == "":
		out.WriteString("（还没有笔记。有长期有效的偏好或约定时，用 gofer_steward_notes 记下来。）")
	case len(txt) > 12<<10:
		out.WriteString(txt[:12<<10] + "\n…（笔记过长，只列出前 12KB；用 gofer_steward_notes get 读全文并在巡检时精简）")
	default:
		out.WriteString(txt)
	}

	remaining := s.primeMax - out.Len() - 1024 // headings and the "另有 N" lines
	if remaining < 2048 {
		remaining = 2048 // the sections below always get a floor, even with silly settings
	}
	var st primeStats
	st.ItemsTotal = len(items)

	// Open items, priority order, one line each, up to 60% of the remaining budget.
	itemsBudget := remaining * 6 / 10
	out.WriteString("\n\n## 未结工作项（快照，按 等我 > 到期 > 需现场/等资源 > 其他 排序）\n")
	if len(items) == 0 {
		out.WriteString("（没有未结工作项）")
	}
	used := 0
	for _, it := range items {
		line := itemLine(it, now) + "\n"
		if used+len(line) > itemsBudget {
			break
		}
		out.WriteString(line)
		used += len(line)
		st.ItemsShown++
	}
	if hidden := st.ItemsTotal - st.ItemsShown; hidden > 0 {
		st.Truncated = true
		fmt.Fprintf(&out, "…另有 %d 项未列出（优先级较低或超出长度上限；用 gofer_work_list 查看）\n", hidden)
	}
	remaining -= used

	// Request ledger: the in-flight asks and what became of the recent ones.
	ledgerBudget := min(remaining/2, 3<<10)
	out.WriteString("\n## 在途请求账本（你发出的和系统发出的汇报 / 交接 / 整理请求）\n")
	reqs, _ := s.store.ListWorkRequests("", false, 100)
	cutoff := now.Add(-24 * time.Hour).Unix()
	usedL, shownL, hiddenL := 0, 0, 0
	for _, r := range reqs {
		if !jobstore.WorkRequestActive(r.State) && r.CreatedAt < cutoff {
			continue
		}
		line := fmt.Sprintf("- %s %s %s | 工作项 %s | 会话 %s | 发起：%s | %s\n", r.ID, requestKindLabel(r.Kind),
			requestStateLabel(r.State), r.WorkItemID, orDash(r.SessionID), orDash(r.By), ago(now, r.CreatedAt))
		if usedL+len(line) > ledgerBudget {
			hiddenL++
			continue
		}
		out.WriteString(line)
		usedL += len(line)
		shownL++
	}
	if shownL == 0 && hiddenL == 0 {
		out.WriteString("（没有在途请求）\n")
	}
	if hiddenL > 0 {
		st.Truncated = true
		fmt.Fprintf(&out, "…另有 %d 条未列出（用 gofer_work_requests 查看）\n", hiddenL)
	}
	st.RequestsShow = shownL
	remaining -= usedL

	// Last 24h of journal, newest first, in whatever is left.
	out.WriteString("\n## 最近 24 小时的工作项日志（新的在前）\n")
	journalBudget := max(remaining, 1024)
	entries, _ := s.store.ListRecentWorkJournal(cutoff, 300)
	usedJ, hiddenJ := 0, 0
	for _, e := range entries {
		line := fmt.Sprintf("- %s %s [%s] %s：%s\n", time.Unix(e.At, 0).In(now.Location()).Format("15:04"), e.WorkItemID,
			journalKindLabel(e.Kind), runesCap(e.By, 40), runesCap(e.Text, 90))
		if usedJ+len(line) > journalBudget {
			hiddenJ++
			continue
		}
		out.WriteString(line)
		usedJ += len(line)
		st.JournalShown++
	}
	if len(entries) == 0 {
		out.WriteString("（最近 24 小时没有日志）\n")
	}
	if hiddenJ > 0 {
		st.Truncated = true
		fmt.Fprintf(&out, "…另有 %d 条更早的未列出（用 gofer_work_get 看单项日志）\n", hiddenJ)
	}

	text := strings.TrimRight(out.String(), "\n")
	return Prime{Text: text, Bytes: len(text), Stats: st}, nil
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func journalKindLabel(k string) string {
	switch k {
	case jobstore.WorkJournalReport:
		return "汇报"
	case jobstore.WorkJournalNote:
		return "备注"
	case jobstore.WorkJournalStatus:
		return "状态"
	case jobstore.WorkJournalSteward:
		return "整理"
	case jobstore.WorkJournalLink:
		return "关联"
	}
	return k
}

func weekdayCN(d time.Weekday) string {
	return [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}[d]
}
