<script setup lang="ts">
// WEB-12 关于页：server 的版本 / 地址 / 运行时长等自述信息，全部来自 GET /v1/stats。
//
// 只显示服务端**确实有**的字段（设计 §一：没有的就不显示那一行，不为了这一页加后端）：
//  - version：buildinfo.DisplayVersion()，构建时由 LDFLAGS 注入的 "vX.Y.Z (commit7)"，
//    所以版本与 commit 是同一行（没注入 commit 时就是纯版本号）。
//  - uptime_sec / server_time：进程运行时长与服务器当前时间。
//  - 服务器地址：控制台由 server 自己服务（请求走同源相对路径），当前页面的 origin 就是
//    控制台正在对话的那个地址。
//  - 构建时间 / worker wire 协议版本：/v1/stats 没有这两个字段，故不渲染（本期不改后端）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { applyUpdate, cancelUpdateWait, checkUpdate, getStats, getUpdateStatus } from '../../api/client'
import { fmtAgo, fmtDateTime, fmtDuration } from '../../api/time'
import type { Stats, UpdateApply, UpdateStatus } from '../../api/types'
import UpdateApplyDialog from '../../components/UpdateApplyDialog.vue'
import { useWhoami } from '../../store/whoami'
import { noteUpdateStatus } from '../../store/updateAvailable'

const stats = ref<Stats | null>(null)
const error = ref('')
const loading = ref(false)
const loaded = ref(false)

const serverAddr = window.location.origin

const version = computed(() => stats.value?.version || '未知（未注入构建版本）')
const uptime = computed(() => {
  const sec = stats.value?.uptime_sec
  return sec && sec > 0 ? fmtDuration(sec) : '—'
})
// server_time 是 Unix 毫秒（stats_handler.nowMillis），fmtDateTime 收秒。
const serverTime = computed(() => {
  const ms = stats.value?.server_time
  return ms ? fmtDateTime(Math.floor(ms / 1000)) : '—'
})

// 检查更新（gofer-6x2n）：server 每隔一段时间（默认 2h）自己检查一次，这里读它最近的结果；
// 「检查更新」立即查一次。一键更新（gofer-29gi）只对以服务方式运行的 server 开放
// （managed）：server 下载新版本替换自己，重启后再升级勾选的 worker；进度轮询 apply。
const update = ref<UpdateStatus | null>(null)
const updateError = ref('')
const checking = ref(false)
const sourceLabel = computed(() => (update.value?.source === 'mirror' ? '内部 mirror' : 'GitHub'))
const checkedAt = computed(() => {
  const at = update.value?.checked_at
  return at ? `${fmtDateTime(at)}（${fmtAgo(at)}）` : '还没检查过'
})

function setUpdate(st: UpdateStatus): void {
  update.value = st
  noteUpdateStatus(st)
  if (st.apply) apply.value = st.apply
}

async function loadUpdate(): Promise<void> {
  updateError.value = ''
  try {
    setUpdate(await getUpdateStatus())
  } catch (e) {
    updateError.value = e instanceof Error ? e.message : String(e)
  }
}

// 一键更新进度。server 重启期间请求会失败，这时显示「重启中」并继续轮询。
// 一键更新需要 update.apply；没有就不摆入口（检查新版本仍可用）。
const { can } = useWhoami()
const apply = ref<UpdateApply | null>(null)
const showApply = ref(false)
const applying = ref(false)
const applyError = ref('')
const unreachable = ref(false)
let pollTimer: ReturnType<typeof setTimeout> | null = null

const activePhases = ['downloading', 'waiting_idle', 'restarting', 'upgrading_workers']
const applyActive = computed(() => !!apply.value && activePhases.includes(apply.value.phase))
const phaseLabel: Record<string, string> = {
  downloading: '下载并校验新版本',
  waiting_idle: '等运行中的 job 结束',
  restarting: '重启 server',
  upgrading_workers: 'server 已更新，正在升级 worker',
  done: '完成',
  failed: '失败',
  cancelled: '已取消',
}
// gofer-dzv3：等空闲时展示的两个数字——还在跑的 job（挡住重启的）与已经停住的新 job。
const waitingIdle = computed(() => apply.value?.phase === 'waiting_idle')
const waitingRows = computed(() => (waitingIdle.value ? (apply.value?.waiting ?? []) : []))
const waitDeadlineText = computed(() => {
  const at = apply.value?.wait_deadline
  if (!at) return ''
  const left = at - Math.floor(Date.now() / 1000)
  return `${fmtDateTime(at)}（${left > 0 ? '还剩 ' + fmtDuration(left) : '即将放弃'}）`
})
const cancelling = ref(false)
const cancelError = ref('')

// 取消等待（gofer-dzv3）：停住的 job 立即放行，apply 记为 cancelled（已经进入重启时 409）。
async function cancelWait(): Promise<void> {
  cancelling.value = true
  cancelError.value = ''
  try {
    await cancelUpdateWait()
    schedulePoll()
  } catch (e) {
    cancelError.value = e instanceof Error ? e.message : String(e)
  } finally {
    cancelling.value = false
  }
}
const workerStateLabel: Record<string, string> = {
  pending: '等待',
  accepted: '已交接，升级中',
  succeeded: '已升级',
  failed: '失败',
  skipped: '跳过',
}

function schedulePoll(): void {
  if (pollTimer) clearTimeout(pollTimer)
  pollTimer = setTimeout(() => void poll(), 3000)
}

async function poll(): Promise<void> {
  try {
    setUpdate(await getUpdateStatus())
    unreachable.value = false
  } catch {
    unreachable.value = true
  }
  const workersPending = apply.value?.workers.some((w) => w.state === 'pending' || w.state === 'accepted')
  if (applyActive.value || unreachable.value || (apply.value?.phase === 'done' && workersPending)) {
    schedulePoll()
  } else {
    void load() // 版本号变了
  }
}

async function startApply(workers: string[], waitIdleSec: number): Promise<void> {
  applying.value = true
  applyError.value = ''
  try {
    apply.value = await applyUpdate(workers, waitIdleSec)
    showApply.value = false
    schedulePoll()
  } catch (e) {
    applyError.value = e instanceof Error ? e.message : String(e)
  } finally {
    applying.value = false
  }
}

async function runCheck(): Promise<void> {
  checking.value = true
  updateError.value = ''
  try {
    setUpdate(await checkUpdate())
  } catch (e) {
    updateError.value = e instanceof Error ? e.message : String(e)
  } finally {
    checking.value = false
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = ''
  try {
    stats.value = await getStats()
    loaded.value = true
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  void load()
  await loadUpdate()
  if (applyActive.value) schedulePoll()
})
onUnmounted(() => {
  if (pollTimer) clearTimeout(pollTimer)
})
</script>

<template>
  <div class="about">
    <p v-if="error" class="error mono" :title="error">读取服务端信息失败：{{ error }}</p>

    <div class="facts">
      <div class="facts-head">
        <h2 class="facts-title mono">服务端 / SERVER</h2>
        <button class="mini-btn mono" type="button" :disabled="loading" @click="load()">
          {{ loading ? '刷新中...' : '刷新' }}
        </button>
      </div>

      <dl class="facts-grid mono">
        <dt>版本</dt>
        <dd class="brk">{{ version }}</dd>

        <dt>服务器地址</dt>
        <dd class="brk">{{ serverAddr }}</dd>

        <dt>运行时长</dt>
        <dd>{{ uptime }}</dd>

        <dt>服务器时间</dt>
        <dd>{{ serverTime }}</dd>
      </dl>

      <p v-if="!loaded && !error" class="placeholder mono">读取中...</p>
    </div>

    <div class="facts update">
      <div class="facts-head">
        <h2 class="facts-title mono">更新 / UPDATE</h2>
        <button class="mini-btn mono" type="button" :disabled="checking" @click="runCheck()">
          {{ checking ? '检查中...' : '检查更新' }}
        </button>
      </div>

      <p v-if="updateError" class="error mono" :title="updateError">{{ updateError }}</p>

      <dl v-if="update" class="facts-grid mono">
        <dt>最新版本</dt>
        <dd class="brk">
          <template v-if="update.latest">
            {{ update.latest }}
            <span v-if="update.newer" class="badge badge--new">有新版本</span>
            <span v-else-if="!update.error" class="badge">已是最新</span>
          </template>
          <template v-else>—</template>
        </dd>

        <template v-if="update.released_at">
          <dt>发布时间</dt>
          <dd class="brk">{{ update.released_at }}</dd>
        </template>

        <template v-if="update.description">
          <dt>说明</dt>
          <dd class="brk">{{ update.description }}</dd>
        </template>

        <template v-if="update.download_url">
          <dt>本平台下载</dt>
          <dd class="brk"><a :href="update.download_url" target="_blank" rel="noopener">{{ update.download_url }}</a></dd>
        </template>

        <template v-if="update.release_url">
          <dt>发布页</dt>
          <dd class="brk"><a :href="update.release_url" target="_blank" rel="noopener">{{ update.release_url }}</a></dd>
        </template>

        <dt>来源</dt>
        <dd>{{ sourceLabel }}</dd>

        <dt>上次检查</dt>
        <dd>{{ checkedAt }}</dd>

        <template v-if="update.error">
          <dt>检查失败</dt>
          <dd class="brk fail">{{ update.error }}</dd>
        </template>
      </dl>

      <div v-if="update?.newer && !applyActive && can('update.apply')" class="apply-entry">
        <template v-if="update.managed">
          <button class="go-btn mono" type="button" data-testid="update-apply-open" @click="showApply = true; applyError = ''">
            立即更新到 {{ update.latest }}…
          </button>
        </template>
        <p v-else class="hint mono" data-testid="update-manual">
          这个 server 不是以服务方式运行的，不能自我更新：下载上面的二进制替换后重启 <code>gofer serve</code>；
          worker 用 <code>gofer worker upgrade &lt;id&gt;</code>（其他平台加 <code>--file &lt;该平台二进制&gt;</code>）。
          以 <code>gofer serve register</code> 注册为服务后，这里可以一键更新。
        </p>
      </div>

      <div v-if="apply" class="apply" data-testid="update-apply-progress">
        <div class="apply-head mono">
          <span>{{ applyActive ? '正在更新' : '上次更新' }}：{{ apply.from }} → {{ apply.version }}</span>
          <span :class="{ fail: apply.phase === 'failed', ok: apply.phase === 'done' }">{{ phaseLabel[apply.phase] || apply.phase }}</span>
        </div>
        <p v-if="unreachable && applyActive" class="hint mono">server 重启中，稍后自动刷新…</p>
        <p v-if="apply.error" class="fail mono brk apply-err">{{ apply.error }}</p>
        <!-- gofer-dzv3：等空闲期间说清楚在等谁、停住了多少，可随时取消。 -->
        <div v-if="waitingIdle" class="apply-wait" data-testid="update-wait-idle">
          <p class="hint mono">
            正在等机器空闲：本机还有 {{ waitingRows.length }} 个 job 在跑（worker 上的 job 不影响）；
            等待期间新提交的 job 已停住 {{ apply.paused ?? 0 }} 个，更新完成后自动开始。
          </p>
          <div v-for="j in waitingRows" :key="j.id" class="apply-row mono">
            <RouterLink class="brk" :to="`/jobs/${j.id}`">{{ j.title || j.id }}</RouterLink>
            <span class="dim">{{ j.runner || '' }}</span>
            <span class="dim">{{ j.status }}</span>
          </div>
          <p v-if="apply.wait_deadline" class="hint mono">放弃时间：{{ waitDeadlineText }}</p>
          <div class="apply-actions">
            <button
              class="mini-btn mono"
              type="button"
              :disabled="cancelling"
              data-testid="update-wait-cancel"
              @click="cancelWait"
            >
              {{ cancelling ? '取消中…' : '取消等待' }}
            </button>
            <span class="hint mono">取消后停住的 job 会立即开始，本次更新记为已取消。</span>
          </div>
          <p v-if="cancelError" class="fail mono brk apply-err">{{ cancelError }}</p>
        </div>
        <div v-for="w in apply.workers" :key="w.id" class="apply-row mono">
          <span class="brk">{{ w.id }}</span>
          <span class="dim">{{ w.os && w.arch ? `${w.os}/${w.arch}` : '' }}</span>
          <span :class="{ fail: w.state === 'failed', ok: w.state === 'succeeded', dim: w.state === 'skipped' }">
            {{ workerStateLabel[w.state] || w.state }}
          </span>
          <span v-if="w.error" class="fail brk apply-werr">{{ w.error }}</span>
        </div>
        <p class="hint mono">时间：{{ fmtDateTime(apply.started_at) }}；worker 的升级详情见 Runners 页。</p>
      </div>

      <p class="hint mono">
        server 会定时检查（默认每 2 小时，配置 <code>server.update</code>，可改用内部 mirror），发现新版本时发
        <code>update.available</code> 通知。
      </p>
    </div>

    <UpdateApplyDialog
      v-if="showApply && update?.latest"
      :current="update.current"
      :latest="update.latest"
      :submitting="applying"
      :error="applyError"
      @close="showApply = false"
      @confirm="startApply"
    />
  </div>
</template>

<style scoped>
.about {
  min-width: 0;
}
.facts {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 14px;
}
.facts-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.facts-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0;
}
.facts-grid {
  display: grid;
  grid-template-columns: 110px minmax(0, 1fr);
  gap: 8px 14px;
  margin: 0;
  font-size: 12px;
}
.facts-grid dt {
  color: var(--queue);
}
.facts-grid dd {
  margin: 0;
  color: var(--paper);
}
.brk {
  word-break: break-all;
}
.update {
  margin-top: 14px;
}
.mini-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.mini-btn:disabled {
  color: var(--queue);
  cursor: default;
}
.badge {
  display: inline-block;
  margin-left: 8px;
  padding: 0 6px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  color: var(--queue);
  font-size: 11px;
}
.badge--new {
  border-color: var(--done);
  color: var(--done);
}
.fail {
  color: var(--fail);
}
.ok {
  color: var(--done);
}
.dim {
  color: var(--queue);
}
.apply-entry {
  margin-top: 12px;
}
.go-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.go-btn:hover {
  opacity: 0.85;
}
.apply {
  margin-top: 12px;
  border-top: 1px dashed var(--line);
  padding-top: 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
}
.apply-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  color: var(--paper);
}
.apply-err {
  margin: 0;
}
.apply-row {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: 4px 12px;
  color: var(--paper);
}
.apply-werr {
  grid-column: 1 / -1;
  font-size: 11px;
}
/* gofer-dzv3：等空闲的进度块——在等谁、停住了多少、放弃时间与「取消等待」。 */
.apply-wait {
  display: flex;
  flex-direction: column;
  gap: 6px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
}
.apply-wait .hint {
  margin: 0;
}
.apply-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
.mini-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.mini-btn:disabled {
  color: var(--queue);
  cursor: default;
}
.hint {
  color: var(--queue);
  font-size: 11px;
  margin: 12px 0 0;
  line-height: 1.6;
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
.placeholder {
  color: var(--queue);
  font-size: 12px;
  margin: 10px 0 0;
}
</style>
