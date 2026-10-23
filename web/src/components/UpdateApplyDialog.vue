<script setup lang="ts">
// 一键更新确认弹层（gofer-29gi）：说明会发生什么（排空运行中的 job、server 重启），
// 列出 worker 供勾选（默认全选在线的；离线 / 版本过旧的不能远程升级）。
// 提交由父组件做（请求失败时把 error 传回来，弹层保持打开）。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { listJobs, listRunners } from '../api/client'
import type { Runner } from '../api/types'

const props = defineProps<{ current: string; latest: string; submitting: boolean; error: string }>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'confirm', workers: string[], waitIdleSec: number): void
}>()

// 最长等多久（gofer-dzv3）：server 会等本机运行中的 job 结束才重启，期间新 job 先停住；
// 到时间还没空闲就放弃本次更新（停住的 job 立即放行）。默认 2 小时。
const waitOptions: Array<{ sec: number; label: string }> = [
  { sec: 1800, label: '30 分钟' },
  { sec: 3600, label: '1 小时' },
  { sec: 7200, label: '2 小时' },
  { sec: 14400, label: '4 小时' },
]
const waitIdleSec = ref(7200)

interface WorkerRow {
  id: string
  platform: string
  version: string
  online: boolean
}

const workers = ref<WorkerRow[]>([])
const picked = ref<Set<string>>(new Set())
const running = ref<number | null>(null)
const loadError = ref('')
const loading = ref(true)

function toRow(r: Runner): WorkerRow {
  const w = r.worker
  return {
    id: r.worker_id || r.name,
    platform: w?.os && w?.arch ? `${w.os}/${w.arch}` : '—',
    version: w?.gofer_version || '—',
    online: r.status === 'connected' || r.status === 'up',
  }
}

const allPicked = computed(() => {
  const online = workers.value.filter((w) => w.online)
  return online.length > 0 && online.every((w) => picked.value.has(w.id))
})

function toggle(id: string): void {
  const next = new Set(picked.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  picked.value = next
}

function toggleAll(): void {
  picked.value = allPicked.value ? new Set() : new Set(workers.value.filter((w) => w.online).map((w) => w.id))
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const [rs, jobs] = await Promise.all([listRunners(), listJobs({ status: 'running', limit: 200 })])
    workers.value = rs.runners.filter((r) => r.type === 'worker').map(toRow)
    picked.value = new Set(workers.value.filter((w) => w.online).map((w) => w.id))
    running.value = jobs.jobs.length
  } catch (e) {
    loadError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

function onKeydown(ev: KeyboardEvent): void {
  if (ev.key === 'Escape' && !props.submitting) emit('close')
}

onMounted(() => {
  document.addEventListener('keydown', onKeydown)
  void load()
})
onUnmounted(() => {
  document.removeEventListener('keydown', onKeydown)
})
</script>

<template>
  <div class="ua-overlay" @click.self="!submitting && emit('close')">
    <div class="ua-panel" role="dialog" aria-modal="true" aria-label="立即更新">
      <div class="ua-head">
        <span class="ua-title mono">更新到 {{ latest }}</span>
        <button class="ua-x mono" type="button" :disabled="submitting" @click="emit('close')">关闭</button>
      </div>

      <p class="ua-text mono">
        server 会下载 {{ latest }}（有 sha256 时校验），等本机运行中的 job 结束后重启为新版本（当前 {{ current }}）。
        重启期间控制台会短暂断开。
      </p>
      <p v-if="running != null" class="ua-text mono" :class="{ 'ua-warn': running > 0 }" data-testid="update-running-jobs">
        {{
          running > 0
            ? `现在有 ${running} 个 job 在运行，会先等它们结束；期间新提交的 job 先停住，更新完成后自动开始（不用重新提交）。`
            : '现在没有运行中的 job，可以立即重启。'
        }}
      </p>

      <div class="ua-wait">
        <div class="ua-wait-head mono">最多等多久（到时还没空闲就放弃本次更新）</div>
        <div class="ua-wait-opts">
          <label v-for="o in waitOptions" :key="o.sec" class="ua-radio mono">
            <input
              type="radio"
              name="update-wait"
              :value="o.sec"
              :checked="waitIdleSec === o.sec"
              :disabled="submitting"
              :data-testid="`update-wait-${o.sec}`"
              @change="waitIdleSec = o.sec"
            />
            <span>{{ o.label }}</span>
          </label>
        </div>
        <p class="ua-dim mono">等待期间新 job 会先停住（不占执行位）；worker 上的 job 不受影响，server 回来后继续接管。</p>
      </div>

      <div class="ua-workers">
        <div class="ua-workers-head mono">
          <span>重启后一同更新的 worker</span>
          <button v-if="workers.length" class="ua-link mono" type="button" :disabled="submitting" @click="toggleAll">
            {{ allPicked ? '全不选' : '全选' }}
          </button>
        </div>
        <p v-if="loading" class="ua-dim mono">读取中...</p>
        <p v-else-if="loadError" class="ua-err mono">{{ loadError }}</p>
        <p v-else-if="!workers.length" class="ua-dim mono">没有登记的 worker。</p>
        <label v-for="w in workers" :key="w.id" class="ua-row mono" :class="{ 'ua-row--off': !w.online }">
          <input
            type="checkbox"
            :checked="picked.has(w.id)"
            :disabled="!w.online || submitting"
            :data-testid="`update-worker-${w.id}`"
            @change="toggle(w.id)"
          />
          <span class="ua-id">{{ w.id }}</span>
          <span class="ua-dim">{{ w.platform }}</span>
          <span class="ua-dim">{{ w.online ? w.version : '离线' }}</span>
        </label>
        <p class="ua-dim mono ua-note">其他平台的 worker 会下载自己平台的发布包。</p>
      </div>

      <p v-if="error" class="ua-err mono">{{ error }}</p>

      <div class="ua-actions">
        <button class="ua-btn mono" type="button" :disabled="submitting" @click="emit('close')">取消</button>
        <button
          class="ua-btn ua-btn--go mono"
          type="button"
          :disabled="submitting || loading"
          data-testid="update-apply-confirm"
          @click="emit('confirm', [...picked], waitIdleSec)"
        >
          {{ submitting ? '提交中…' : '开始更新' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ua-overlay {
  position: fixed;
  inset: 0;
  z-index: 40;
  background: rgba(0, 0, 0, 0.55);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}
.ua-panel {
  width: 100%;
  max-width: 520px;
  max-height: calc(100vh - 40px);
  overflow-y: auto;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.ua-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.ua-title {
  color: var(--accent);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.ua-x,
.ua-btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 4px 12px;
  font-size: 12px;
}
.ua-x {
  color: var(--ink-3);
  padding: 2px 8px;
}
.ua-x:hover:not(:disabled),
.ua-btn:hover:not(:disabled) {
  color: var(--accent);
  border-color: var(--accent);
}
.ua-btn:disabled {
  color: var(--ink-3);
}
.ua-btn--go {
  border-color: var(--accent);
  color: var(--accent);
}
.ua-text {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--ink);
}
.ua-warn {
  color: var(--run);
}
.ua-wait {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ua-wait-head {
  font-size: 11px;
  color: var(--ink-3);
}
.ua-wait-opts {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}
.ua-radio {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--ink);
  cursor: pointer;
}
.ua-workers {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.ua-workers-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  font-size: 11px;
  color: var(--ink-3);
}
.ua-link {
  background: transparent;
  border: none;
  color: var(--accent);
  font-size: 11px;
  padding: 0;
}
.ua-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--ink);
}
.ua-row--off {
  color: var(--ink-3);
}
.ua-id {
  word-break: break-all;
}
.ua-dim {
  color: var(--ink-3);
  font-size: 11px;
  margin: 0;
}
.ua-note {
  margin-top: 2px;
}
.ua-err {
  margin: 0;
  color: var(--fail);
  font-size: 12px;
  word-break: break-word;
}
.ua-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
