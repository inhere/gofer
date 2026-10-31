<script setup lang="ts">
// JobPreviews：JobDetail 概览里的「预览」区（设计稿 §3.7 / §4 P3）。
//  - 创建：端口 + TTL（可选「创建后立即打开」）；job 不在运行时表单置灰并说明原因；
//  - 列表：状态、到期时间、host:port 截断显示（绝不显示 token）；
//  - 撤销：二次确认（先点撤销，再点确认）；续租：用当前选的 TTL；
//  - 打开：新标签页打开创建时返回的一次性 URL，URL 只在内存里，用过即丢；
//  - 预览未启用 / job 非运行 / runner 不支持等，直接显示接口错误（err.<code> 已本地化）。
// 逻辑都在 utils/jobPreviews.ts；这里只管渲染与绑定。
import { computed, onMounted, ref, watch } from 'vue'
import { t, te } from '../i18n'
import StatusPill from '../ui/StatusPill.vue'
import Toast from '../ui/Toast.vue'
import Switch from '../ui/Switch.vue'
import { toastDuration } from '../ui/toast'
import { fmtDateTime } from '../api/time'
import type { JobPreview } from '../api/preview'
import {
  PREVIEW_DEFAULT_TTL_MINUTES,
  PREVIEW_TTL_MINUTES,
  parsePreviewPort,
  previewActive,
  previewHostLabel,
  previewJobRunning,
  previewStateFamily,
  useJobPreviews,
} from '../utils/jobPreviews'

const props = withDefaults(
  defineProps<{
    jobId: string
    // job 当前状态：只有运行态才能创建
    status: string
    // 预置列表（测试 / 服务端渲染用）；挂载后会被接口结果替换
    initialItems?: JobPreview[]
  }>(),
  { initialItems: () => [] },
)

const ctl = useJobPreviews(() => props.jobId)
const { items, error, notice, busy, pendingRevoke, openUrls, creating } = ctl
if (props.initialItems.length > 0) items.value = [...props.initialItems]

const portText = ref('')
const ttlMinutes = ref(PREVIEW_DEFAULT_TTL_MINUTES)
const openNow = ref(false)
const portError = ref(false)

const running = computed(() => previewJobRunning(props.status))
const hostname = typeof window !== 'undefined' && window.location ? window.location.hostname : 'localhost'

function ttlLabel(min: number): string {
  return min >= 60 ? t('jobs.detail.preview.ttlHour', { n: min / 60 }) : t('jobs.detail.preview.ttlMin', { n: min })
}

function stateLabel(state: string | undefined): string {
  const key = `jobs.detail.preview.state.${state ?? ''}`
  return state && te(key) ? t(key) : (state ?? '')
}

function hostLabel(p: JobPreview): string {
  return previewHostLabel(hostname, p.port)
}

async function onCreate(): Promise<void> {
  const port = parsePreviewPort(portText.value)
  portError.value = port === null
  if (port === null) return
  const ok = await ctl.create(port, ttlMinutes.value * 60, openNow.value)
  if (ok) portText.value = ''
}

function noticeText(): string {
  return notice.value ? t(`jobs.detail.preview.notice.${notice.value}`) : ''
}

onMounted(() => {
  void ctl.load()
})
// job 状态变化（尤其是结束）时，服务端会关掉它的预览：重新拉一次列表。
watch(
  () => props.status,
  () => {
    void ctl.load()
  },
)
</script>

<template>
  <section class="jp" data-test="job-previews">
    <h2 class="jp-title mono">
      {{ t('jobs.detail.preview.title') }}
      <span class="jp-hint">{{ t('jobs.detail.preview.hint') }}</span>
    </h2>

    <form class="jp-form" data-test="preview-form" @submit.prevent="onCreate">
      <label class="jp-field mono">
        {{ t('jobs.detail.preview.port') }}
        <input
          v-model="portText"
          class="jp-input mono"
          inputmode="numeric"
          :placeholder="t('jobs.detail.preview.portPh')"
          :disabled="!running || creating"
          :aria-invalid="portError ? 'true' : undefined"
          data-test="preview-port"
        />
      </label>
      <label class="jp-field mono">
        {{ t('jobs.detail.preview.ttl') }}
        <select v-model.number="ttlMinutes" class="jp-input mono" :disabled="!running || creating" data-test="preview-ttl">
          <option v-for="m in PREVIEW_TTL_MINUTES" :key="m" :value="m">{{ ttlLabel(m) }}</option>
        </select>
      </label>
      <Switch v-model="openNow" :disabled="!running || creating" data-test="preview-open-now">
        {{ t('jobs.detail.preview.openNow') }}
      </Switch>
      <button class="jp-btn jp-btn--primary mono" type="submit" :disabled="!running || creating" data-test="preview-create">
        {{ creating ? t('jobs.detail.preview.creating') : t('jobs.detail.preview.create') }}
      </button>
    </form>
    <p v-if="!running" class="jp-note mono" data-test="preview-not-running">{{ t('jobs.detail.preview.notRunning') }}</p>
    <p v-if="portError" class="jp-err mono" data-test="preview-port-error">{{ t('jobs.detail.preview.portInvalid') }}</p>
    <p v-if="error" class="jp-err mono" role="alert" data-test="preview-error">{{ error }}</p>

    <ul v-if="items.length > 0" class="jp-list" data-test="preview-list">
      <li v-for="p in items" :key="p.id" class="jp-row" :data-preview-id="p.id">
        <StatusPill :status="p.state" :family="previewStateFamily(p.state)" :label="stateLabel(p.state)" />
        <span class="jp-host mono" :title="hostLabel(p)" data-test="preview-host">{{ hostLabel(p) }}</span>
        <span v-if="p.target_port" class="jp-target mono">→ :{{ p.target_port }}</span>
        <span class="jp-exp mono" data-test="preview-expires">{{ t('jobs.detail.preview.expires', { time: fmtDateTime(p.expires_at) }) }}</span>
        <template v-if="previewActive(p)">
          <button
            v-if="openUrls[p.id]"
            class="jp-btn jp-btn--primary mono"
            type="button"
            data-test="preview-open"
            @click="ctl.open(p.id)"
          >{{ t('jobs.detail.preview.open') }}</button>
          <span v-else class="jp-nolink mono" :title="t('jobs.detail.preview.noLink')">{{ t('jobs.detail.preview.noLinkShort') }}</span>
          <button
            class="jp-btn mono"
            type="button"
            :disabled="busy.has(p.id)"
            data-test="preview-renew"
            @click="ctl.renew(p.id, ttlMinutes * 60)"
          >{{ t('jobs.detail.preview.renew') }}</button>
          <template v-if="pendingRevoke === p.id">
            <span class="jp-ask mono">{{ t('jobs.detail.preview.revokeAsk') }}</span>
            <button class="jp-btn jp-btn--danger mono" type="button" data-test="preview-revoke-confirm" @click="ctl.confirmRevoke()">
              {{ t('jobs.detail.preview.revokeConfirm') }}
            </button>
            <button class="jp-btn mono" type="button" data-test="preview-revoke-cancel" @click="ctl.cancelRevoke()">
              {{ t('jobs.detail.preview.revokeCancel') }}
            </button>
          </template>
          <button
            v-else
            class="jp-btn mono"
            type="button"
            :disabled="busy.has(p.id)"
            data-test="preview-revoke"
            @click="ctl.askRevoke(p.id)"
          >{{ t('jobs.detail.preview.revoke') }}</button>
        </template>
      </li>
    </ul>
    <p v-else class="jp-note mono" data-test="preview-empty">{{ t('jobs.detail.preview.empty') }}</p>

    <Toast
      v-if="notice"
      placement="corner"
      closable
      :text="noticeText()"
      :duration="toastDuration() || 4000"
      @close="notice = ''"
    />
  </section>
</template>

<style scoped>
.jp {
  margin: 12px 0;
  min-width: 0;
}
.jp-title {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 4px 10px;
  margin: 0 0 6px;
  font-size: 13px;
}
.jp-hint {
  color: var(--ink-3);
  font-size: 12px;
  font-weight: 400;
}
.jp-form {
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 8px 12px;
}
.jp-field {
  display: flex;
  flex-direction: column;
  gap: 2px;
  color: var(--ink-3);
  font-size: 12px;
}
.jp-input {
  min-width: 90px;
  padding: 3px 6px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: var(--surface);
  color: var(--ink);
}
.jp-list {
  margin: 8px 0 0;
  padding: 0;
  list-style: none;
}
.jp-row {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px 10px;
  padding: 4px 0;
  border-bottom: 1px solid var(--line);
}
.jp-host {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.jp-target,
.jp-exp,
.jp-nolink,
.jp-note {
  color: var(--ink-3);
  font-size: 12px;
}
.jp-note {
  margin: 6px 0 0;
}
.jp-ask {
  color: var(--fail);
  font-size: 12px;
}
.jp-err {
  margin: 6px 0 0;
  color: var(--fail);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.jp-btn {
  padding: 2px 8px;
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: transparent;
  color: inherit;
  cursor: pointer;
}
.jp-btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.jp-btn--primary {
  border-color: var(--accent);
  color: var(--accent);
}
.jp-btn--danger {
  border-color: var(--fail);
  color: var(--fail);
}
</style>
