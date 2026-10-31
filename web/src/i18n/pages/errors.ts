// 服务端错误响应的文案（key 前缀 `err.`）。错误体带稳定的 `code`（和占位符取值 `params`）时，
// 控制台用 `err.<code>` 在这里找句子，按当前界面语言渲染；找不到再显示服务端的 detail
// （api/errorText.ts）。句子与服务端目录 internal/i18n/lang/*/err.ini 逐字一致，
// Go 侧 TestWebErrorDictMatchesCatalog 会比对两边；改一边必须改另一边。
//
// 这里一行一个 key，格式不要改（那个测试按行解析）。
import type { PageDict } from './types'

export const errors: PageDict = {
  zh: {
    'err.messenger_unavailable': '该 server 没有启用会话传话（没有任务存储或传话配置）',
    'err.messenger_no_resident': '只有本机和 worker 类型的 runner 有常驻传话人',
    'err.messenger_no_worker_registry': 'server 没有接入 worker 注册表',
    'err.messenger_worker_offline': 'worker {worker} 当前没有连接，无法列出它的会话',
    'err.messenger_worker_too_old': 'worker {worker} 的协议版本是 v{have}，列出会话需要 v{need} 及以上；请先升级该 worker（gofer worker upgrade）',
    'err.workbench_turn_undeliverable': '会话当前无法接收消息：{reason}（{cause}）',
    'err.workbench_turn_undeliverable_plain': '会话当前无法接收消息：{cause}',
    'err.workbench_turn_no_open_turn': '会话当前无法接收消息：它没有在等待回复',
    'err.workbench_turn_ended': '会话当前无法接收消息：会话已结束',
    'err.session_approval_internal': 'a submitted job cannot carry its own approval policy; configure it per 收件 agent (mail.agents.<name>.approval) or per project (projects.<key>.approval)',
    'err.today_advice_steward_only': 'only the steward (or a person) advises on 「今天」 cards',
  },
  en: {
    'err.messenger_unavailable': 'Session messaging is not enabled on this server (no job store or messenger configuration)',
    'err.messenger_no_resident': 'Only the local runner and worker-type runners have a resident messenger',
    'err.messenger_no_worker_registry': 'The server is not connected to a worker registry',
    'err.messenger_worker_offline': 'Worker {worker} is not connected, so its sessions cannot be listed',
    'err.messenger_worker_too_old': 'Worker {worker} speaks protocol v{have}; listing sessions needs v{need} or later. Upgrade the worker first (gofer worker upgrade)',
    'err.workbench_turn_undeliverable': 'The session cannot receive messages right now: {reason} ({cause})',
    'err.workbench_turn_undeliverable_plain': 'The session cannot receive messages right now: {cause}',
    'err.workbench_turn_no_open_turn': 'The session cannot receive messages right now: it is not waiting for a reply',
    'err.workbench_turn_ended': 'The session cannot receive messages right now: the session has ended',
    'err.session_approval_internal': 'a submitted job cannot carry its own approval policy; configure it per mail agent (mail.agents.<name>.approval) or per project (projects.<key>.approval)',
    'err.today_advice_steward_only': 'only the steward (or a person) advises on Today cards',
  },
}
