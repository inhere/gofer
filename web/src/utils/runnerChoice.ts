// 新建任务 / 新建会话表单共用的 runner 选项收敛逻辑（NewJob、工作台 Composer、Sessions 页）。
// 原则：不可用的 runner 灰显并写原因，不静默移除；信息缺失 ≠ 不支持（worker 离线 / 未上报时放行，由后端拒）。
import type { MetaProject, MetaRunner, MetaWorker } from '../api/types'
import { t } from '../i18n'

export interface BlockNote {
  short: string
  full: string
}

// 持续 ACP 会话在 worker 上需要协议 v13（internal/wsproto SessionJobMinProtocolVersion）。
export const SESSION_MIN_PROTOCOL = 13

// 按项目 allowed_runners 过滤；空白名单 = 不限。
export function projectRunnerOptions(project: MetaProject | undefined, runners: MetaRunner[]): MetaRunner[] {
  const allowed = project?.allowed_runners ?? []
  if (allowed.length === 0) return runners
  const set = new Set(allowed)
  return runners.filter((r) => set.has(r.name))
}

// runner 名 → 不可用原因（空 = 可用）。
export function computeRunnerBlocks(
  project: MetaProject | undefined,
  options: MetaRunner[],
  workers: MetaWorker[],
): Record<string, BlockNote> {
  const out: Record<string, BlockNote> = {}
  if (!project) return out
  const anyReporting = workers.some((w) => w.connected && (w.projects ?? []).length > 0)
  for (const r of options) {
    if (r.type !== 'worker') {
      if (project.worker_only) {
        out[r.name] = {
          short: t('util.runnerChoice.workerOnly.short'),
          full: t('util.runnerChoice.workerOnly.full', { key: project.key }),
        }
      }
      continue
    }
    const pinned = r.worker_id ?? ''
    if (pinned !== '') {
      const w = workers.find((x) => x.id === pinned)
      if (!w || !w.connected || (w.projects ?? []).length === 0) continue
      if (!(w.projects ?? []).includes(project.key)) {
        out[r.name] = {
          short: t('util.runnerChoice.noProjectOnWorker.short', { worker: pinned }),
          full: t('util.runnerChoice.noProjectOnWorker.full', { worker: pinned, key: project.key }),
        }
      }
      continue
    }
    if (!anyReporting) continue
    if (!workers.some((w) => w.connected && (w.projects ?? []).includes(project.key))) {
      out[r.name] = {
        short: t('util.runnerChoice.noOnlineWorker.short'),
        full: t('util.runnerChoice.noOnlineWorker.full', { key: project.key }),
      }
    }
  }
  return out
}

// 持续 ACP 会话对该 runner 是否可用：local 可；worker 需协议 >= v13（离线 / 未知版本放行）；
// 其它（如 peer-http）不支持。返回 null = 可用。
export function sessionRunnerBlock(runner: MetaRunner, workers: MetaWorker[]): BlockNote | null {
  if (runner.type === 'local') return null
  if (runner.type !== 'worker') {
    return {
      short: t('util.runnerChoice.noSession.short'),
      full: t('util.runnerChoice.noSession.full', { name: runner.name, type: runner.type }),
    }
  }
  const pinned = runner.worker_id ?? ''
  const candidates = pinned !== '' ? workers.filter((w) => w.id === pinned) : workers
  const known = candidates.filter((w) => w.connected && (w.protocol_version ?? 0) > 0)
  if (known.length === 0) return null
  if (known.some((w) => (w.protocol_version ?? 0) >= SESSION_MIN_PROTOCOL)) return null
  const v = known[0].protocol_version
  return {
    short: t('util.runnerChoice.protocolOld.short', { v, min: SESSION_MIN_PROTOCOL }),
    full: t('util.runnerChoice.protocolOld.full', { id: known[0].id, v, min: SESSION_MIN_PROTOCOL }),
  }
}

// 合并 project 阻塞与（可选）持续会话阻塞，project 阻塞优先。
export function effectiveRunnerBlocks(
  options: MetaRunner[],
  base: Record<string, BlockNote>,
  workers: MetaWorker[],
  needSession: boolean,
): Record<string, BlockNote> {
  if (!needSession) return base
  const out = { ...base }
  for (const r of options) {
    if (out[r.name]) continue
    const b = sessionRunnerBlock(r, workers)
    if (b) out[r.name] = b
  }
  return out
}

// 默认 runner：当前值仍可用则保留；否则优先项目 allowed_runners[0]（可用时），再取第一个可用项，
// 全部不可用时仍落在首个选项上，让原因显示出来而不是留空下拉。
export function pickRunner(
  current: string,
  project: MetaProject | undefined,
  options: MetaRunner[],
  blocks: Record<string, BlockNote>,
): string {
  const usable = options.filter((r) => !blocks[r.name])
  if (usable.some((r) => r.name === current)) return current
  const first = project?.allowed_runners?.[0]
  if (first && usable.some((r) => r.name === first)) return first
  if (usable.length > 0) return usable[0].name
  return options[0]?.name ?? ''
}
