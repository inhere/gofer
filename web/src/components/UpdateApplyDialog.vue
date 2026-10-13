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
  (e: 'confirm', workers: string[]): void
}>()

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
        server 会下载 {{ latest }}（有 sha256 时校验），等运行中的 job 结束后重启为新版本（当前 {{ current }}）。
        重启期间控制台会短暂断开。
      </p>
      <p v-if="running != null" class="ua-text mono" :class="{ 'ua-warn': running > 0 }" data-testid="update-running-jobs">
        {{ running > 0 ? `现在有 ${running} 个 job 在运行，重启会等它们结束（最多约 5 分钟，超时则放弃本次更新）。` : '现在没有运行中的 job。' }}
      </p>

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
          @click="emit('confirm', [...picked])"
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
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  color: var(--phosphor);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.ua-x,
.ua-btn {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 12px;
  font-size: 12px;
}
.ua-x {
  color: var(--queue);
  padding: 2px 8px;
}
.ua-x:hover:not(:disabled),
.ua-btn:hover:not(:disabled) {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.ua-btn:disabled {
  color: var(--queue);
}
.ua-btn--go {
  border-color: var(--phosphor);
  color: var(--phosphor);
}
.ua-text {
  margin: 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--paper);
}
.ua-warn {
  color: var(--run);
}
.ua-workers {
  border: 1px solid var(--line);
  border-radius: var(--radius);
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
  color: var(--queue);
}
.ua-link {
  background: transparent;
  border: none;
  color: var(--phosphor);
  font-size: 11px;
  padding: 0;
}
.ua-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto auto;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  color: var(--paper);
}
.ua-row--off {
  color: var(--queue);
}
.ua-id {
  word-break: break-all;
}
.ua-dim {
  color: var(--queue);
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
