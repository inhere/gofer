<script setup lang="ts">
// TUN-03 / WEB-12：/settings/tunnels —— 上半「在线转发」，下半「预设」。
//
// 转发是**客户端本地监听**：`gofer tun forward` 在要用端口的那台机器上跑，每条连接经 hub
// 转到 worker。server 本机托管和客户端本地转发共用同一 Forwarder 实现。
import { computed, onMounted, onUnmounted, ref } from 'vue'
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
} from '../../api/client'
import { getMetaCached } from '../../api/metaCache'
import { fmtDateTime, fmtDuration } from '../../api/time'
import { fmtBytes } from '../../utils/bytes'
import { createPoller } from '../../utils/poller'
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
  return `最后心跳 ${fmtDuration(Math.max(0, Math.floor((nowMs.value - seen) / 1000)))} 前`
}

function trafficText(f: TunnelForwarder): string {
  return `↑ ${fmtBytes(f.bytes_up)}  ↓ ${fmtBytes(f.bytes_down)}`
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
    notice.value = resp.warning ? `已启动 ${p.name}：${resp.warning}` : `已启动托管转发 ${p.name}`
    await fetchForwarders()
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

async function stopHosted(p: TunnelPreset): Promise<void> {
  presetError.value = ''
  try {
    await stopHostedTunnel(p.name)
    notice.value = `已停止托管转发 ${p.name}`
    await fetchForwarders()
  } catch (e) {
    presetError.value = e instanceof Error ? e.message : String(e)
  }
}

async function importLocal(p: TunnelPreset): Promise<void> {
  presetError.value = ''
  try {
    await importLocalTunnelPreset(p.name)
    notice.value = `已导入本机预设 ${p.name}`
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

const editorTitle = computed(() => (editor.value?.mode === 'edit' ? '编辑预设' : '新增预设'))

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
    notice.value = `已保存预设 ${name}`
    closeEditor()
    await fetchPresets()
  } catch (e) {
    const detail = e instanceof ApiError ? e.detail || e.message : String(e)
    if (e instanceof ApiError && e.status === 400) {
      specError.value = detail
    } else if (e instanceof ApiError && e.status === 409) {
      saveError.value = `${detail}（改个名字，或在该行的「编辑」里覆盖它）`
    } else {
      saveError.value = detail
    }
  } finally {
    saving.value = false
  }
}

async function removePreset(p: TunnelPreset): Promise<void> {
  if (!window.confirm(`删除预设「${p.name}」？server 上的这条记录会被移除。`)) {
    return
  }
  notice.value = ''
  presetError.value = ''
  try {
    await deleteTunnelPreset(p.name)
    notice.value = `已删除预设 ${p.name}`
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
    notice.value = `已复制：${cmd}`
  } catch {
    notice.value = ''
    presetError.value = `复制失败，请手动执行：${cmd}`
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
    <section class="section" aria-label="在线转发">
      <header class="section-head">
        <h2 class="section-title mono">在线转发</h2>
        <span class="section-note mono">{{ forwarders.length }} 个进程</span>
        <span class="poll mono" :class="{ 'poll--on': loading }">●</span>
      </header>

      <p v-if="fwError" class="error mono" :title="fwError">在线转发读取失败：{{ fwError }}</p>

      <div v-if="forwarders.length" class="table table--forwarders">
        <div class="thead mono">
          <span>worker</span>
          <span>规则</span>
          <span>主机 / pid</span>
          <span>已运行</span>
          <span>连接</span>
          <span>流量</span>
        </div>
        <div v-for="f in forwarders" :key="f.id" class="trow">
          <span class="cell-worker mono" :title="f.id">{{ f.hosted ? 'server 托管 · ' : '' }}{{ f.worker || '—' }}</span>
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
        </div>
      </div>

      <p v-else-if="!fwError" class="empty mono">
        这里显示正在运行的 <code>gofer tun forward</code> 进程；转发是在要用端口的那台机器上本地监听的，web 不能代为启动。
      </p>
    </section>

    <!-- 下半：预设 -->
    <section class="section" aria-label="转发预设">
      <header class="section-head">
        <h2 class="section-title mono">预设</h2>
        <span class="section-note mono">{{ presets.length }} 条</span>
        <button class="mini-btn mono" type="button" @click="openCreate()">新增预设</button>
      </header>

      <p v-if="presetError" class="error mono" :title="presetError">{{ presetError }}</p>

      <div v-if="presets.length" class="table table--presets">
        <div class="thead mono">
          <span>名称</span>
          <span>worker</span>
          <span>规则</span>
          <span>备注</span>
          <span>状态</span>
          <span>更新</span>
          <span>操作</span>
        </div>
        <div v-for="p in presets" :key="p.name" class="trow">
          <span class="cell-name mono" :title="p.name">{{ p.name }}</span>
          <span class="cell-worker mono">{{ p.worker || '—' }}</span>
          <span class="cell-rules mono" :title="presetRuleText(p)">{{ presetRuleText(p) }}</span>
          <span class="cell-note" :title="p.note">{{ p.note || '—' }}</span>
          <span class="cell-state mono">
            <span v-if="hostedFor(p.name)" class="state state--on">server 托管</span>
            <span v-else-if="p.autostart" class="state">自启动</span>
            <span v-else>—</span>
          </span>
          <span class="cell-updated mono">
            <span class="upd-at">{{ fmtDateTime(Math.floor(Date.parse(p.updated_at) / 1000)) }}</span>
            <span v-if="p.updated_by" class="upd-by" :title="`最后写入的 caller：${p.updated_by}`">by {{ p.updated_by }}</span>
          </span>
          <span class="cell-act mono">
            <button class="act" type="button" title="复制 `gofer tun forward -n 名字`" @click="copyCommand(p)">
              复制启动命令
            </button>
            <button v-if="hostedFor(p.name)" class="act" type="button" @click="stopHosted(p)">停止</button>
            <button v-else class="act" type="button" @click="startHosted(p)">启动</button>
            <button class="act" type="button" @click="openEdit(p)">编辑</button>
            <button class="act act--del" type="button" @click="removePreset(p)">删</button>
          </span>
        </div>
      </div>

      <p v-else-if="!presetError" class="empty mono">
        还没有预设。预设存在 server 上，任何机器 <code>gofer tun forward -n &lt;名字&gt;</code> 都能直接用。
      </p>
    </section>

    <section class="section" aria-label="本机未上传预设">
      <header class="section-head">
        <h2 class="section-title mono">本机未上传</h2>
        <span class="section-note mono">{{ localPresets.length }} 条</span>
      </header>
      <div v-if="localPresets.length" class="table table--presets">
        <div class="thead mono"><span>名称</span><span>worker</span><span>规则</span><span>状态</span><span>操作</span></div>
        <div v-for="p in localPresets" :key="p.name" class="trow">
          <span class="cell-name mono">{{ p.name }}</span>
          <span class="cell-worker mono">{{ p.worker }}</span>
          <span class="cell-rules mono">{{ presetRuleText(p) }}</span>
          <span class="cell-state mono">仅本机文件</span>
          <span class="cell-act mono"><button class="act" type="button" @click="importLocal(p)">导入</button></span>
        </div>
      </div>
      <p v-else class="empty mono">没有发现尚未上传的本机预设。</p>
    </section>

    <!-- 预设编辑弹窗 -->
    <div v-if="editor" class="scrim" @click.self="closeEditor()">
      <div class="modal" role="dialog" aria-modal="true" aria-labelledby="preset-editor-title">
        <div class="modal-head">
          <h3 id="preset-editor-title" class="modal-title mono">{{ editorTitle }}</h3>
          <button class="mini-btn mono" type="button" :disabled="saving" @click="closeEditor()">关闭</button>
        </div>

        <div class="modal-body">
          <label class="field">
            <span class="field-name mono">名称<span class="req">*</span></span>
            <input
              v-model="editor.form.name"
              class="input mono"
              type="text"
              :disabled="editor.mode === 'edit'"
              placeholder="例如 demo（不能含空格或路径分隔符）"
            />
            <span v-if="editor.mode === 'edit'" class="field-hint mono">
              名字是主键，不能就地改；要改名就新建一条再删旧的。
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
                {{ w.connected ? '在线' : '离线' }}
              </option>
            </datalist>
            <span class="field-hint mono">
              候选来自已登记的 worker；离线的也可以先存下来。
            </span>
          </label>

          <label class="field">
            <span class="field-name mono">规则<span class="req">*</span></span>
            <textarea
              v-model="editor.form.specsText"
              class="input input--area mono"
              :class="{ 'input--bad': !!specError }"
              rows="5"
              placeholder="一行一条，或用逗号分隔：&#10;udp/21845:192.168.0.253:21845&#10;1502:192.168.0.205:502"
            ></textarea>
            <span v-if="specError" class="field-error mono">{{ specError }}</span>
            <span v-else class="field-hint mono">
              一行一条或逗号分隔都行；`[bind:]本地端口:目标主机:目标端口`，udp 写 `udp/端口`。
            </span>
          </label>

          <label class="field">
            <span class="field-name mono">备注</span>
            <input v-model="editor.form.note" class="input mono" type="text" placeholder="可选" />
          </label>

          <label class="field field--check">
            <input v-model="editor.form.autostart" type="checkbox" />
            <span class="field-name mono">server 启动时自动监听</span>
          </label>

          <p v-if="saveError" class="error mono" :title="saveError">{{ saveError }}</p>

          <div class="modal-actions">
            <button class="mini-btn mono" type="button" :disabled="saving" @click="save()">
              {{ saving ? '保存中...' : '保存' }}
            </button>
            <button class="mini-btn mono" type="button" :disabled="saving" @click="closeEditor()">取消</button>
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
  color: var(--queue);
  margin: 0;
}
.section-note {
  color: var(--queue);
  font-size: 11px;
}
.poll {
  margin-left: auto;
  color: var(--line);
  font-size: 10px;
}
.poll--on {
  color: var(--phosphor);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
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
  color: var(--queue);
  font-size: 12px;
  border: 1px dashed var(--line);
  border-radius: var(--radius);
  padding: 10px 12px;
  margin: 0;
}
.empty code {
  color: var(--phosphor);
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  grid-template-columns: 140px minmax(220px, 1fr) 170px 90px 60px 160px;
}
.table--presets .thead,
.table--presets .trow {
  grid-template-columns: 140px 130px minmax(200px, 1fr) 150px 150px 260px;
}
.thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
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
  background: var(--panel);
}
.cell-worker,
.cell-name {
  color: var(--paper);
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
  color: var(--paper);
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.table--presets .cell-rules {
  display: block;
  color: var(--paper);
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
  color: var(--queue);
  font-size: 11px;
}
.cell-age,
.cell-conn,
.cell-traffic,
.cell-updated {
  color: var(--queue);
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
  color: var(--queue);
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
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 2px 8px;
  font-size: 11px;
  font-family: var(--font-mono);
  white-space: nowrap;
}
.act:hover {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.act--del:hover {
  color: var(--fail);
  border-color: var(--fail);
}
.mini-btn {
  background: transparent;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
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
  background: var(--bg, var(--panel));
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  color: var(--queue);
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
  color: var(--queue);
}
.req {
  color: var(--fail);
}
.field-hint {
  color: var(--queue);
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
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 8px;
  font-size: 12px;
  font-family: var(--font-mono);
}
.input:focus {
  outline: none;
  border-color: var(--phosphor);
}
.input:disabled {
  color: var(--queue);
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
