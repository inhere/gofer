// 全站中文扫描（设计稿 §11 P6 / §14 P6 行）：web/src 下除 i18n 字典与测试外的源码里，
// 不允许出现硬编码中文——界面文案只能来自 i18n 字典（zh 与 en 并列）。
//
// 口径：
// - 注释里的中文不算（注释是给开发者看的）：.ts 与 SFC 的 <script> 交给 TypeScript 解析，
//   只看字符串字面量 / 模板字面量的内容，注释是 trivia、天然不参与；
// - SFC 的 <template> 按原文扫（属性值、文本节点都是界面文案），只去掉 HTML 注释与 <style>；
// - 字典本身（i18n/ 下）与 *.test.ts 不扫：测试可以按 zh 断言文案；
// - 确需写字面的地方进 WHITELIST，逐条写理由；还没抽完的文件进 PENDING（分批抽取），
//   PENDING 只许变短：某个文件抽干净了却还挂在表里，第二个用例会红。
import { describe, expect, it } from 'vitest'
import ts from 'typescript'

// 源码用 Vite 的 ?raw 一次性读进来（不必给测试引入 @types/node）。
const RAW = import.meta.glob('/src/**/*.{ts,vue}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

const CJK = /[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff]/

// 允许保留中文的字面量：按仓库相对路径（/ 分隔）给「允许出现的片段」+ 理由。
const WHITELIST: Record<string, { text: string[]; why: string }[]> = {
  'ui/LangToggle.vue': [{ text: ['中文'], why: '语言开关按各自语言写自身名（English / 中文），不翻译' }],
  'utils/attachmentText.ts': [
    {
      text: ['附图', '相对于项目', '根目录'],
      why: '识别 worker 写进正文的附图痕迹（`[附图 n] <路径>`），是协议文本而非界面文案；改了就不认老消息',
    },
  ],
}

// 还没抽完的文件：本次只覆盖工具层与公共组件，其余页面按批次跟进。
// 值写理由，便于认领；抽干净后必须从表里删掉（第二个用例守着）。
const PENDING: Record<string, string> = {
  'components/SessionDrawer.vue': 'P6-b 后续批次（尚未抽取）',
  'views/NewJob.vue': 'P6-b 后续批次（尚未抽取）',
  'components/WorkDispatchDialog.vue': 'P6-b 后续批次（尚未抽取）',
  'components/MergeDialog.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/uiText.ts': 'P6-b 后续批次（尚未抽取）',
  'components/IssueBatchBar.vue': 'P6-b 后续批次（尚未抽取）',
  'components/PlanSupervisor.vue': 'P6-b 后续批次（尚未抽取）',
  'components/SessionNudges.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/steward.ts': 'P6-b 后续批次（尚未抽取）',
  'components/AttachmentBar.vue': 'P6-b 后续批次（尚未抽取）',
  'components/EnrollAcceptDialog.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/compare.ts': 'P6-b 后续批次（尚未抽取）',
  'utils/todaySnooze.ts': 'P6-b 后续批次（尚未抽取）',
  'utils/trackerView.ts': 'P6-b 后续批次（尚未抽取）',
  'components/RunnerDirs.vue': 'P6-b 后续批次（尚未抽取）',
  'components/StewardPanel.vue': 'P6-b 后续批次（尚未抽取）',
  'components/EnrollRejectDialog.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/capture.ts': 'P6-b 后续批次（尚未抽取）',
  'utils/permissionGate.ts': 'P6-b 后续批次（尚未抽取）',
  'api/attachments.ts': 'P6-b 后续批次（尚未抽取）',
  'components/today/SnoozedDrawer.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/injectMode.ts': 'P6-b 后续批次（尚未抽取）',
  'components/CommentThread.vue': 'P6-b 后续批次（尚未抽取）',
  'components/today/DecisionOverlay.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/todoContinue.ts': 'P6-b 后续批次（尚未抽取）',
  'views/NewWorkflow.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/wrapUpDisplay.ts': 'P6-b 后续批次（尚未抽取）',
  'components/MemoryList.vue': 'P6-b 后续批次（尚未抽取）',
  'components/UncommittedBadge.vue': 'P6-b 后续批次（尚未抽取）',
  'main.ts': 'P6-b 后续批次（尚未抽取）',
  'utils/planProgress.ts': 'P6-b 后续批次（尚未抽取）',
  'ui/LangToggle.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/pageGate.ts': 'P6-b 后续批次（尚未抽取）',
  'views/Skills.vue': 'P6-b 后续批次（尚未抽取）',
  'components/InfoCard.vue': 'P6-b 后续批次（尚未抽取）',
  'components/today/TodayLaneRow.vue': 'P6-b 后续批次（尚未抽取）',
  'components/today/TodayStatusBar.vue': 'P6-b 后续批次（尚未抽取）',
  'utils/findings.ts': 'P6-b 后续批次（尚未抽取）',
  'utils/mcpView.ts': 'P6-b 后续批次（尚未抽取）',
  'utils/mobileLayout.ts': 'P6-b 后续批次（尚未抽取）',
  'views/Projects.vue': 'P6-b 后续批次（尚未抽取）',
}

interface Hit {
  line: number
  text: string
}

function rel(file: string): string {
  return file.replace(/^\/src\//, '')
}

function lineOf(text: string, pos: number): number {
  return text.slice(0, pos).split('\n').length
}

// 一段 TS 源码里的中文字符串字面量（含模板字面量、JSX 文本）。注释是 trivia，不会被收进来。
function tsStringHits(code: string): Hit[] {
  const file = ts.createSourceFile('scan.ts', code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
  const out: Hit[] = []
  const visit = (node: ts.Node): void => {
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node) || ts.isTemplateHead(node) || ts.isTemplateMiddle(node) || ts.isTemplateTail(node) || ts.isJsxText(node)) {
      const value = ts.isJsxText(node) ? node.getText(file) : node.text
      if (CJK.test(value)) out.push({ line: lineOf(code, node.getStart(file)), text: value.trim() })
    }
    node.forEachChild(visit)
  }
  visit(file)
  return out
}

interface Block {
  start: number
  end: number
  body: string
}

function blocksOf(raw: string, tag: string): Block[] {
  const out: Block[] = []
  const re = new RegExp(`<${tag}\\b[^>]*>([\\s\\S]*?)</${tag}>`, 'g')
  for (let m = re.exec(raw); m; m = re.exec(raw)) {
    out.push({ start: m.index, end: m.index + m[0].length, body: m[1] })
  }
  return out
}

// 一个 SFC 的命中：<template> 按原文扫（去 HTML 注释与 <style>），<script> 交给 TS。
function sfcHits(raw: string): Hit[] {
  const hits: Hit[] = []
  const scripts = blocksOf(raw, 'script')
  for (const s of scripts) {
    const offset = lineOf(raw, s.start)
    for (const h of tsStringHits(s.body)) hits.push({ line: h.line + offset - 1, text: h.text })
  }
  // 模板 = 整个文件去掉 script / style 块，再按行扫。
  let rest = raw
  for (const tag of ['script', 'style']) {
    for (const b of blocksOf(rest, tag)) {
      rest = rest.slice(0, b.start) + rest.slice(b.start).replace(b.body, (m) => m.replace(/[^\n]/g, ' '))
    }
  }
  const noComments = rest.replace(/<!--[\s\S]*?-->/g, (m) => m.replace(/[^\n]/g, ' '))
  noComments.split('\n').forEach((line, i) => {
    if (CJK.test(line)) hits.push({ line: i + 1, text: line.trim() })
  })
  return hits
}

function fileHits(file: string): Hit[] {
  const raw = RAW[file] ?? ''
  return file.endsWith('.vue') ? sfcHits(raw) : tsStringHits(raw)
}

// 按 WHITELIST 去掉允许的片段后再判定（片段本身可能带中文）。
function hits(file: string): string[] {
  const key = rel(file)
  const allowed = (WHITELIST[key] ?? []).flatMap((r) => r.text)
  const lower = (s: string): string => allowed.reduce((acc, piece) => acc.split(piece).join(''), s)
  return fileHits(file)
    .filter((h) => CJK.test(lower(h.text)))
    .map((h) => `${key}:${h.line}: ${h.text}`)
}

const FILES = Object.keys(RAW).filter((f) => !f.endsWith('.test.ts') && !rel(f).startsWith('i18n/'))

describe('no hardcoded Chinese in sources', () => {
  it('leaves every file free of hardcoded CJK (dictionaries, tests and the whitelist aside)', () => {
    const offenders = FILES.filter((f) => !PENDING[rel(f)]).flatMap(hits)
    expect(offenders, '这些文案要走 i18n：把字面量换成 t(...) 并在 i18n/pages/ 下补 zh / en').toEqual([])
  })

  it('keeps the PENDING list honest (only files that still have hardcoded CJK)', () => {
    const stale = Object.keys(PENDING).filter((key) => {
      const file = FILES.find((f) => rel(f) === key)
      // 文件没了或已经抽干净，都要把表项删掉
      return !file || hits(file).length === 0
    })
    expect(stale, '这些文件已经没有硬编码中文了：从 PENDING 里删掉').toEqual([])
  })
})
