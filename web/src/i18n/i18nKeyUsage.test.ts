// 用到的文案 key 必须存在（gofer-hez1）：扫描 web/src 里 t('字面量') / te('字面量') /
// translate('字面量') 的 key，逐个检查 zh-CN 与 en-US 字典。缺 key 时页面会直接显示原始 key
// （例如运行节点页的「runners.dirs.title」），只有肉眼截图才能发现，所以用测试兜住。
// 动态拼接的 key（t(`status.${s}`)、te(cond ? 'a' : 'b')）不在本测试范围内。
//
// 源码用 Vite 的 ?raw 一次性读进来（与 noHardcodedCjk.test.ts 同法，不必给测试引入 @types/node）。
import { describe, expect, it } from 'vitest'
import { dicts } from './index'

const RAW = import.meta.glob('/src/**/*.{ts,vue}', { query: '?raw', import: 'default', eager: true }) as Record<string, string>

// 只认「字面量 key 后面紧跟 , 或 )」的调用，'status.' + x 这类拼接不算。
const CALL = /\b(?:t|te|translate)\(\s*(['"])([^'"`$\n]{1,160})\1\s*[,)]/g

function rel(file: string): string {
  return file.replace(/^\/src\//, '')
}

function isScanned(file: string): boolean {
  const r = rel(file)
  return !r.endsWith('.test.ts') && !r.endsWith('.d.ts') && !r.startsWith('i18n/')
}

// 每个 key → 第一次出现的位置，报错时能直接找到改哪里。
function usedKeys(): Map<string, string> {
  const seen = new Map<string, string>()
  for (const [file, text] of Object.entries(RAW)) {
    if (!isScanned(file)) continue
    for (const m of text.matchAll(CALL)) {
      const key = m[2]
      if (seen.has(key)) continue
      const line = text.slice(0, m.index).split('\n').length
      seen.set(key, `${rel(file)}:${line}`)
    }
  }
  return seen
}

describe('i18n key usage', () => {
  it('every literal key used in web/src exists in both zh-CN and en-US', () => {
    const missing: string[] = []
    for (const [key, at] of usedKeys()) {
      for (const loc of ['zh-CN', 'en-US'] as const) {
        if (!Object.prototype.hasOwnProperty.call(dicts[loc], key)) missing.push(`${key} (${loc}) at ${at}`)
      }
    }
    expect(missing).toEqual([])
  })

  it('scans enough files and keys (guards against a broken walker)', () => {
    expect(Object.keys(RAW).filter(isScanned).length).toBeGreaterThan(80)
    expect(usedKeys().size).toBeGreaterThan(600)
  })
})
