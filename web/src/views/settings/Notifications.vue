<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { ApiError } from '../../api/client'
import {
  createPushSubscription,
  deletePushSubscription,
  getVAPIDPublicKey,
  listPushSubscriptions,
  sendTestPush,
} from '../../api/push'
import type { PushSubscriptionView } from '../../api/types'
import {
  currentPushSubscription,
  decodeVAPIDPublicKey,
  webPushUnavailableReason,
} from '../../utils/webPush'

const rows = ref<PushSubscriptionView[]>([])
const currentEndpoint = ref('')
const unavailable = ref('')
const loading = ref(true)
const busy = ref(false)
const error = ref('')
const notice = ref('')
const permission = ref<NotificationPermission>(
  'Notification' in window ? Notification.permission : 'default',
)

const available = computed(() => unavailable.value === '')
const currentEnabled = computed(() =>
  currentEndpoint.value !== '' && rows.value.some((row) => row.endpoint === currentEndpoint.value),
)

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    rows.value = (await listPushSubscriptions()).subscriptions
    unavailable.value = webPushUnavailableReason()
    permission.value = 'Notification' in window ? Notification.permission : 'default'
    if (!unavailable.value) {
      currentEndpoint.value = (await currentPushSubscription())?.endpoint ?? ''
    } else {
      currentEndpoint.value = ''
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function enable(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    unavailable.value = webPushUnavailableReason()
    if (unavailable.value) return
    if (Notification.permission === 'default') {
      permission.value = await Notification.requestPermission()
    }
    if (Notification.permission !== 'granted') {
      unavailable.value = webPushUnavailableReason() || '没有系统通知权限，无法开启推送。'
      return
    }
    const registration = await navigator.serviceWorker.ready
    let subscription = await registration.pushManager.getSubscription()
    if (!subscription) {
      const { public_key: publicKey } = await getVAPIDPublicKey()
      subscription = await registration.pushManager.subscribe({
        userVisibleOnly: true,
        applicationServerKey: decodeVAPIDPublicKey(publicKey),
      })
    }
    const json = subscription.toJSON()
    if (!subscription.endpoint || !json.keys?.p256dh || !json.keys.auth) {
      throw new Error('浏览器没有返回完整的 PushSubscription keys。')
    }
    await createPushSubscription({
      endpoint: subscription.endpoint,
      keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
    })
    notice.value = '本设备推送已开启。'
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function disable(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const subscription = await currentPushSubscription()
    if (!subscription) {
      notice.value = '本设备没有活动订阅。'
      await load()
      return
    }
    try {
      await deletePushSubscription(subscription.endpoint)
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 404) throw e
    }
    await subscription.unsubscribe()
    notice.value = '本设备推送已关闭。'
    await load()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

async function testPush(): Promise<void> {
  if (busy.value) return
  busy.value = true
  error.value = ''
  notice.value = ''
  try {
    const result = await sendTestPush()
    notice.value = '测试通知已排队（' + result.queued + ' 个设备）。'
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    busy.value = false
  }
}

function shortEndpoint(endpoint: string): string {
  return endpoint.length <= 76 ? endpoint : endpoint.slice(0, 50) + '…' + endpoint.slice(-20)
}

function formatTime(value: number): string {
  return value > 0 ? new Date(value * 1000).toLocaleString() : '尚未成功投递'
}

onMounted(() => {
  void load()
})
</script>

<template>
  <section class="notifications">
    <header class="section-head">
      <div>
        <span class="eyebrow mono">WEB PUSH</span>
        <h2>通知</h2>
      </div>
      <button class="ghost mono" type="button" :disabled="loading || busy" @click="load">
        刷新
      </button>
    </header>

    <p v-if="unavailable" class="warning mono">{{ unavailable }}</p>
    <p v-if="error" class="error mono">{{ error }}</p>
    <p v-if="notice" class="notice mono">{{ notice }}</p>

    <div class="device-card">
      <div>
        <strong>本设备</strong>
        <p class="muted mono">
          {{ currentEnabled ? '已开启' : '未开启' }} · permission={{ permission }}
        </p>
      </div>
      <div class="actions">
        <button
          class="primary mono"
          type="button"
          :disabled="busy || !available || currentEnabled"
          @click="enable"
        >
          开启推送
        </button>
        <button
          class="ghost mono"
          type="button"
          :disabled="busy || !currentEndpoint"
          @click="disable"
        >
          关闭本设备
        </button>
        <button
          class="ghost mono"
          type="button"
          :disabled="busy || rows.length === 0"
          @click="testPush"
        >
          发送测试推送
        </button>
      </div>
    </div>

    <div class="list-head">
      <strong>当前 caller 的订阅</strong>
      <span class="muted mono">{{ rows.length }} device(s)</span>
    </div>
    <p v-if="!loading && rows.length === 0" class="empty mono">暂无订阅。</p>
    <ul v-else class="subscription-list">
      <li v-for="row in rows" :key="row.endpoint">
        <div class="endpoint mono" :title="row.endpoint">
          {{ shortEndpoint(row.endpoint) }}
          <span v-if="row.endpoint === currentEndpoint" class="current">本设备</span>
        </div>
        <div class="meta mono">
          <span>{{ row.user_agent || 'unknown browser' }}</span>
          <span>created {{ formatTime(row.created_at) }}</span>
          <span>last ok {{ formatTime(row.last_ok_at) }}</span>
        </div>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.notifications {
  display: grid;
  gap: 14px;
}
.section-head,
.device-card,
.list-head,
.actions,
.meta {
  display: flex;
  align-items: center;
}
.section-head,
.device-card,
.list-head {
  justify-content: space-between;
  gap: 16px;
}
h2 {
  margin: 2px 0 0;
  color: var(--paper);
  font-size: 18px;
}
.eyebrow {
  color: var(--phosphor);
  font-size: 10px;
  letter-spacing: 0.18em;
}
.device-card,
.subscription-list li {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 14px;
}
.actions {
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
button {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 7px 10px;
  cursor: pointer;
}
button:disabled {
  cursor: not-allowed;
  opacity: 0.45;
}
.primary {
  color: var(--ink);
  background: var(--phosphor);
}
.ghost {
  color: var(--paper);
  background: transparent;
}
.warning,
.error,
.notice,
.empty {
  margin: 0;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
}
.warning {
  color: var(--warning);
}
.error {
  color: var(--danger);
}
.notice {
  color: var(--phosphor);
}
.muted,
.meta {
  color: var(--queue);
  font-size: 11px;
}
.subscription-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.endpoint {
  color: var(--paper);
  overflow-wrap: anywhere;
}
.current {
  margin-left: 8px;
  color: var(--phosphor);
}
.meta {
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 8px;
}
@media (max-width: 768px) {
  .device-card {
    align-items: flex-start;
    flex-direction: column;
  }
  .actions {
    justify-content: flex-start;
  }
}
</style>
