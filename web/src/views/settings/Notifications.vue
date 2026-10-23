<script setup lang="ts">
// 设置 · 通知（设计稿 §7.4）：本设备推送开关与当前 caller 的订阅列表。
// 复用既有接口：GET/POST/DELETE /v1/push/subscriptions、GET /v1/push/vapid-public-key、
// POST /v1/push/test。UI 重设计 P5-d：FormSection 分节 + 文案走 i18n（settings.notify.*）。
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
import { t } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'
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
const currentEnabled = computed(
  () => currentEndpoint.value !== '' && rows.value.some((row) => row.endpoint === currentEndpoint.value),
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
      unavailable.value = webPushUnavailableReason() || t('settings.notify.needPermission')
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
      throw new Error(t('settings.notify.incompleteKeys'))
    }
    await createPushSubscription({
      endpoint: subscription.endpoint,
      keys: { p256dh: json.keys.p256dh, auth: json.keys.auth },
    })
    notice.value = t('settings.notify.enabled')
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
      notice.value = t('settings.notify.noActiveSub')
      await load()
      return
    }
    try {
      await deletePushSubscription(subscription.endpoint)
    } catch (e) {
      if (!(e instanceof ApiError) || e.status !== 404) throw e
    }
    await subscription.unsubscribe()
    notice.value = t('settings.notify.disabled')
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
    notice.value = t('settings.notify.testQueued', { n: result.queued })
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
  return value > 0 ? new Date(value * 1000).toLocaleString() : t('settings.notify.never')
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="notifications">
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.notify.title') }}</h1>
        <p class="sub">{{ t('settings.notify.sub') }}</p>
      </div>
      <button class="btn" type="button" :disabled="loading || busy" @click="load">
        {{ t('act.refresh') }}
      </button>
    </header>

    <p v-if="unavailable" class="msg msg--warn">{{ unavailable }}</p>
    <p v-if="error" class="msg msg--err">{{ error }}</p>
    <p v-if="notice" class="msg msg--ok">{{ notice }}</p>

    <FormSection :title="t('settings.notify.deviceTitle')" :desc="t('settings.notify.deviceDesc')">
      <div class="toggle-row">
        <div class="tx">
          <b>{{ t('settings.notify.thisDevice') }}</b>
          <span data-testid="push-state">
            {{ currentEnabled ? t('settings.notify.on') : t('settings.notify.off') }} ·
            {{ t('settings.notify.permission', { value: permission }) }}
          </span>
        </div>
        <span class="pill" :class="currentEnabled ? 'pill--ok' : 'pill--idle'">
          {{ currentEnabled ? t('settings.notify.on') : t('settings.notify.off') }}
        </span>
      </div>
      <div class="actions">
        <button
          class="btn btn--primary"
          type="button"
          :disabled="busy || !available || currentEnabled"
          @click="enable"
        >
          {{ t('settings.notify.enable') }}
        </button>
        <button class="btn" type="button" :disabled="busy || !currentEndpoint" @click="disable">
          {{ t('settings.notify.disable') }}
        </button>
        <button class="btn" type="button" :disabled="busy || rows.length === 0" @click="testPush">
          {{ t('settings.notify.testPush') }}
        </button>
      </div>
    </FormSection>

    <FormSection :title="t('settings.notify.subsTitle')" :desc="t('settings.notify.subsDesc')">
      <div class="list-head">
        <strong>{{ t('settings.notify.subsList') }}</strong>
        <span class="muted">{{ t('settings.notify.deviceCount', { n: rows.length }) }}</span>
      </div>
      <p v-if="!loading && rows.length === 0" class="empty" data-testid="push-empty">
        {{ t('settings.notify.empty') }}
      </p>
      <ul v-else class="subscription-list">
        <li v-for="row in rows" :key="row.endpoint" data-testid="push-row">
          <div class="endpoint mono" :title="row.endpoint">
            {{ shortEndpoint(row.endpoint) }}
            <span v-if="row.endpoint === currentEndpoint" class="current">
              {{ t('settings.notify.thisDevice') }}
            </span>
          </div>
          <div class="meta">
            <span>{{ row.user_agent || t('settings.notify.unknownBrowser') }}</span>
            <span>{{ t('settings.notify.created', { time: formatTime(row.created_at) }) }}</span>
            <span>{{ t('settings.notify.lastOk', { time: formatTime(row.last_ok_at) }) }}</span>
          </div>
        </li>
      </ul>
    </FormSection>
  </div>
</template>

<style scoped>
.notifications {
  display: grid;
  gap: 4px;
  min-width: 0;
}

.page-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 6px;
}
.page-head .grow {
  flex: 1;
  min-width: 0;
}
.page-head h1 {
  margin: 0 0 4px;
  font-size: var(--fs-xl);
  color: var(--ink);
}
.sub {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
}

.toggle-row,
.list-head,
.actions,
.meta {
  display: flex;
  align-items: center;
}
.toggle-row,
.list-head {
  justify-content: space-between;
  gap: 16px;
}
.list-head {
  margin-bottom: 8px;
}
.list-head strong {
  font-size: var(--fs-sm);
  color: var(--ink);
}

.toggle-row {
  padding: 10px 12px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface);
  margin-bottom: 12px;
}
.toggle-row .tx {
  flex: 1;
  min-width: 0;
}
.toggle-row .tx b {
  display: block;
  font-weight: 500;
  font-size: var(--fs-md);
}
.toggle-row .tx span {
  font-size: var(--fs-xs);
  color: var(--ink-3);
}

.pill {
  flex: none;
  font-size: var(--fs-xs);
  border-radius: 999px;
  padding: 2px 10px;
  border: 1px solid var(--line);
  color: var(--ink-2);
}
.pill--ok {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--accent-soft);
}
.pill--idle {
  color: var(--ink-3);
}

.actions {
  gap: 8px;
  flex-wrap: wrap;
}

.btn {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 7px 12px;
  font: inherit;
  font-size: var(--fs-sm);
  background: transparent;
  color: var(--ink);
}
.btn:hover:not(:disabled) {
  border-color: var(--line-strong);
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}

.msg,
.empty {
  margin: 0 0 10px;
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 10px 12px;
  font-size: var(--fs-sm);
}
.msg--warn {
  color: var(--need-ink);
  border-color: var(--need);
  background: var(--need-soft);
}
.msg--err {
  color: var(--fail);
  border-color: var(--fail);
}
.msg--ok {
  color: var(--accent);
  border-color: var(--accent);
}

.muted {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}

.subscription-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}
.subscription-list li {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  background: var(--surface);
  padding: 12px;
}
.endpoint {
  color: var(--ink);
  font-size: var(--fs-sm);
  overflow-wrap: anywhere;
}
.current {
  margin-left: 8px;
  color: var(--accent);
}
.meta {
  gap: 12px;
  flex-wrap: wrap;
  margin-top: 8px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}

@media (max-width: 760px) {
  .page-head {
    flex-direction: column;
  }
  .toggle-row {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
