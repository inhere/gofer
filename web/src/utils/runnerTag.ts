// 执行机标签（worker / server）的显示名与派生色：工作台会话列表与会话标题共用。
//
// 色相由 worker id 哈希到一组主题 token（styles/tokens.css 里的强调 / 状态色，明暗主题各有一套、
// 正文色都 ≥4.5:1），所以同一台 worker 在工作台、刷新后、换主题后恒为同一色；不同 worker 允许撞色
// ——标签的文字才是身份，颜色只是视觉锚点。server（本机内置 runner）固定中性色，与 G043 的显示
// 规则一致：wire / DB 里的规范名仍是 `local`，给人看的是 `server`。
import { LOCAL_RUNNER_DISPLAY, isLocalRunnerName } from './runnerDisplay'

// 与 tokens.css 的 --<tone> / --<tone>-soft 一一对应；idle 是中性色，只留给 server。
export type RunnerTone = 'run' | 'time' | 'accent' | 'need' | 'fail' | 'idle'

const TONES: readonly RunnerTone[] = ['run', 'time', 'accent', 'need', 'fail']

export const NEUTRAL_TONE: RunnerTone = 'idle'

// hashName：FNV-1a 32 位。worker id 大小写敏感（配置里怎么写就怎么用），所以不折叠大小写。
export function hashName(name: string): number {
  let h = 0x811c9dc5
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i)
    h = Math.imul(h, 0x01000193) >>> 0
  }
  return h >>> 0
}

// runnerTone：执行机名 → 色相。server / 未知 / 空 → 中性色。
export function runnerTone(name: string | undefined | null): RunnerTone {
  if (!name || isLocalRunnerName(name)) return NEUTRAL_TONE
  return TONES[hashName(name) % TONES.length]
}

export interface RunnerTagView {
  // 显示名：本机永远是 server（G043），其它原样
  label: string
  tone: RunnerTone
  // 正文色与底色：CSS 变量引用，主题切换时自动跟着变
  color: string
  soft: string
}

// runnerTag：标签的显示数据。本机 runner 的两种拼写（`local` / `server`）都归到 server；
// 拿不到执行机名时返回 null（不显示标签，而不是显示一个空壳）。
export function runnerTag(name: string | undefined | null): RunnerTagView | null {
  if (!name) return null
  const tone = runnerTone(name)
  return {
    label: isLocalRunnerName(name) ? LOCAL_RUNNER_DISPLAY : name,
    tone,
    color: `var(--${tone})`,
    soft: `var(--${tone}-soft)`,
  }
}
