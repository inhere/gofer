// 通知设置（gofer-x9mb）：事件目录与测试投递。
// 配置本身（server.notification.webhooks / desktop）仍然走 PUT /v1/config/server 的补丁式
// 写入 —— 这里只负责问「有哪些事件」和「这条目标现在能不能发出去」。
import { request } from './client'
import type { NotifyEventsResp, NotifyTestResp } from './types'

export function listNotifyEvents(): Promise<NotifyEventsResp> {
  return request<NotifyEventsResp>('/v1/notify/events')
}

// sendNotifyTest 发一条测试消息：target 为 'webhook' 时用 index 指定第几个机器人，
// 'desktop' 走 server 主机的桌面通道。
export function sendNotifyTest(target: 'webhook' | 'desktop', index = 0): Promise<NotifyTestResp> {
  return request<NotifyTestResp>('/v1/notify/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(target === 'webhook' ? { target, index } : { target }),
  })
}
