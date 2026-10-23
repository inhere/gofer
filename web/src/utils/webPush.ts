import { t } from '../i18n'

export function webPushUnavailableReason(): string {
  if (!window.isSecureContext) {
    return t('util.webPush.insecure')
  }
  if (!('serviceWorker' in navigator)) {
    return t('util.webPush.noServiceWorker')
  }
  if (!('Notification' in window)) {
    return t('util.webPush.noNotification')
  }
  if (!('PushManager' in window)) {
    return t('util.webPush.noPush')
  }
  if (Notification.permission === 'denied') {
    return t('util.webPush.denied')
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
