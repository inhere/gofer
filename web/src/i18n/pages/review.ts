// review 页的文案（UI 重设计 P4）：/review 待验收列表与 job 详情里的验收面板（ReviewPanel）。
// 每个页面一个文件、zh 与 en 并列；key 前缀 `review.`，命名规范见设计稿 §9.2。
// 业务内容（任务标题、汇报正文、提交标题）不翻译。
import type { PageDict } from './types'

export const review: PageDict = {
  zh: {
    // 列表头
    'review.h.title': '待验收',
    'review.h.summary': '{n} 个待验收任务 · 等得最久的排最前',
    'review.h.summaryFiltered': '{n}/{total} 个待验收任务 · 等得最久的排最前',
    'review.h.refreshing': ' · 刷新中',
    'review.h.refresh': '刷新',
    'review.h.approvals': '待批准 {n} →',
    // 列
    'review.col.job': '任务 · 标题 / ID',
    'review.col.project': '项目',
    'review.col.agent': 'Agent',
    'review.col.plan': '计划 · 待办',
    'review.col.verify': '验收',
    'review.col.commits': '提交',
    'review.col.usage': '用量',
    'review.col.wait': '等待',
    'review.col.actions': '裁决',
    // 筛选
    'review.filter.verify': '验证结果',
    'review.filter.all': '全部',
    'review.filter.passed': '通过',
    'review.filter.failed': '失败',
    'review.filter.none': '未验证',
    // 行内裁决
    'review.act.accept': '通过',
    'review.act.reject': '拒绝',
    'review.act.busy': '…',
    'review.toast.acceptFailed': '验收通过失败（{id}）：{msg}',
    'review.toast.rejected': '已拒绝',
    'review.toast.resumed': '已拒绝，并已续投新 job',
    // 空态
    'review.empty.none': '现在没有待验收的任务。',
    'review.empty.filtered': '当前筛选下没有待验收的任务。',
    'review.empty.howTo': '要让一个任务停下来等人验收，两种方式：',
    'review.empty.cmdLabel': '命令：',
    'review.empty.cmdHint': '（本次提交要求人验收）',
    'review.empty.projectPrefix': '项目配置里',
    'review.empty.projectHint': '（该项目所有 job 都要人验收）',
    'review.empty.cli': '容器里也可以直接看：',

    // ---- 验收面板（ReviewPanel）----
    'review.panel.title': '验收',
    'review.panel.tabs': '验收页签',
    'review.panel.hintDecide': 'agent 已停，交付物等人定论：通过即 done；拒绝必须写明理由。',
    'review.panel.hintBlocked': '你没有裁决权，下面是验收材料。',
    'review.panel.hintReviewed': '已裁决，下面是当时的验收材料。',
    'review.panel.acceptance': '验收标准',
    'review.panel.acceptanceHint': '勾选仅本页有效，用来对照汇报逐条核对',
    // 页签
    'review.tab.report': '汇报',
    'review.tab.findings': '发现',
    'review.tab.knowledge': '经验',
    'review.tab.commits': '提交',
    'review.tab.diff': 'Diff',
    'review.tab.verify': '验证',
    'review.tab.usage': '用量',
    // 审阅提示
    'review.hints.summary': '审阅提示 ({n})：删除热点 / 被删注释 / 验证结果行',
    'review.hints.hotspots': 'deletion hotspots (删除最多)',
    'review.hints.removedComments': 'removed comments (被删注释)',
    'review.hints.verifyLines': 'verify lines (汇报里的验证结果)',
    // 汇报
    'review.note.loading': '加载中…',
    'review.act.retry': '重试',
    'review.note.noReport': 'agent 无文本输出',
    // 发现
    'review.findings.hint':
      'agent 在范围外发现、按约定没有动手的问题。「建 issue」直接建到该仓库的 tracker（下次 repo sync 进仓库）；也可复制命令到仓库里执行。',
    'review.findings.filed': '已建 {id}',
    'review.findings.file': '建 issue',
    'review.findings.filing': '创建中…',
    'review.findings.copy': '复制为 issue 命令',
    'review.findings.copied': '已复制',
    // 提交
    'review.commits.baseline': '基线 {base} → HEAD {head}',
    'review.commits.empty': '无提交（可能只改了工作树，见 Diff）',
    'review.commits.copyTitle': '复制 {sha}',
    'review.commits.copied': '已复制',
    // Diff
    'review.diff.noDiffHint': '此 job 没有捕获到 diff（无改动、非 git 仓库，或改动全在提交里）',
    'review.diff.scope': '声明范围：{paths}',
    'review.diff.noOutside': ' · 无越界文件',
    'review.diff.outside': '范围外 {n} 个文件（仅提示，不阻塞通过）：{paths}',
    // 验证
    'review.verify.none': '本 job 没有验证步骤（提交时未指定 verify）',
    'review.verify.reportHint': '以下来自 agent 汇报的「验证」小节（{source}），仅作参考，不是 gofer 跑的 verify 步骤。',
    'review.verify.reportSource': '来源：agent 自述',
    'review.verify.reportHeading': '「{heading}」 · 来源 {source}',
    'review.verify.fromAgent': 'agent 自述',
    'review.verify.exit': 'exit {code} · {dur}s',
    'review.verify.skipped': '未跑验证：{reason}',
    'review.verify.defaultReason': 'agent 没正常结束',
    'review.verify.noBanner': 'stderr 尾部 64KB 里没有 verify 横幅（输出可能已被更大的日志挤出窗口）',
    // 用量
    'review.usage.none': 'agent 未上报用量',
    // 底部裁决条
    'review.act.acceptPlain': 'Accept 通过',
    'review.act.acceptDeliver': 'Accept 通过并{label}',
    'review.act.skipDelivery': '本次不交付',
    'review.act.rejectOpen': 'Reject 拒绝…',
    'review.act.processing': '处理中…',
    'review.reviewed.line': '已验收 · {by} · {at}',
    'review.reviewed.note': '备注：{note}',
    'review.reviewed.noRecord': '该 job 已终态，但没有验收记录（reviewed_by 为空）',

    // 验收面板「经验」页签（components/MemoryCandidatesTab.vue）
    'review.memory.note': 'agent 汇报的跨任务经验；接受后写成本 job 项目的记忆（kind 默认 note），拒绝不留痕。不会自动入库。',
    'review.memory.needKey': '先填 key',
    'review.memory.saving': '处理中…',
    'review.memory.accept': '接受',
    'review.memory.reject': '拒绝',
    'review.memory.accepted': '已接受 → {key}',
    'review.memory.rejected': '已拒绝',

    // 验收面板的参考输出截断行（components/ReviewPanel.vue）
    'review.verify.omitted': '…（前面 {kept} 行已省略，本段共 {total} 行）',
  },
  en: {
    // 列表头
    'review.h.title': 'Review',
    'review.h.summary': '{n} waiting for review · longest wait first',
    'review.h.summaryFiltered': '{n}/{total} waiting for review · longest wait first',
    'review.h.refreshing': ' · refreshing',
    'review.h.refresh': 'Refresh',
    'review.h.approvals': '{n} awaiting approval →',
    // 列
    'review.col.job': 'Job · title / ID',
    'review.col.project': 'Project',
    'review.col.agent': 'Agent',
    'review.col.plan': 'Plan · todo',
    'review.col.verify': 'Verify',
    'review.col.commits': 'Commits',
    'review.col.usage': 'Usage',
    'review.col.wait': 'Waiting',
    'review.col.actions': 'Decide',
    // 筛选
    'review.filter.verify': 'Verify result',
    'review.filter.all': 'All',
    'review.filter.passed': 'Passed',
    'review.filter.failed': 'Failed',
    'review.filter.none': 'Not verified',
    // 行内裁决
    'review.act.accept': 'Accept',
    'review.act.reject': 'Reject',
    'review.act.busy': '…',
    'review.toast.acceptFailed': 'Accept failed ({id}): {msg}',
    'review.toast.rejected': 'Rejected',
    'review.toast.resumed': 'Rejected, and a follow-up job was queued',
    // 空态
    'review.empty.none': 'Nothing is waiting for review.',
    'review.empty.filtered': 'Nothing is waiting for review under the current filters.',
    'review.empty.howTo': 'Two ways to make a job stop and wait for a human:',
    'review.empty.cmdLabel': 'Command: ',
    'review.empty.cmdHint': ' (this run asks for a human review)',
    'review.empty.projectPrefix': 'In the project config,',
    'review.empty.projectHint': ' (every job in that project needs a human review)',
    'review.empty.cli': 'Inside the container you can also run:',

    // ---- 验收面板（ReviewPanel）----
    'review.panel.title': 'Review',
    'review.panel.tabs': 'Review tabs',
    'review.panel.hintDecide': 'The agent stopped; the deliverable needs a verdict. Accept marks it done; reject needs a reason.',
    'review.panel.hintBlocked': 'You cannot decide here; the materials are below.',
    'review.panel.hintReviewed': 'Already decided; these are the materials from then.',
    'review.panel.acceptance': 'Acceptance criteria',
    'review.panel.acceptanceHint': 'Ticks are local to this view, for checking the report item by item',
    // 页签
    'review.tab.report': 'Report',
    'review.tab.findings': 'Findings',
    'review.tab.knowledge': 'Lessons',
    'review.tab.commits': 'Commits',
    'review.tab.diff': 'Diff',
    'review.tab.verify': 'Verify',
    'review.tab.usage': 'Usage',
    // 审阅提示
    'review.hints.summary': 'Review hints ({n}): deletion hotspots / removed comments / verify lines',
    'review.hints.hotspots': 'deletion hotspots',
    'review.hints.removedComments': 'removed comments',
    'review.hints.verifyLines': 'verify lines (from the report)',
    // 汇报
    'review.note.loading': 'Loading…',
    'review.act.retry': 'Retry',
    'review.note.noReport': 'The agent produced no text output',
    // 发现
    'review.findings.hint':
      'Problems the agent found outside the scope and left untouched. "File issue" creates it in that repo\'s tracker (synced on the next repo sync); you can also copy the command and run it in the repo.',
    'review.findings.filed': 'Filed {id}',
    'review.findings.file': 'File issue',
    'review.findings.filing': 'Filing…',
    'review.findings.copy': 'Copy as issue command',
    'review.findings.copied': 'Copied',
    // 提交
    'review.commits.baseline': 'Base {base} → HEAD {head}',
    'review.commits.empty': 'No commits (maybe only the worktree changed; see Diff)',
    'review.commits.copyTitle': 'Copy {sha}',
    'review.commits.copied': 'Copied',
    // Diff
    'review.diff.noDiffHint': 'No diff was captured for this job (no changes, not a git repo, or everything is in commits)',
    'review.diff.scope': 'Declared scope: {paths}',
    'review.diff.noOutside': ' · no files outside scope',
    'review.diff.outside': '{n} files outside scope (advisory only, does not block accept): {paths}',
    // 验证
    'review.verify.none': 'This job has no verify step (none was given on submit)',
    'review.verify.reportHint': 'Taken from the "verification" section of the agent report ({source}); reference only, not a verify step gofer ran.',
    'review.verify.reportSource': 'source: the agent itself',
    'review.verify.reportHeading': '"{heading}" · {source}',
    'review.verify.fromAgent': 'the agent itself',
    'review.verify.exit': 'exit {code} · {dur}s',
    'review.verify.skipped': 'Verify skipped: {reason}',
    'review.verify.defaultReason': 'the agent did not finish cleanly',
    'review.verify.noBanner': 'No verify banner in the last 64KB of stderr (output may have been pushed out of the window)',
    // 用量
    'review.usage.none': 'The agent reported no usage',
    // 底部裁决条
    'review.act.acceptPlain': 'Accept',
    'review.act.acceptDeliver': 'Accept and {label}',
    'review.act.skipDelivery': 'Do not deliver this time',
    'review.act.rejectOpen': 'Reject…',
    'review.act.processing': 'Working…',
    'review.reviewed.line': 'Reviewed · {by} · {at}',
    'review.reviewed.note': 'Note: {note}',
    'review.reviewed.noRecord': 'This job is terminal but has no review record (reviewed_by is empty)',

    'review.memory.note':
      'Cross-task experience reported by the agent; accepting writes it into this job project memory (kind defaults to note), rejecting leaves no trace. Nothing is stored automatically.',
    'review.memory.needKey': 'Fill in a key first',
    'review.memory.saving': 'Working…',
    'review.memory.accept': 'Accept',
    'review.memory.reject': 'Reject',
    'review.memory.accepted': 'Accepted → {key}',
    'review.memory.rejected': 'Rejected',
    'review.verify.omitted': '… ({kept} earlier lines omitted, {total} in this block)',
  },
}
