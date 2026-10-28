<script setup lang="ts">
// 设置 · 通知（设计稿 §7.4）：三个频道各一节 —— 本设备推送（浏览器 Web Push）、IM 机器人
// （server.notification.webhooks）、桌面通知（server.notification.desktop，弹在 server 所在
// 机器上）。
// 本设备推送复用既有接口：GET/POST/DELETE /v1/push/subscriptions、GET /v1/push/vapid-public-key、
// POST /v1/push/test。另两节读 GET /v1/config，写回仍走 PUT /v1/config/server 的补丁式写入
// （body 里只带被编辑的成员，secret_env 名字读不到 → 留空即保留）；事件目录来自
// GET /v1/notify/events，测试投递走 POST /v1/notify/test。
import { computed, onMounted, reactive, ref } from 'vue'
import { ApiError, getConfig, putConfigServer } from '../../api/client'
import { listNotifyEvents, sendNotifyTest } from '../../api/notify'
import {
  createPushSubscription,
  deletePushSubscription,
  getVAPIDPublicKey,
  listPushSubscriptions,
  sendTestPush,
} from '../../api/push'
import type {
  ConfigView,
  NotifyEventsResp,
  NotifyTestResp,
  PushSubscriptionView,
} from '../../api/types'
import { botPolicy, globalForm, globalPatch, hostsWithBot, testResultMessage, type GlobalNotifyForm } from './notifyPolicy'
import { t } from '../../i18n'
import NotifyEventsPicker from '../../components/NotifyEventsPicker.vue'
import NotifyProjectsField from '../../components/NotifyProjectsField.vue'
import Drawer from '../../ui/Drawer.vue'
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

// ---- IM 机器人 / 桌面通知 --------------------------------------------------------------

interface BotForm {
  url: string
  urlRedacted: string
  kind: string
  keyword: string
  secretEnv: string
  secretSet: boolean
  clearSecret: boolean
  events: string[]
  projects: string
  enabled: boolean
}

interface DesktopForm {
  enabled: boolean
  events: string[]
  projects: string
  mergeSec: string
}

const config = ref<ConfigView | null>(null)
const catalog = ref<NotifyEventsResp | null>(null)
const bots = ref<BotForm[]>([])
const botsSaving = ref(false)
const globalNotify = reactive<GlobalNotifyForm>(globalForm())
const globalSaving = ref(false)
const addBotToAllowlist = ref(true)
const drawerOpen = ref(false)
const editingIndex = ref<number | null>(null)
const draft = reactive<BotForm>(emptyBot())
// 桌面通知与 IM 机器人同一套编辑方式：页面只显示摘要（开关 / 事件数 / 项目 / 合并窗口），
// 点「编辑」打开同样宽度的侧栏改草稿，保存成功后再重读配置（摘要永远是已保存的值）。
const desktopSaving = ref(false)
const desktopEnabled = ref(false)
const desktopEvents = ref<string[]>([])
const desktopProjects = ref('')
const desktopMergeSec = ref('')
const desktopOpen = ref(false)
const desktopDraft = reactive<DesktopForm>(emptyDesktop())
const rowResult = reactive<Record<number, NotifyTestResp>>({})
const testing = ref('')

function emptyDesktop(): DesktopForm {
  return { enabled: false, events: [], projects: '', mergeSec: '' }
}

function emptyBot(): BotForm {
  return {
    url: '',
    urlRedacted: '',
    kind: '',
    keyword: '',
    secretEnv: '',
    secretSet: false,
    clearSecret: false,
    events: [],
    projects: '',
    enabled: true,
  }
}

// defaultEvents：新机器人的预选事件集（后端给的 webhook 默认集）；桌面频道留空同样
// 表示「用默认事件集」，所以打开侧栏时不替用户勾死任何事件。
function defaultEvents(): string[] {
  return (catalog.value?.events ?? [])
    .filter((e) => e.default)
    .map((e) => e.key)
}

function botFromView(w: {
  url: string
  url_redacted?: string
  kind: string
  keyword: string
  secret_set: boolean
  events: string[]
  projects: string[]
  enabled: boolean
}): BotForm {
  return {
    url: w.url,
    urlRedacted: w.url_redacted || w.url,
    kind: w.kind ?? '',
    keyword: w.keyword ?? '',
    secretEnv: '',
    secretSet: w.secret_set,
    clearSecret: false,
    events: [...(w.events ?? [])],
    projects: (w.projects ?? []).join(', '),
    enabled: w.enabled ?? true,
  }
}

function splitList(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}

// ---- 桌面通知摘要 ----------------------------------------------------------------------
// 摘要只读已保存的值（侧栏里的是草稿）：事件 0 个 = 后端默认事件集，项目留空 = 全部项目，
// 合并窗口留空 = 服务端默认。
const desktopEventsLabel = computed(() =>
  desktopEvents.value.length === 0
    ? t('settings.notify.eventsDefault')
    : t('settings.notify.desktopEventsCount', { n: desktopEvents.value.length }),
)
const desktopProjectsLabel = computed(() =>
  t('settings.notify.botProjectsLabel', {
    projects: desktopProjects.value.trim() || t('settings.notify.desktopAllProjects'),
  }),
)
const desktopMergeLabel = computed(() =>
  t('settings.notify.desktopMergeSec', {
    sec: desktopMergeSec.value.trim() || String(catalog.value?.desktop.merge_sec_default ?? 10),
  }),
)

async function loadNotify(): Promise<void> {
  try {
    const [cfg, cat] = await Promise.all([getConfig(), listNotifyEvents()])
    config.value = cfg
    catalog.value = cat
    const n = cfg.server.notification
    Object.assign(globalNotify, globalForm(n))
    bots.value = (n?.webhooks ?? []).map(botFromView)
    const d = n?.desktop
    desktopEnabled.value = d?.enabled ?? false
    desktopEvents.value = [...(d?.events ?? [])]
    desktopProjects.value = (d?.projects ?? []).join(', ')
    desktopMergeSec.value = d ? String(d.merge_sec) : ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  }
}

function openAdd(): void {
  Object.assign(draft, emptyBot(), { events: defaultEvents() })
  addBotToAllowlist.value = true
  editingIndex.value = null
  drawerOpen.value = true
}

// openDesktop：侧栏草稿从已保存的值复制（取消 = 丢弃草稿，摘要不受影响）。
function openDesktop(): void {
  Object.assign(desktopDraft, {
    enabled: desktopEnabled.value,
    events: [...desktopEvents.value],
    projects: desktopProjects.value,
    mergeSec: desktopMergeSec.value,
  })
  desktopOpen.value = true
}

function openEdit(index: number): void {
  Object.assign(draft, bots.value[index] ?? emptyBot(), { secretEnv: '', clearSecret: false })
  const policy = botPolicy(draft.url, globalNotify.allowHosts, globalNotify.allowHttp)
  addBotToAllowlist.value = policy.missing
  editingIndex.value = index
  drawerOpen.value = true
}

function botWarning() {
  return botPolicy(draft.url, globalNotify.allowHosts, globalNotify.allowHttp)
}

async function saveGlobalNotify(): Promise<void> {
  if (globalSaving.value) return
  globalSaving.value = true
  error.value = ''
  try {
    await putConfigServer(globalPatch(globalNotify))
    notice.value = t('settings.notify.saved')
    await loadNotify()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    globalSaving.value = false
  }
}

function saveDraft(): void {
  if (draft.url.trim() === '') {
    error.value = t('settings.notify.botUrlRequired')
    return
  }
  const next: BotForm = {
    ...draft,
    url: draft.url.trim(),
    urlRedacted: draft.urlRedacted || draft.url.trim(),
    keyword: draft.keyword.trim(),
    events: [...draft.events],
    projects: draft.projects.trim(),
  }
  const policy = botPolicy(next.url, globalNotify.allowHosts, globalNotify.allowHttp)
  if (addBotToAllowlist.value && policy.host && policy.missing) {
    globalNotify.allowHosts = hostsWithBot(globalNotify.allowHosts, policy.host)
  }
  if (editingIndex.value === null) {
    bots.value.push(next)
  } else {
    bots.value.splice(editingIndex.value, 1, next)
  }
  drawerOpen.value = false
  void saveBots()
}

function removeBot(index: number): void {
  bots.value.splice(index, 1)
  delete rowResult[index]
  void saveBots()
}

// saveBots 把整张 webhook 表写回（配置写是整表替换）：secret_env 只在操作员**填了新名字**或
// 显式勾了清除时才发，否则省略 → 服务端按 URL 继承已配置的名字（读路径不回声名字）。
async function saveBots(): Promise<void> {
  if (botsSaving.value) return
  botsSaving.value = true
  error.value = ''
  notice.value = ''
  try {
    await putConfigServer({
      notification: {
        enabled: globalNotify.enabled,
        allow_hosts: globalNotify.allowHosts,
        allow_http: globalNotify.allowHttp,
        max_attempts: Number(globalNotify.maxAttempts) || 0,
        max_text_runes: Number(globalNotify.maxTextRunes) || 0,
        session_reply_delay_sec: Number(globalNotify.sessionReplyDelaySec) || 120,
        webhooks: bots.value.map((b) => {
          const row: Record<string, unknown> = {
            url: b.url,
            kind: b.kind,
            keyword: b.keyword,
            events: b.events,
            projects: splitList(b.projects),
            enabled: b.enabled,
          }
          if (b.secretEnv.trim() !== '') {
            row.secret_env = b.secretEnv.trim()
          } else if (b.clearSecret) {
            row.secret_env = ''
          }
          return row
        }),
      },
    })
    notice.value = t('settings.notify.saved')
    await loadNotify()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    botsSaving.value = false
  }
}

// saveDesktop 写回桌面频道（config/server 的补丁式写入：body 里只带 desktop 成员）。
// secret 无关，事件留空 = 默认事件集，merge_sec 留空 = 服务端默认。
async function saveDesktop(): Promise<void> {
  if (desktopSaving.value) return
  desktopSaving.value = true
  error.value = ''
  notice.value = ''
  try {
    const merge = desktopDraft.mergeSec.trim()
    await putConfigServer({
      notification: {
        desktop: {
          enabled: desktopDraft.enabled,
          events: [...desktopDraft.events],
          projects: splitList(desktopDraft.projects),
          merge_sec: merge === '' ? undefined : Number(merge),
        },
      },
    })
    notice.value = t('settings.notify.saved')
    desktopOpen.value = false
    await loadNotify()
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    desktopSaving.value = false
  }
}

async function test(target: 'webhook' | 'desktop', index = 0): Promise<void> {
  const key = `${target}:${index}`
  if (testing.value === key) return
  testing.value = key
  error.value = ''
  notice.value = ''
  try {
    const res = await sendNotifyTest(target, index)
    if (target === 'webhook') {
      rowResult[index] = res
      if (!res.ok && res.code === 'host_not_allowed') {
        error.value = testResultMessage(res, t)
      }
    } else if (res.ok) {
      notice.value = t('settings.notify.testDesktopOk')
    } else if (res.skipped) {
      error.value = t('settings.notify.testDesktopSkipped', { reason: res.error ?? '' })
    } else {
      error.value = t('settings.notify.testFailed', { error: res.error ?? '' })
    }
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    testing.value = ''
  }
}

// ---- 本设备推送 -------------------------------------------------------------------------

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

function eventsLabel(list: string[]): string {
  if (list.length === 0) {
    return t('settings.notify.eventsDefault')
  }
  return list.join(', ')
}

onMounted(() => {
  void load()
  void loadNotify()
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
    <p v-if="error" class="msg msg--err" data-testid="notify-error">{{ error }}</p>
    <p v-if="notice" class="msg msg--ok" data-testid="notify-notice">{{ notice }}</p>

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

    <FormSection :title="t('settings.notify.globalTitle')" :desc="t('settings.notify.globalDesc')">
      <div class="global-grid">
        <label class="field field--check"><input v-model="globalNotify.enabled" type="checkbox" data-testid="notify-enabled" /><span>{{ t('settings.notify.globalEnabled') }}</span></label>
        <label class="field"><span class="field-name">{{ t('settings.notify.allowHosts') }}</span><textarea :value="globalNotify.allowHosts.join('\n')" @input="globalNotify.allowHosts = ($event.target as HTMLTextAreaElement).value.split(/\s+/).filter(Boolean)" class="input textarea" data-testid="notify-allow-hosts" rows="3" /></label>
        <label class="field field--check"><input v-model="globalNotify.allowHttp" type="checkbox" data-testid="notify-allow-http" /><span>{{ t('settings.notify.allowHttp') }}</span></label>
        <label class="field"><span class="field-name">{{ t('settings.notify.maxAttempts') }}</span><input v-model="globalNotify.maxAttempts" class="input" data-testid="notify-max-attempts" /></label>
        <label class="field"><span class="field-name">{{ t('settings.notify.maxTextRunes') }}</span><input v-model="globalNotify.maxTextRunes" class="input" data-testid="notify-max-text-runes" /></label>
        <label class="field"><span class="field-name">{{ t('settings.notify.sessionReplyDelay') }}</span><input v-model="globalNotify.sessionReplyDelaySec" class="input" data-testid="notify-session-delay" /></label>
      </div>
      <button class="btn btn--primary" type="button" data-testid="notify-global-save" :disabled="globalSaving" @click="saveGlobalNotify">{{ t('act.save') }}</button>
    </FormSection>

    <FormSection :title="t('settings.notify.imTitle')" :desc="t('settings.notify.imDesc')">
      <div class="list-head">
        <strong>{{ t('settings.notify.imList') }}</strong>
        <button class="btn btn--primary" type="button" data-testid="bot-add" @click="openAdd">
          {{ t('settings.notify.botAdd') }}
        </button>
      </div>
      <p v-if="bots.length === 0" class="empty" data-testid="bot-empty">
        {{ t('settings.notify.imEmpty') }}
      </p>
      <ul v-else class="subscription-list">
        <li v-for="(b, i) in bots" :key="`${b.url}:${i}`" data-testid="bot-row">
          <div class="row-head">
            <span class="endpoint mono" :title="b.url">{{ b.urlRedacted || b.url }}</span>
            <span class="pill" :class="b.enabled ? 'pill--ok' : 'pill--idle'">
              {{ b.enabled ? t('settings.notify.on') : t('settings.notify.off') }}
            </span>
          </div>
          <div class="meta">
            <span>{{ t('settings.notify.botKindLabel', { kind: b.kind || 'generic' }) }}</span>
            <span v-if="b.keyword">{{ t('settings.notify.botKeywordLabel', { kw: b.keyword }) }}</span>
            <span v-if="b.secretSet">{{ t('settings.notify.botSecretSet') }}</span>
            <span>{{ t('settings.notify.botEventsLabel', { events: eventsLabel(b.events) }) }}</span>
            <span v-if="b.projects">
              {{ t('settings.notify.botProjectsLabel', { projects: b.projects }) }}
            </span>
          </div>
          <p v-if="rowResult[i]" class="hint" :class="rowResult[i].ok ? 'hint--ok' : 'hint--bad'" data-testid="bot-test-result">
            {{ rowResult[i].ok ? t('settings.notify.testWebhookOk') : t('settings.notify.testFailed', { error: rowResult[i].error ?? '' }) }}
          </p>
          <div class="actions">
            <button class="btn" type="button" data-testid="bot-test" :disabled="testing === `webhook:${i}`" @click="test('webhook', i)">
              {{ t('settings.notify.testSend') }}
            </button>
            <button class="btn" type="button" data-testid="bot-edit" @click="openEdit(i)">
              {{ t('settings.notify.botEdit') }}
            </button>
            <button class="btn" type="button" data-testid="bot-remove" :disabled="botsSaving" @click="removeBot(i)">
              {{ t('settings.notify.botRemove') }}
            </button>
          </div>
        </li>
      </ul>
    </FormSection>

    <FormSection :title="t('settings.notify.desktopTitle')" :desc="t('settings.notify.desktopDesc')">
      <p class="hint" data-testid="desktop-platform">
        {{ catalog?.desktop.supported ? t('settings.notify.desktopSupported') : t('settings.notify.desktopUnsupported') }}
      </p>
      <!-- 与 IM 机器人同一套：页面只显示摘要，编辑在同样宽度的侧栏里（openDesktop）。 -->
      <div class="toggle-row">
        <div class="tx">
          <b>{{ t('settings.notify.desktopToggle') }}</b>
          <span>{{ t('settings.notify.desktopMachine') }}</span>
        </div>
        <span class="pill" :class="desktopEnabled ? 'pill--ok' : 'pill--idle'" data-testid="desktop-state">
          {{ desktopEnabled ? t('settings.notify.on') : t('settings.notify.off') }}
        </span>
      </div>
      <div class="desktop-summary" data-testid="desktop-summary">
        <span data-testid="desktop-events">{{ desktopEventsLabel }}</span>
        <span data-testid="desktop-projects">{{ desktopProjectsLabel }}</span>
        <span data-testid="desktop-merge">{{ desktopMergeLabel }}</span>
      </div>
      <div class="actions">
        <button class="btn btn--primary" type="button" data-testid="desktop-edit" @click="openDesktop">
          {{ t('settings.notify.desktopEdit') }}
        </button>
        <button class="btn" type="button" data-testid="desktop-test" :disabled="testing === 'desktop:0'" @click="test('desktop')">
          {{ t('settings.notify.testSend') }}
        </button>
      </div>
    </FormSection>

    <Drawer v-model="desktopOpen" :label="t('settings.notify.desktopDrawerTitle')" width="clamp(560px, 46vw, 900px)">
      <div class="bot-form">
        <label class="field field--check">
          <input v-model="desktopDraft.enabled" type="checkbox" data-testid="desktop-enabled" />
          <span>{{ t('settings.notify.desktopToggle') }}</span>
        </label>
        <p class="hint">{{ t('settings.notify.desktopMachine') }}</p>
        <NotifyEventsPicker v-model="desktopDraft.events" :catalog="catalog" />
        <NotifyProjectsField v-model="desktopDraft.projects" test-id="desktop-projects" />
        <label class="field">
          <span class="field-name">{{ t('settings.notify.mergeSec') }}</span>
          <input
            v-model="desktopDraft.mergeSec"
            class="input"
            data-testid="desktop-merge"
            :placeholder="String(catalog?.desktop.merge_sec_default ?? 10)"
          />
        </label>
        <div class="actions">
          <button class="btn btn--primary" type="button" data-testid="desktop-save" :disabled="desktopSaving" @click="saveDesktop">
            {{ t('act.save') }}
          </button>
          <button class="btn" type="button" data-testid="desktop-cancel" @click="desktopOpen = false">
            {{ t('act.cancel') }}
          </button>
        </div>
      </div>
    </Drawer>

    <Drawer v-model="drawerOpen" :label="t('settings.notify.botDrawerTitle')" width="clamp(560px, 46vw, 900px)">
      <div class="bot-form">
        <label class="field">
          <span class="field-name">{{ t('settings.notify.botType') }}</span>
          <select v-model="draft.kind" class="input" data-testid="bot-kind">
            <option value="">{{ t('settings.notify.kindGeneric') }}</option>
            <option value="dingtalk">{{ t('settings.notify.kindDingtalk') }}</option>
            <option value="feishu">{{ t('settings.notify.kindFeishu') }}</option>
          </select>
        </label>
        <label class="field">
          <span class="field-name">{{ t('settings.notify.botUrl') }}</span>
          <input v-model="draft.url" class="input" data-testid="bot-url" placeholder="https://oapi.dingtalk.com/robot/send?access_token=…" />
        </label>
        <p v-if="botWarning().missing || botWarning().httpBlocked" class="msg msg--warn" data-testid="bot-host-warning">
          {{ botWarning().httpBlocked ? t('settings.notify.httpBlocked') : t('settings.notify.hostNotAllowed', { host: botWarning().host }) }}
        </p>
        <label v-if="botWarning().missing" class="field field--check">
          <input v-model="addBotToAllowlist" type="checkbox" data-testid="bot-add-host" />
          <span>{{ t('settings.notify.addHost', { host: botWarning().host }) }}</span>
        </label>
        <label class="field">
          <span class="field-name">{{ t('settings.notify.botKeyword') }}</span>
          <input v-model="draft.keyword" class="input" data-testid="bot-keyword" placeholder="gofer" />
          <span class="hint">{{ t('settings.notify.botKeywordHint') }}</span>
        </label>
        <label class="field">
          <span class="field-name">{{ t('settings.notify.botSecret') }}</span>
          <input
            v-model="draft.secretEnv"
            class="input"
            data-testid="bot-secret-env"
            placeholder="GOFER_WEBHOOK_SECRET"
          />
          <span class="hint">
            {{ draft.secretSet ? t('settings.notify.botSecretKept') : t('settings.notify.botSecretHint') }}
          </span>
        </label>
        <label v-if="draft.secretSet" class="field field--check">
          <input v-model="draft.clearSecret" type="checkbox" data-testid="bot-clear-secret" />
          <span>{{ t('settings.notify.botClearSecret') }}</span>
        </label>
        <NotifyProjectsField v-model="draft.projects" test-id="bot-projects" />
        <NotifyEventsPicker v-model="draft.events" :catalog="catalog" />
        <label class="field field--check">
          <input v-model="draft.enabled" type="checkbox" data-testid="bot-enabled" />
          <span>{{ t('settings.notify.botEnabled') }}</span>
        </label>
        <div class="actions">
          <button class="btn btn--primary" type="button" data-testid="bot-save" @click="saveDraft">
            {{ t('act.save') }}
          </button>
          <button class="btn" type="button" @click="drawerOpen = false">{{ t('act.cancel') }}</button>
        </div>
      </div>
    </Drawer>
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
.meta,
.row-head {
  display: flex;
  align-items: center;
}
.toggle-row,
.list-head,
.row-head {
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
/* 桌面通知摘要：开关状态、已选事件数、项目、合并窗口一行排开（编辑在侧栏）。 */
.desktop-summary {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 12px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
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
.hint {
  margin: 0 0 8px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.hint--ok {
  color: var(--accent);
}
.hint--bad {
  color: var(--fail);
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
.field {
  display: grid;
  gap: 4px;
  margin-bottom: 10px;
}
.field-name {
  font-size: var(--fs-xs);
  color: var(--ink-3);
}
.field--check {
  display: flex;
  align-items: center;
  gap: 6px;
}
.input {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 7px 10px;
  font: inherit;
  font-size: var(--fs-sm);
  background: var(--surface);
  color: var(--ink);
}
/* 事件多选（.ev-*）在 components/NotifyEventsPicker.vue 里，页面与两个侧栏共用同一份。 */
.bot-form {
  display: grid;
  gap: 4px;
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
