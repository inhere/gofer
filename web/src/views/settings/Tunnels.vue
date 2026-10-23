<script setup lang="ts">
// TUN-03 / WEB-12：/settings/tunnels —— 上半「在线转发」，下半「预设」。
//
// 转发是**客户端本地监听**：`gofer tun forward` 在要用端口的那台机器上跑，每条连接经 hub
// 转到 worker。server 本机托管和客户端本地转发共用同一 Forwarder 实现。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useWhoami } from '../../store/whoami'
import {
  ApiError,
  deleteTunnelPreset,
  importLocalTunnelPreset,
  listLocalTunnelPresets,
  listTunnelForwarders,
  listTunnelPresets,
  putTunnelPreset,
  startHostedTunnel,
  stopHostedTunnel,
  stopTunnelForwarder,
} from '../../api/client'
import { getMetaCached } from '../../api/metaCache'
import { fmtDateTime, fmtDuration } from '../../api/time'
import { fmtBytes } from '../../utils/bytes'
import { createPoller } from '../../utils/poller'
import { forwarderStopState, forwarderStopUnsupportedHint } from '../../utils/forwarderStop'
import { t } from '../../i18n'
import type { MetaWorker, TunnelForwarder, TunnelForwarderSpec, TunnelPreset } from '../../api/types'

const POLL_MS = 5000

const forwarders = ref<TunnelForwarder[]>([])
const presets = ref<TunnelPreset[]>([])
const localPresets = ref<TunnelPreset[]>([])
// worker 候选：/v1/meta 的 workers（服务端配置里登记过的 worker id，含当前离线的）。
const workers = ref<MetaWorker[]>([])
const fwError = ref('')
const presetError = ref('')
const notice = ref('')
const loading = ref(false)
const loaded = ref(false)
// 本地时钟：两次轮询之间推进"已运行时长"，否则数字每 5s 才跳一下。
const nowMs = ref(Date.now())

// ── 在线转发 ──────────────────────────────────────────────────────────────────

async function fetchForwarders(): Promise<void> {
  try {
    const resp = await listTunnelForwarders()
    forwarders.value = resp.forwarders ?? []
    fwError.value = ''
  } catch (e) {
    // 401 已由 client 统一处理（跳登录）；其余保留上一帧，只在段落里给出错误条。
    fwError.value = e instanceof Error ? e.message : String(e)
  }
}

// 规则渲染成设计里的写法：`udp/21845 → 192.168.0.253:21845`。bind 是 spec 的本地监听
// 地址（不写时服务端按 127.0.0.1 解析）——非默认绑定要显示出来，否则"监听在哪"就丢了。
function ruleText(s: TunnelForwarderSpec): string {
  const local = s.bind && s.bind !== '127.0.0.1' ? `${s.bind}:${s.local_port}` : String(s.local_port)
  // socks 规则没有固定目标：每条连接由客户端（浏览器 / curl）给出目标。
  if (s.network === 'socks') return t('settings.tunnels.ruleSocks', { local })
  return `${s.network}/${local} → ${s.target}`
}

function uptimeText(f: TunnelForwarder): string {
  const start = Date.parse(f.started_at)
  if (!Number.isFinite(start)) {
    return '—'
  }
  return fmtDuration(Math.max(0, Math.floor((nowMs.value - start) / 1000)))
}

function heartbeatText(f: TunnelForwarder): string {
  const seen = Date.parse(f.last_seen_at)
  if (!Number.isFinite(seen)) {
    return ''
  }
  return t('settings.tunnels.heartbeat', { d: fmtDuration(Math.max(0, Math.floor((nowMs.value - seen) / 1000))) })
}

function trafficText(f: TunnelForwarder): string {
  return `↑ ${fmtBytes(f.bytes_up)}  ↓ ${fmtBytes(f.bytes_down)}`
}

// ── 远程停止外部转发进程 ──────────────────────────────────────────────────────
//
// hub 够不着那台机器上的进程，只能标记；进程下一次心跳（≤30 秒）收到 410 后自己退出，
// 条目随之从列表消失。确认放在行内（不用 window.confirm），点「停止」先变成「确认停止 / 取消」。
const confirmStopId = ref<string | null>(null)
// 按权限隐藏：托管转发 / 预设写入需要 tunnel.hosted；停别人的转发需要 tunnel.manage.others。
const { can } = useWhoami()
const stopBusyId = ref<string | null>(null)

function askStop(f: TunnelForwarder): void {
  confirmStopId.value = f.id
}

function cancelStop(): void {
  confirmStopId.value = null
}

async function confirmStop(f: TunnelForwarder): Promise<void> {
  stopBusyId.value = f.id
  fwError.value = ''
  notice.value = ''
  try {
    const resp = await stopTunnelForwarder(f.id)
    // 本地先翻成「停止中」，不必等下一轮轮询。
    forwarders.value = forwarders.value.map((x) => (x.id === f.id ? { ...x, ...resp.forwarder, stop_requested: true } : x))
    notice.value = t('settings.tunnels.stopRequested', { host: f.host || f.id })
  } catch (e) {
    fwError.value = e instanceof Error ? e.message : String(e)
  } finally {
    stopBusyId.value = null
    confirmStopId.value = null
  }
}

// ── 预设 ──────────────────────────────────────────────────────────────────────

async function fetchPresets(): Promise<void> {
  try {
    const resp = await listTunnelPresets()
    presets.value = resp.presets ?? []
    presetError.value = ''
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

async function fetchLocalPresets(): Promise<void> {
  try {
    const resp = await listLocalTunnelPresets()
    localPresets.value = resp.presets ?? []
  } catch (e) {
    if (!presetError.value) {
      presetError.value = e instanceof Error ? e.message : String(e)
    }
  }
}

async function fetchAll(): Promise<void> {
  loading.value = true
  try {
    await Promise.all([fetchForwarders(), fetchPresets(), fetchLocalPresets()])
    loaded.value = true
    nowMs.value = Date.now()
  } finally {
    loading.value = false
  }
}

function hostedFor(name: string): TunnelForwarder | undefined {
  return forwarders.value.find((f) => f.hosted && f.hosted_name === name)
}

async function startHosted(p: TunnelPreset): Promise<void> {
  presetError.value = ''
  try {
    const resp = await startHostedTunnel(p.name)
    notice.value = resp.warning
      ? t('settings.tunnels.startedWithWarning', { name: p.name, warning: resp.warning })
      : t('settings.tunnels.started', { name: p.name })
    await fetchForwarders()
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

async function stopHosted(p: TunnelPreset): Promise<void> {
  presetError.value = ''
  try {
    await stopHostedTunnel(p.name)
    notice.value = t('settings.tunnels.stopped', { name: p.name })
    await fetchForwarders()
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

async function importLocal(p: TunnelPreset): Promise<void> {
  presetError.value = ''
  try {
    await importLocalTunnelPreset(p.name)
    notice.value = t('settings.tunnels.importedLocal', { name: p.name })
    await fetchPresets()
    await fetchLocalPresets()
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

const dataPoller = createPoller(fetchAll, POLL_MS)
const clockPoller = createPoller(() => {
  nowMs.value = Date.now()
}, 1000)

onMounted(() => {
  void fetchAll()
  void loadWorkers()
  dataPoller.start()
  clockPoller.start()
})

onUnmounted(() => {
  dataPoller.stop()
  clockPoller.stop()
})

async function loadWorkers(): Promise<void> {
  try {
    const meta = await getMetaCached()
    workers.value = meta.workers ?? []
  } catch {
    // worker 候选只是下拉提示，拉不到就退化成手填（预设允许指向还没上线的 worker）。
    workers.value = []
  }
}

// ── 预设编辑弹窗 ──────────────────────────────────────────────────────────────

interface PresetForm {
  name: string
  worker: string
  specsText: string
  note: string
  autostart: boolean
}

const editor = ref<{ mode: 'create' | 'edit'; form: PresetForm } | null>(null)
const saving = ref(false)
// 表单级错误（名称非法 / 同名冲突 / 网络）与规则字段错误（400 的 spec #N）分开放：
// 后者要贴在规则文本框下面，才指得出是哪一行。
const saveError = ref('')
const specError = ref('')

const editorTitle = computed(() => (editor.value?.mode === 'edit' ? t('settings.tunnels.editorEdit') : t('settings.tunnels.editorCreate')))

function openCreate(): void {
  editor.value = { mode: 'create', form: { name: '', worker: '', specsText: '', note: '', autostart: false } }
  saveError.value = ''
  specError.value = ''
}

function openEdit(p: TunnelPreset): void {
  editor.value = {
    mode: 'edit',
    form: { name: p.name, worker: p.worker, specsText: p.specs.join('\n'), note: p.note, autostart: p.autostart },
  }
  saveError.value = ''
  specError.value = ''
}

function closeEditor(): void {
  editor.value = null
  saveError.value = ''
  specError.value = ''
}

async function save(): Promise<void> {
  const ed = editor.value
  if (!ed) {
    return
  }
  const name = ed.form.name.trim()
  // 规则文本框按**换行**拆成条目后原样发给服务端；条目里的逗号由服务端拆（TUN-04），
  // 这样 400 里的 `spec #N` 仍是用户在文本框里看到的顺序。
  const specs = ed.form.specsText
    .split('\n')
    .map((s) => s.trim())
    .filter((s) => s !== '')

  saving.value = true
  saveError.value = ''
  specError.value = ''
  try {
    await putTunnelPreset(name, {
      worker: ed.form.worker.trim(),
      specs,
      note: ed.form.note,
      autostart: ed.form.autostart,
      // 编辑即"我知道要覆盖"：服务端没有 force 会以 409 拒绝同名写入。
      force: ed.mode === 'edit',
    })
    notice.value = t('settings.tunnels.saved', { name })
    closeEditor()
    await fetchPresets()
  } catch (e) {
    const detail = e instanceof ApiError ? e.detail || e.message : String(e)
    if (e instanceof ApiError && e.status === 400) {
      specError.value = detail
    } else if (e instanceof ApiError && e.status === 409) {
      saveError.value = t('settings.tunnels.nameTaken', { detail })
    } else {
      saveError.value = detail
    }
  } finally {
    saving.value = false
  }
}

async function removePreset(p: TunnelPreset): Promise<void> {
  if (!window.confirm(t('settings.tunnels.confirmDelete', { name: p.name }))) {
    return
  }
  notice.value = ''
  presetError.value = ''
  try {
    await deleteTunnelPreset(p.name)
    notice.value = t('settings.tunnels.deleted', { name: p.name })
    await fetchPresets()
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

// 每行一个"复制启动命令"：预设存在 server 上，换一台机器复制这行就能用。
async function copyCommand(p: TunnelPreset): Promise<void> {
  const cmd = `gofer tun forward -n ${p.name}`
  try {
    await navigator.clipboard.writeText(cmd)
    notice.value = t('settings.tunnels.copied', { cmd })
  } catch {
    notice.value = ''
    presetError.value = t('settings.tunnels.copyFailed', { cmd })
  }
}

function presetRuleText(p: TunnelPreset): string {
  return p.specs.length > 0 ? p.specs.join('  ') : '—'
}
</script>

<template>
  <div class="tunnels">
    <p v-if="notice" class="notice mono">{{ notice }}</p>

    <!-- 上半：在线转发进程 -->
    <section class="section" :aria-label="t('settings.tunnels.forwarders')">
      <header class="section-head">
        <h2 class="section-title mono">{{ t('settings.tunnels.forwarders') }}</h2>
        <span class="section-note mono">{{ t('settings.tunnels.forwardersCount', { n: forwarders.length }) }}</span>
        <span class="poll mono" :class="{ 'poll--on': loading }">●</span>
      </header>

      <p v-if="fwError" class="error mono" :title="fwError">{{ t('settings.tunnels.fwLoadFailed', { error: fwError }) }}</p>

      <div v-if="forwarders.length" class="table table--forwarders">
        <div class="thead mono">
          <span>worker</span>
          <span>{{ t('settings.tunnels.col.rules') }}</span>
          <span>{{ t('settings.tunnels.col.hostPid') }}</span>
          <span>{{ t('settings.tunnels.col.age') }}</span>
          <span>{{ t('settings.tunnels.col.conn') }}</span>
          <span>{{ t('settings.tunnels.col.traffic') }}</span>
          <span>{{ t('settings.tunnels.col.actions') }}</span>
        </div>
        <div v-for="f in forwarders" :key="f.id" class="trow">
          <span class="cell-worker mono" :title="f.id">{{ f.hosted ? t('settings.tunnels.hostedPrefix') : '' }}{{ f.worker || '—' }}</span>
          <span class="cell-rules">
            <span v-for="(s, i) in f.specs" :key="i" class="rule mono">{{ ruleText(s) }}</span>
          </span>
          <span class="cell-host mono" :title="heartbeatText(f)">
            <span class="host-name">{{ f.host || '—' }}</span>
            <span class="host-pid">{{ f.pid || '—' }}</span>
          </span>
          <span class="cell-age mono" :title="fmtDateTime(Math.floor(Date.parse(f.started_at) / 1000))">
            {{ uptimeText(f) }}
          </span>
          <span class="cell-conn mono">{{ f.connections }}</span>
          <span class="cell-traffic mono">{{ trafficText(f) }}</span>
          <span class="cell-act cell-act--fw mono">
            <span v-if="forwarderStopState(f) === 'hosted'" class="act-note">{{ t('settings.tunnels.stopInPresets') }}</span>
            <span v-else-if="forwarderStopState(f) === 'stopping'" class="state state--stopping" data-test="fw-stopping">
              {{ t('settings.tunnels.stopping') }}
            </span>
            <span
              v-else-if="forwarderStopState(f) === 'stoppable' && !can('tunnel.stopOthers', f.caller_id)"
              class="act-note"
              data-test="fw-stop-forbidden"
              :title="t('settings.tunnels.stopForbiddenTitle')"
            >{{ t('settings.tunnels.stopForbidden') }}</span>
            <template v-else-if="forwarderStopState(f) === 'stoppable'">
              <template v-if="confirmStopId === f.id">
                <button
                  class="act act--del"
                  type="button"
                  data-test="fw-stop-confirm"
                  :disabled="stopBusyId === f.id"
                  @click="confirmStop(f)"
                >
                  {{ stopBusyId === f.id ? t('settings.tunnels.requesting') : t('settings.tunnels.confirmStop') }}
                </button>
                <button class="act" type="button" data-test="fw-stop-cancel" :disabled="stopBusyId === f.id" @click="cancelStop()">
                  {{ t('act.cancel') }}
                </button>
              </template>
              <button
                v-else
                class="act act--del"
                type="button"
                data-test="fw-stop"
                :title="t('settings.tunnels.stopTitle', { host: f.host || t('settings.tunnels.thatMachine') })"
                @click="askStop(f)"
              >
                {{ t('settings.tunnels.stop') }}
              </button>
            </template>
            <button
              v-else
              class="act"
              type="button"
              disabled
              data-test="fw-stop-disabled"
              :title="forwarderStopUnsupportedHint(f)"
              :aria-label="forwarderStopUnsupportedHint(f)"
            >
              {{ t('settings.tunnels.stop') }}
            </button>
          </span>
        </div>
      </div>

      <p v-else-if="!fwError" class="empty mono">
        {{ t('settings.tunnels.fwEmptyBefore') }}<code>gofer tun forward</code>{{ t('settings.tunnels.fwEmptyAfter') }}
      </p>
    </section>

    <!-- 下半：预设 -->
    <section class="section" :aria-label="t('settings.tunnels.presets')">
      <header class="section-head">
        <h2 class="section-title mono">{{ t('settings.tunnels.presets') }}</h2>
        <span class="section-note mono">{{ t('settings.tunnels.presetsCount', { n: presets.length }) }}</span>
        <button v-if="can('tunnel.hosted')" class="mini-btn mono" type="button" data-test="preset-new" @click="openCreate()">{{ t('settings.tunnels.newPreset') }}</button>
      </header>

      <p v-if="presetError" class="error mono" :title="presetError">{{ presetError }}</p>

      <div v-if="presets.length" class="table table--presets">
        <div class="thead mono">
          <span>{{ t('settings.tunnels.col.name') }}</span>
          <span>worker</span>
          <span>{{ t('settings.tunnels.col.rules') }}</span>
          <span>{{ t('settings.tunnels.col.note') }}</span>
          <span>{{ t('settings.tunnels.col.state') }}</span>
          <span>{{ t('settings.tunnels.col.updated') }}</span>
          <span>{{ t('settings.tunnels.col.actions') }}</span>
        </div>
        <div v-for="p in presets" :key="p.name" class="trow">
          <span class="cell-name mono" :title="p.name">{{ p.name }}</span>
          <span class="cell-worker mono">{{ p.worker || '—' }}</span>
          <span class="cell-rules mono" :title="presetRuleText(p)">{{ presetRuleText(p) }}</span>
          <span class="cell-note" :title="p.note">{{ p.note || '—' }}</span>
          <span class="cell-state mono">
            <span v-if="hostedFor(p.name)" class="state state--on">{{ t('settings.tunnels.hosted') }}</span>
            <span v-else-if="p.autostart" class="state">{{ t('settings.tunnels.autostart') }}</span>
            <span v-else>—</span>
          </span>
          <span class="cell-updated mono">
            <span class="upd-at">{{ fmtDateTime(Math.floor(Date.parse(p.updated_at) / 1000)) }}</span>
            <span v-if="p.updated_by" class="upd-by" :title="t('settings.tunnels.lastWriter', { by: p.updated_by })">by {{ p.updated_by }}</span>
          </span>
          <span class="cell-act mono">
            <button class="act" type="button" :title="t('settings.tunnels.copyCmdTitle')" @click="copyCommand(p)">
              {{ t('settings.tunnels.copyCmd') }}
            </button>
            <template v-if="can('tunnel.hosted')">
              <button v-if="hostedFor(p.name)" class="act" type="button" @click="stopHosted(p)">{{ t('settings.tunnels.stop') }}</button>
              <button v-else class="act" type="button" @click="startHosted(p)">{{ t('settings.tunnels.start') }}</button>
              <button class="act" type="button" @click="openEdit(p)">{{ t('settings.tunnels.edit') }}</button>
              <button class="act act--del" type="button" @click="removePreset(p)">{{ t('settings.tunnels.deleteShort') }}</button>
            </template>
          </span>
        </div>
      </div>

      <p v-else-if="!presetError" class="empty mono">
        {{ t('settings.tunnels.presetsEmptyBefore') }}<code>gofer tun forward -n &lt;名字&gt;</code>{{ t('settings.tunnels.presetsEmptyAfter') }}
      </p>
    </section>

    <section class="section" :aria-label="t('settings.tunnels.local')">
      <header class="section-head">
        <h2 class="section-title mono">{{ t('settings.tunnels.local') }}</h2>
        <span class="section-note mono">{{ t('settings.tunnels.presetsCount', { n: localPresets.length }) }}</span>
      </header>
      <div v-if="localPresets.length" class="table table--presets">
        <div class="thead mono"><span>{{ t('settings.tunnels.col.name') }}</span><span>worker</span><span>{{ t('settings.tunnels.col.rules') }}</span><span>{{ t('settings.tunnels.col.state') }}</span><span>{{ t('settings.tunnels.col.actions') }}</span></div>
        <div v-for="p in localPresets" :key="p.name" class="trow">
          <span class="cell-name mono">{{ p.name }}</span>
          <span class="cell-worker mono">{{ p.worker }}</span>
          <span class="cell-rules mono">{{ presetRuleText(p) }}</span>
          <span class="cell-state mono">{{ t('settings.tunnels.localOnly') }}</span>
          <span class="cell-act mono"><button v-if="can('tunnel.hosted')" class="act" type="button" @click="importLocal(p)">{{ t('settings.tunnels.import') }}</button></span>
        </div>
      </div>
      <p v-else class="empty mono">{{ t('settings.tunnels.localEmpty') }}</p>
    </section>

    <!-- 预设编辑弹窗 -->
    <div v-if="editor" class="scrim" @click.self="closeEditor()">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="preset-editor-title">
        <div class="modal-head">
          <h3 id="preset-editor-title" class="modal-title mono">{{ editorTitle }}</h3>
          <button class="mini-btn mono" type="button" :disabled="saving" @click="closeEditor()">{{ t('act.close') }}</button>
        </div>

        <div class="modal-body">
          <label class="field">
            <span class="field-name mono">{{ t('settings.tunnels.name') }}<span class="req">*</span></span>
            <input
              v-model="editor.form.name"
              class="input mono"
              type="text"
              :disabled="editor.mode === 'edit'"
              :placeholder="t('settings.tunnels.namePlaceholder')"
            />
            <span v-if="editor.mode === 'edit'" class="field-hint mono">
              {{ t('settings.tunnels.nameImmutable') }}
            </span>
          </label>

          <label class="field">
            <span class="field-name mono">worker<span class="req">*</span></span>
            <!-- 用 datalist 而不是 <select>：预设允许指向当前没上线的 worker（CLI 也这样），
                 严格下拉会把这种值丢掉；datalist 既是下拉候选，也仍可手填 worker id。 -->
            <input
              v-model="editor.form.worker"
              class="input mono"
              type="text"
              list="tunnel-worker-options"
              placeholder="worker id"
            />
            <datalist id="tunnel-worker-options">
              <option v-for="w in workers" :key="w.id" :value="w.id">
                {{ w.connected ? t('settings.tunnels.workerOnline') : t('settings.tunnels.workerOffline') }}
              </option>
            </datalist>
            <span class="field-hint mono">
              {{ t('settings.tunnels.workerHint') }}
            </span>
          </label>

          <label class="field">
            <span class="field-name mono">{{ t('settings.tunnels.rules') }}<span class="req">*</span></span>
            <textarea
              v-model="editor.form.specsText"
              class="input input--area mono"
              :class="{ 'input--bad': !!specError }"
              rows="5"
              :placeholder="t('settings.tunnels.rulesPlaceholder')"
            ></textarea>
            <span v-if="specError" class="field-error mono">{{ specError }}</span>
            <span v-else class="field-hint mono">
              {{ t('settings.tunnels.rulesHint') }}
            </span>
          </label>

          <label class="field">
            <span class="field-name mono">{{ t('settings.tunnels.note') }}</span>
            <input v-model="editor.form.note" class="input mono" type="text" :placeholder="t('settings.tunnels.optional')" />
          </label>

          <label class="field field--check">
            <input v-model="editor.form.autostart" type="checkbox" />
            <span class="field-name mono">{{ t('settings.tunnels.autostartToggle') }}</span>
          </label>

          <p v-if="saveError" class="error mono" :title="saveError">{{ saveError }}</p>

          <div class="modal-actions">
            <button class="mini-btn mono" type="button" :disabled="saving" @click="save()">
              {{ saving ? t('settings.ext.saving') : t('act.save') }}
            </button>
            <button class="mini-btn mono" type="button" :disabled="saving" @click="closeEditor()">{{ t('act.cancel') }}</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tunnels {
  min-width: 0;
}
.section {
  margin-bottom: 22px;
}
.section-head {
  display: flex;
  align-items: baseline;
  gap: 10px;
  margin-bottom: 12px;
}
.section-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--ink-3);
  margin: 0;
}
.section-note {
  color: var(--ink-3);
  font-size: 11px;
}
.poll {
  margin-left: auto;
  color: var(--line);
  font-size: 10px;
}
.poll--on {
  color: var(--accent);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}
.notice {
  color: var(--done);
  font-size: 12px;
  margin: 0 0 12px;
}
.empty {
  color: var(--ink-3);
  font-size: 12px;
  border: 1px dashed var(--line);
  border-radius: var(--r-chip);
  padding: 10px 12px;
  margin: 0;
}
.empty code {
  color: var(--accent);
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.table--forwarders .thead,
.table--forwarders .trow {
  grid-template-columns: 140px minmax(220px, 1fr) 170px 90px 60px 160px 150px;
}
.table--presets .thead,
.table--presets .trow {
  grid-template-columns: 140px 130px minmax(200px, 1fr) 150px 150px 260px;
}
.thead {
  background: var(--surface);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 12px;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--surface);
}
.cell-worker,
.cell-name {
  color: var(--ink);
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-rules {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.rule {
  color: var(--ink);
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.table--presets .cell-rules {
  display: block;
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-host {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.host-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.host-pid {
  color: var(--ink-3);
  font-size: 11px;
}
.cell-age,
.cell-conn,
.cell-traffic,
.cell-updated {
  color: var(--ink-3);
}
.cell-updated {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.upd-at {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.upd-by {
  font-size: 10px;
  opacity: 0.8;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-note {
  color: var(--ink-3);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-act {
  display: flex;
  gap: 6px;
}
.act {
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 2px 8px;
  font-size: 11px;
  font-family: var(--font-mono);
  white-space: nowrap;
}
.act:hover {
  color: var(--accent);
  border-color: var(--accent);
}
.act--del:hover:not(:disabled) {
  color: var(--fail);
  border-color: var(--fail);
}
.act:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.act:disabled:hover {
  color: var(--ink-3);
  border-color: var(--line);
}
.cell-act--fw {
  flex-wrap: wrap;
  align-items: center;
  min-width: 0;
}
.state--stopping {
  color: var(--run);
  font-size: 11px;
}
.act-note {
  color: var(--ink-3);
  font-size: 11px;
}
.mini-btn {
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.mini-btn:disabled {
  opacity: 0.5;
}

/* 编辑弹窗 */
.scrim {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: flex-start;
  justify-content: center;
  padding: 40px 16px;
  overflow: auto;
}
.modal {
  width: min(560px, 100%);
  background: var(--bg, var(--surface));
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
}
.modal-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid var(--line);
}
.modal-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--ink-3);
  margin: 0;
}
.modal-head .mini-btn {
  margin-left: auto;
}
.modal-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 14px;
}
.modal-actions {
  display: flex;
  gap: 8px;
  justify-content: flex-end;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
}
.field-name {
  color: var(--ink-3);
}
.req {
  color: var(--fail);
}
.field-hint {
  color: var(--ink-3);
  font-size: 10px;
  opacity: 0.8;
}
.field-error {
  color: var(--fail);
  font-size: 11px;
  word-break: break-word;
}
.input {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 5px 8px;
  font-size: 12px;
  font-family: var(--font-mono);
}
.input:focus {
  outline: none;
  border-color: var(--accent);
}
.input:disabled {
  color: var(--ink-3);
  opacity: 0.7;
}
/* 规则校验失败（400 spec #N）时把文本框本身标红——错误文案就在它下面。 */
.input--bad {
  border-color: var(--fail);
}
.input--area {
  resize: vertical;
  min-height: 84px;
}

/* 窄屏：表格退化为单列卡片（每行一条），列头隐藏。 */
@media (max-width: 900px) {
  .thead {
    display: none;
  }
  .table--forwarders .trow,
  .table--presets .trow {
    grid-template-columns: minmax(0, 1fr);
    gap: 6px;
  }
}
</style>
