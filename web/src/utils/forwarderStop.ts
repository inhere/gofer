// 在线转发的「停止」按钮状态（/settings/tunnels）。
//
// - hosted：server 托管的转发，沿用预设行里的启动/停止（DELETE /v1/tunnels/hosted/{name}）。
// - stopping：已请求停止，等转发进程下一次心跳（≤30 秒）收到 410 后自行退出、从列表消失。
// - stoppable：进程登记时声明了 `stop` 能力，可远程停止（POST .../forwarders/{id}/stop）。
// - unsupported：旧版 gofer 没有这项能力，只能去那台机器上 Ctrl+C。
import type { TunnelForwarder } from '../api/types'

export type ForwarderStopState = 'hosted' | 'stopping' | 'stoppable' | 'unsupported'

export function forwarderStopState(f: TunnelForwarder): ForwarderStopState {
  if (f.hosted) {
    return 'hosted'
  }
  if (f.stop_requested) {
    return 'stopping'
  }
  return (f.caps ?? []).includes('stop') ? 'stoppable' : 'unsupported'
}

export function forwarderStopUnsupportedHint(f: TunnelForwarder): string {
  const host = f.host || '该机器'
  return `该转发进程的 gofer 版本过旧，不支持远程停止；请在 ${host} 上升级 gofer，或直接在 ${host} 上 Ctrl+C`
}
