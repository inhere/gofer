#!/usr/bin/env node
// 截图自动检查（设计稿 docs/design/2026-10-23-ui-redesign-design.md §13「截图检查」）。
//
// 对 6 个代表页（及各自的详情页）按「宽度 × 主题 × 语言」拍整页截图，并检查：
// 页面级横向滚动、超过 40px 的行内 SVG、计数 / 标签断行；英文界面残留中文为 warn。
// 结果写 <out>/report.json 并打印摘要，有 error 级问题退出码 1。
// 截图只给人看，不做像素比对。用法见 docs/reference/web-console.md「截图检查脚本」。
//
// Playwright 不在 package.json 里：运行时动态 import，缺失时打印安装提示并退出 2。
import { mkdir, writeFile } from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  STORAGE_KEYS,
  USAGE,
  UsageError,
  PAGES,
  collectSample,
  countShots,
  evaluateSample,
  exitCode,
  expandCombos,
  navigationIssue,
  parseArgs,
  pickDetailId,
  shotName,
  summarize,
  viewportFor,
} from './shots.lib.mjs'

const INSTALL_HINT = `缺少 playwright。任选其一安装后重试（本脚本不改 package.json / lock）：
  cd web && npm install --no-save playwright && npx playwright install chromium
  或已有系统浏览器时只装库并加 --channel msedge|chrome：  cd web && npm install --no-save playwright-core`

async function loadPlaywright() {
  for (const name of ['playwright', 'playwright-core']) {
    try {
      return await import(name)
    } catch {
      // 试下一个
    }
  }
  return null
}

// 取详情页 id：用 token 调列表接口取第一条；没有 token / 没有数据 / 接口失败都当作「没有详情」。
async function resolveDetailIds(opts) {
  const ids = {}
  const notes = []
  if (!opts.detail) return { ids, notes }
  if (!opts.token) {
    notes.push(`未提供 token（环境变量 ${opts.tokenEnv}），跳过全部详情页`)
    return { ids, notes }
  }
  for (const p of PAGES) {
    if (!p.detail || !opts.pages.includes(p.name)) continue
    try {
      const res = await fetch(opts.base + p.detail.api, { headers: { Authorization: `Bearer ${opts.token}` } })
      if (!res.ok) {
        notes.push(`${p.name}：详情 id 查询 HTTP ${res.status}，跳过详情页`)
        continue
      }
      const id = pickDetailId(await res.json(), p.detail.listKey)
      if (id) ids[p.name] = id
      else notes.push(`${p.name}：列表为空，跳过详情页`)
    } catch (e) {
      notes.push(`${p.name}：详情 id 查询失败（${e instanceof Error ? e.message : e}），跳过详情页`)
    }
  }
  return { ids, notes }
}

async function main() {
  let opts
  try {
    opts = parseArgs(process.argv.slice(2), process.env)
  } catch (e) {
    if (e instanceof UsageError) {
      console.error(`参数错误：${e.message}\n\n${USAGE}`)
      return 2
    }
    throw e
  }
  if (opts.help) {
    console.log(USAGE)
    return 0
  }

  const pw = await loadPlaywright()
  if (!pw) {
    console.error(INSTALL_HINT)
    return 2
  }

  const { ids, notes } = await resolveDetailIds(opts)
  const groups = expandCombos({
    pages: opts.pages,
    widths: opts.widths,
    themes: opts.themes,
    langs: opts.langs,
    detailIds: ids,
    includeDetail: opts.detail,
  })
  // 默认输出固定在 web/shots（与当前目录无关）；显式 --out 相对当前目录。
  const outDir = opts.outGiven ? path.resolve(opts.out) : fileURLToPath(new URL('../shots', import.meta.url))
  await mkdir(outDir, { recursive: true })
  console.log(`目标 ${opts.base}，共 ${countShots(groups)} 张 -> ${outDir}`)
  if (!opts.token) console.log(`提示：环境变量 ${opts.tokenEnv} 未设置，页面会被重定向到 /access`)
  for (const n of notes) console.log(`注意：${n}`)

  let browser
  try {
    browser = await pw.chromium.launch(opts.channel ? { channel: opts.channel } : {})
  } catch (e) {
    console.error(`启动浏览器失败：${e instanceof Error ? e.message.split('\n')[0] : e}\n可先 npx playwright install chromium，或用 --channel msedge|chrome`)
    return 2
  }

  const entries = []
  try {
    for (const g of groups) {
      const context = await browser.newContext({
        viewport: viewportFor(g.width),
        colorScheme: g.theme,
        locale: g.lang,
        deviceScaleFactor: 1,
      })
      // 在页面脚本之前写 localStorage：登录 token、主题、语言（键名见 STORAGE_KEYS）。
      // token 只经 init 脚本参数进入浏览器，不写日志 / report。
      await context.addInitScript(
        ({ keys, token, theme, lang }) => {
          try {
            if (token) localStorage.setItem(keys.token, token)
            localStorage.setItem(keys.theme, theme)
            localStorage.setItem(keys.locale, lang)
          } catch {
            // 存储不可用：靠 colorScheme / navigator.languages 兜底
          }
        },
        { keys: STORAGE_KEYS, token: opts.token, theme: g.theme, lang: g.lang },
      )
      const page = await context.newPage()
      for (const target of g.targets) {
        const file = shotName(g, target)
        const entry = { page: target.page, kind: target.kind, path: target.path, width: g.width, theme: g.theme, lang: g.lang, file, issues: [] }
        try {
          await page.goto(opts.base + target.path, { waitUntil: 'load', timeout: 30_000 })
          await page.waitForSelector('[data-test="app-shell"]', { timeout: 10_000 }).catch(() => undefined)
          await page.waitForTimeout(opts.settleMs)
          const finalPath = new URL(page.url()).pathname
          const nav = navigationIssue(target.path, finalPath)
          if (nav) entry.issues.push(nav)
          const sample = await page.evaluate(collectSample, { lang: g.lang })
          entry.issues.push(...evaluateSample(sample, { lang: g.lang }))
          await page.screenshot({ path: path.join(outDir, file), fullPage: true })
        } catch (e) {
          entry.issues.push({ check: 'capture', severity: 'error', message: `拍摄失败：${e instanceof Error ? e.message.split('\n')[0] : e}` })
        }
        entries.push(entry)
        const mark = entry.issues.some((i) => i.severity === 'error') ? 'FAIL' : entry.issues.length ? 'warn' : 'ok  '
        console.log(`${mark} ${file}`)
        for (const i of entry.issues) console.log(`       [${i.severity}] ${i.check}: ${i.message}`)
      }
      await context.close()
    }
  } finally {
    await browser.close()
  }

  const summary = summarize(entries)
  const report = {
    base: opts.base,
    generatedAt: new Date().toISOString(),
    options: { pages: opts.pages, widths: opts.widths, themes: opts.themes, langs: opts.langs, detail: opts.detail },
    notes,
    summary,
    shots: entries,
  }
  await writeFile(path.join(outDir, 'report.json'), JSON.stringify(report, null, 2) + '\n')

  console.log(`\n共 ${summary.shots} 张：error ${summary.errors}，warn ${summary.warnings}，有 error 的截图 ${summary.failedShots}`)
  for (const [check, c] of Object.entries(summary.byCheck)) console.log(`  ${check}: error ${c.error}, warn ${c.warn}`)
  console.log(`报告：${path.join(outDir, 'report.json')}`)
  return exitCode(summary, { failOnWarn: opts.failOnWarn })
}

main().then(
  (code) => process.exit(code),
  (e) => {
    console.error(e instanceof Error ? e.stack : e)
    process.exit(2)
  },
)
