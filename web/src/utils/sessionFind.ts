// Sessions 历史页的「按主题 / 路径找」（CLI `gofer session find` 同一接口：
// GET /v1/sessions?q=|path=）。页面只做接线，口径都在这里：搜索框输入 → 查找参数、
// 点一行去哪。纯函数，不碰 DOM，便于单测。
import type { WorkbenchThread } from '../api/types'
import { acpThreadLink } from './acpThreads'
import { buildFindParams } from './sessionTopic'

export interface FindQuery {
  q?: string
  path?: string
  project?: string
  limit: number
}

// findQuery：搜索框输入 → 会话查找接口的参数。空输入返回 null（不查找，页面停在空态）。
// 输入判定与服务端 / CLI 一致（sessionTopic.buildFindParams）：含 / 或 \ 且无空白当路径，
// 其余当关键词；查找是相关度排序，所以不分页、一次取一页上限。project 跟着页面顶部的
// 项目筛选走（与服务端 findSessions 的 Visible 条件同口径）。
export function findQuery(input: string, limit: number, project = ''): FindQuery | null {
  const params = buildFindParams(input)
  if (!params) return null
  return { ...params, project: project || undefined, limit }
}

export type FindTarget =
  | { kind: 'thread'; threadID: string; link: string }
  | { kind: 'drawer'; sid: string }

function threadSessionID(threadID: string): string {
  const at = threadID.indexOf(':')
  return at >= 0 ? threadID.slice(at + 1) : ''
}

// findRowTarget：点一行打开哪个会话。终端会话（relay 线程）打开详情抽屉
// （SessionDrawer：中继开关、授权、唤醒 / 接管、主题、删除都在里面）；同一会话有 ACP / pty
// 的工作台线程时进对应线程（可在工作台接着聊）。查找结果来自会话登记表，未必在当前这一页
// 历史里，所以按页面已加载的线程判断，认不出来的一律按终端会话处理。
export function findRowTarget(sid: string, threads: Array<Pick<WorkbenchThread, 'id' | 'kind'>>): FindTarget {
  const matched = threads.filter((thread) => threadSessionID(thread.id) === sid)
  const thread = matched.find((item) => item.kind !== 'relay') ?? matched[0]
  if (!thread || thread.kind === 'relay') return { kind: 'drawer', sid }
  return { kind: 'thread', threadID: thread.id, link: acpThreadLink(thread.id) }
}
