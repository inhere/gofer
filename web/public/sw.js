/* gofer W2b service worker: notifications only. Deliberately no fetch event or cache. */

async function focusOrOpen(rawURL) {
  const target = new URL(rawURL || '/workbench', self.location.origin).href
  const windows = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
  const existing = windows.find((client) => new URL(client.url).origin === self.location.origin)
  if (existing) {
    if ('navigate' in existing) {
      try {
        await existing.navigate(target)
      } catch {
        // A stale/uncontrolled window can refuse navigation; opening below remains safe.
      }
    }
    await existing.focus()
    return
  }
  await self.clients.openWindow(target)
}

self.addEventListener('push', (event) => {
  let payload = {}
  try {
    payload = event.data ? event.data.json() : {}
  } catch {
    payload = { title: 'gofer', body: '有新的待处理事项', url: '/workbench' }
  }
  const actions = Array.isArray(payload.actions)
    ? payload.actions.map((item) => ({ action: item.action, title: item.title }))
    : []
  const actionOptions = {}
  for (const item of Array.isArray(payload.actions) ? payload.actions : []) {
    actionOptions[item.action] = item.option
  }
  const tag = payload.tag || payload.thread_id || ''
  event.waitUntil(
    self.registration.showNotification(payload.title || 'gofer', {
      body: payload.body || '',
      icon: '/android-chrome-192x192.png',
      badge: '/favicon-48x48.png',
      tag,
      renotify: Boolean(tag),
      actions,
      data: {
        url: payload.url || '/workbench',
        token: payload.action_token || '',
        actionOptions,
      },
    }),
  )
})

self.addEventListener('notificationclick', (event) => {
  const notification = event.notification
  const data = notification.data || {}
  notification.close()
  if (!event.action || !data.token) {
    event.waitUntil(focusOrOpen(data.url))
    return
  }
  const option = data.actionOptions?.[event.action]
  if (!option) {
    event.waitUntil(focusOrOpen(data.url))
    return
  }
  event.waitUntil(
    fetch('/v1/push/actions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: data.token, option }),
    })
      .then((response) => {
        if (!response.ok) throw new Error('push action failed: ' + response.status)
      })
      .catch(() => focusOrOpen(data.url)),
  )
})

self.addEventListener('notificationclose', () => {
  // No durable acknowledgement is required; closing a notification is not an answer.
})
