export function webPushUnavailableReason(): string {
  if (!window.isSecureContext) {
    return '推送需要 HTTPS 或 localhost，当前页面不是安全上下文。'
  }
  if (!('serviceWorker' in navigator)) {
    return '当前浏览器不支持 Service Worker。'
  }
  if (!('Notification' in window)) {
    return '当前浏览器不支持系统通知。'
  }
  if (!('PushManager' in window)) {
    return '当前浏览器不支持 Web Push。'
  }
  if (Notification.permission === 'denied') {
    return '系统通知权限已被拒绝，请在浏览器站点设置中重新允许。'
  }
  return ''
}

export function decodeVAPIDPublicKey(value: string): ArrayBuffer {
  const padded = value.replace(/-/g, '+').replace(/_/g, '/').padEnd(
    Math.ceil(value.length / 4) * 4,
    '=',
  )
  const raw = window.atob(padded)
  const buffer = new ArrayBuffer(raw.length)
  const out = new Uint8Array(buffer)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return buffer
}

export async function currentPushSubscription(): Promise<PushSubscription | null> {
  if (webPushUnavailableReason()) return null
  const registration = await navigator.serviceWorker.ready
  return registration.pushManager.getSubscription()
}
