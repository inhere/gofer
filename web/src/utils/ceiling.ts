// 调用方上限（ceiling，上限）：配置 server.callers[].ceiling 给一个 token 划的**上界**，
// 由 GET /v1/auth/whoami 的 `ceiling` 字段下发（字段缺省 = 这个调用方没有上限）。
// 判定语义与后端 internal/config/ceiling.go 逐条一致——两边必须同口径，否则界面会放出一个
// 提交必被 404 / 403 的选项：
//   projects / agents / workers：glob 列表（Go path.Match 语义，`*` 不跨 `/`）；空列表 = 该维度不限；
//   runners：空列表 = 只允许远程 worker（不含本机内置 runner）；本机 runner 只有**写明**
//            local / server 才允许，glob 永不匹配它；
//   allow_exec / read_only：whoami 给的是**已生效**的布尔值（缺 key 的严格默认已在服务端套上：
//            allow_exec 为 false，read_only 为 false）。
// 这里全是纯函数：不认识 whoami、不发请求，界面只是拿它决定"给不给这个选项"。
import { t } from '../i18n'
import { LOCAL_RUNNER, LOCAL_RUNNER_DISPLAY } from './runnerDisplay'
import type { AuthCeiling, AuthWhoami } from './permissions'

// 选项被上限挡下时的统一说明（灰显原因 / 提交前的校验文案）。做成函数而非常量：常量会在
// 模块加载时把语言定住，切语言后不跟着变。
export function ceilingLabel(): string {
  return t('util.ceiling.denied')
}

// 上限挡下的具体原因（谁、被哪个维度挡的）。
export function ceilingDeniedText(kind: string, value: string): string {
  return `${ceilingLabel()}：${t('util.ceiling.notInRange', { kind, value })}`
}

// callerCeiling：请求方的上限，没有上限（旧服务端 / 未配置 / worker 与 job 凭据）返回 null。
export function callerCeiling(who: AuthWhoami | null | undefined): AuthCeiling | null {
  return who?.ceiling ?? null
}

// ---- glob ----

const REGEX_META = /[.*+?^${}()|[\]\\]/g
const CLASS_META = /[\]\\^]/g

function escapeRegExp(s: string): string {
  return s.replace(REGEX_META, '\\$&')
}

// globToRegExp：把 Go path.Match 的 glob 翻成正则。`*` / `?` 只在一层内（不跨 `/`），
// `[a-z]` 字符类可用（`^` 开头为取反），`\` 转义。模式非法（尾部反斜杠、未闭合的 `[`）返回 null：
// 后端 ceilingMatches 把非法模式当"不匹配"，加载期又会拒绝这种配置，这里同样不匹配。
export function globToRegExp(glob: string): RegExp | null {
  let out = ''
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i]
    if (c === '*') {
      out += '[^/]*'
      continue
    }
    if (c === '?') {
      out += '[^/]'
      continue
    }
    if (c === '\\') {
      i++
      if (i >= glob.length) return null
      out += escapeRegExp(glob[i])
      continue
    }
    if (c === '[') {
      let j = i + 1
      let neg = false
      if (glob[j] === '^') {
        neg = true
        j++
      }
      let cls = ''
      let closed = false
      for (; j < glob.length; j++) {
        const d = glob[j]
        if (d === ']' && cls !== '') {
          closed = true
          break
        }
        if (d === '\\') {
          j++
          if (j >= glob.length) return null
          cls += glob[j].replace(CLASS_META, '\\$&')
          continue
        }
        cls += d
      }
      if (!closed) return null
      out += `[${neg ? '^' : ''}${cls}]`
      i = j
      continue
    }
    out += escapeRegExp(c)
  }
  return new RegExp(`^${out}$`)
}

// globMatch：value 是否匹配某一个模式（空数组 / 非法模式 = 不匹配）。
export function globMatch(patterns: readonly string[] | undefined, value: string): boolean {
  for (const pattern of patterns ?? []) {
    const re = globToRegExp(pattern)
    if (re && re.test(value)) {
      return true
    }
  }
  return false
}

// ---- runner 名的归一化（G043）----

// 本机内置 runner 两种拼写是同一个：wire / DB 用 local，CLI 与界面显示用 server。
// 与后端 config.NormalizeRunnerName 一致（大小写不敏感、纯拼写映射、无例外）。
export function normalizeRunnerName(name: string | null | undefined): string {
  const n = (name ?? '').trim().toLowerCase()
  return n === LOCAL_RUNNER_DISPLAY ? LOCAL_RUNNER : n
}

// 本机 runner（两种拼写都算）。上限里的本机 runner 只能按名字给。
export function isMachineRunner(name: string | null | undefined): boolean {
  const n = normalizeRunnerName(name)
  return n === LOCAL_RUNNER || n === LOCAL_RUNNER_DISPLAY
}

// ---- 逐维判定 ----

// 项目：空列表 = 不限；非空要求匹配（没写名字的请求在受限维度里失败关闭）。
export function ceilingAllowsProject(ceil: AuthCeiling | null | undefined, key: string): boolean {
  if (!ceil || !ceil.projects || ceil.projects.length === 0) return true
  return globMatch(ceil.projects, key)
}

// worker：空列表 = 不限。
export function ceilingAllowsWorker(ceil: AuthCeiling | null | undefined, id: string): boolean {
  if (!ceil || !ceil.workers || ceil.workers.length === 0) return true
  return globMatch(ceil.workers, id)
}

// agent：空列表 = 不限。
export function ceilingAllowsAgent(ceil: AuthCeiling | null | undefined, key: string): boolean {
  if (!ceil || !ceil.agents || ceil.agents.length === 0) return true
  return globMatch(ceil.agents, key)
}

// runner：空列表 = 只允许远程 worker（本机 runner 必须写明）；非空时本机 runner 只按名字匹配，
// glob（含 `*`）不匹配它——本机 runner 上的 job 跑在 server 主机上，能读配置与 token。
export function ceilingAllowsRunner(ceil: AuthCeiling | null | undefined, name: string): boolean {
  if (!ceil) return true
  const n = normalizeRunnerName(name)
  const machine = isMachineRunner(n)
  const patterns = ceil.runners ?? []
  if (patterns.length === 0) return !machine
  for (const pattern of patterns) {
    const p = normalizeRunnerName(pattern)
    if (p === n) return true
    if (machine) continue
    const re = globToRegExp(p)
    if (re && re.test(n)) return true
  }
  return false
}

// exec 类 job（agent=exec / cmd / verify argv）：没有上限时不受限；有上限时按 allow_exec
// （服务端已把"没写"折成 false，故这里只认 true）。
export function ceilingAllowsExec(ceil: AuthCeiling | null | undefined): boolean {
  if (!ceil) return true
  return ceil.allow_exec === true
}

// 全局记忆写入：没有上限时不受限；有上限时按 global_memory。
export function ceilingAllowsGlobalMemory(ceil: AuthCeiling | null | undefined): boolean {
  if (!ceil) return true
  return ceil.global_memory === true
}

// 只读：这个调用方的每个写操作都会被服务端 403（个人偏好类 ReadSafe 写除外）。
export function ceilingReadOnly(ceil: AuthCeiling | null | undefined): boolean {
  return ceil?.read_only === true
}

// 有没有真的设了上限（界面据此决定要不要显示上限摘要 / 只读标记）。
export function hasCeiling(ceil: AuthCeiling | null | undefined): boolean {
  return ceil != null
}

// 列了哪些项目（非空 = 这一维受限），界面用它决定要不要提"只列范围内的 project"。
export function ceilingLimitsProjects(ceil: AuthCeiling | null | undefined): boolean {
  return !!ceil && (ceil.projects?.length ?? 0) > 0
}

// ---- 界面文案 ----

function listText(patterns: readonly string[] | undefined): string {
  const list = (patterns ?? []).filter((p) => p.trim() !== '')
  return list.length === 0 ? t('util.ceiling.any') : list.join(t('util.sep.list'))
}

// ceilingSummary：设置 → 权限页上的本人上限摘要，一行一维（顺序稳定）。
export function ceilingSummary(ceil: AuthCeiling | null | undefined): string[] {
  if (!ceil) return []
  const runners = (ceil.runners ?? []).filter((p) => p.trim() !== '')
  return [
    t('util.ceiling.summary.projects', { list: listText(ceil.projects) }),
    // 空列表不是"不限"：它只放远程 worker 通过，本机 server 被排除在外。
    t('util.ceiling.summary.runners', {
      list: runners.length === 0
        ? t('util.ceiling.summary.remoteOnly', { local: LOCAL_RUNNER_DISPLAY })
        : runners.join(t('util.sep.list')),
    }),
    t('util.ceiling.summary.workers', { list: listText(ceil.workers) }),
    t('util.ceiling.summary.agents', { list: listText(ceil.agents) }),
    t('util.ceiling.summary.exec', { value: ceil.allow_exec === true ? t('util.ceiling.allow') : t('util.ceiling.deny') }),
    t('util.ceiling.summary.memory', { value: ceil.global_memory === true ? t('util.ceiling.allow') : t('util.ceiling.deny') }),
    t('util.ceiling.summary.readOnly', { value: ceil.read_only === true ? t('util.ceiling.yes') : t('util.ceiling.no') }),
  ]
}

// 只读标记 / 说明用的短句（顶栏徽标、设置页、写入口的悬停说明共用一句）。
export function ceilingReadOnlyNotice(): string {
  return t('util.ceiling.readOnly')
}
