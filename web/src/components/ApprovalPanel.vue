<script setup lang="ts">
// 待批面板（gofer-9b1b）：job 详情页首，仅 status = awaiting_approval 时由 JobDetail 挂出。
// 让人（多半在手机上）一眼看清「批了会跑什么」：理由、完整命令（exec 的 argv / agent 的 prompt
// 开头，折行不横滚）、在哪跑（项目 / cwd / runner / agent）、谁提交的、还剩多久过期、附带属性。
// 「批准」一键直发，不二次确认；「拒绝」展开可选理由框。≤640px 时按钮条贴底、触控高度 ≥44px。
// 操作状态与错误提示在 utils/approval.ts（可单测）；成功后把最新 job 经 decided 交回页面。
import { computed, onMounted, ref, watch } from 'vue'
import { approveJob, getJobRequest, rejectJob } from '../api/client'
import type { Job } from '../api/types'
import { commandLine, countdownText, createApprovalActions, submitterText } from '../utils/approval'
import { fmtJobTimeout } from '../utils/jobTimeout'
import { runnerLabel } from '../utils/runnerDisplay'

const props = defineProps<{ job: Job; nowSec: number }>()
const emit = defineEmits<{ (e: 'decided', job: Job): void }>()

const hold = computed(() => props.job.hold)
const isExec = computed(() => (hold.value?.command?.length ?? 0) > 0)
const cmdText = computed(() => commandLine(hold.value?.command))
const prompt = computed(() => hold.value?.prompt_preview ?? '')
const submitter = computed(() => submitterText(props.job))
const countdown = computed(() => countdownText(hold.value?.expires_at, props.nowSec))
const expired = computed(() => countdown.value === '已过期')

// 附带属性：执行后验收、受管 worktree（只记在原始请求里，按需取一次）、只读、job 超时。
const wantsWorktree = ref(false)
const extras = computed(() => {
  const out: string[] = []
  if (props.job.require_review) out.push('执行后要人验收（--review）')
  if (wantsWorktree.value) out.push('在受管 worktree 里执行（--worktree）')
  if (props.job.read_only) out.push('只读（agent 不能写文件）')
  const t = props.job.requested_timeout_sec
  out.push(t ? `job ${fmtJobTimeout(t)}` : 'job 超时按项目 / agent 默认')
  return out
})

async function loadRequestExtras(): Promise<void> {
  try {
    const { request } = await getJobRequest(props.job.id)
    wantsWorktree.value = !!(request as { worktree?: boolean }).worktree
  } catch {
    // 附带信息，取不到就不显示这一条
  }
}
onMounted(() => void loadRequestExtras())
watch(
  () => props.job.id,
  () => {
    wantsWorktree.value = false
    void loadRequestExtras()
  },
)

const actions = createApprovalActions(
  () => props.job.id,
  { approve: approveJob, reject: rejectJob },
  (j) => emit('decided', j),
)
const { busy, error, rejectOpen, rejectNote } = actions

function fmtTime(sec: number | undefined): string {
  return sec ? new Date(sec * 1000).toLocaleString() : '—'
}
</script>

<template>
  <section class="ap" data-test="approval-panel" aria-label="待批准">
    <div class="ap-head">
      <span class="ap-title mono">⏸ 等你批准</span>
      <span class="ap-count mono" :class="{ 'ap-count--bad': expired }" data-test="ap-countdown">
        {{ countdown || '—' }}<template v-if="hold?.expires_at"> · {{ fmtTime(hold.expires_at) }} 过期</template>
      </span>
    </div>

    <p class="ap-reason" data-test="ap-reason">{{ hold?.reason || '（提交者没写理由）' }}</p>

    <div v-if="isExec" class="ap-block">
      <div class="ap-k mono">批准后执行</div>
      <pre class="ap-cmd mono" data-test="ap-command">{{ cmdText }}</pre>
    </div>
    <details v-else-if="prompt" class="ap-block" open>
      <summary class="ap-k mono">agent 任务（prompt 开头）</summary>
      <pre class="ap-cmd mono" data-test="ap-prompt">{{ prompt }}</pre>
    </details>

    <dl class="ap-facts">
      <dt class="mono">项目</dt>
      <dd class="mono">{{ job.project_key }}</dd>
      <dt class="mono">cwd</dt>
      <dd class="mono">{{ job.cwd || '—' }}</dd>
      <dt class="mono">runner</dt>
      <dd class="mono">{{ runnerLabel(job.runner) }}<template v-if="job.worker_id"> · {{ job.worker_id }}</template></dd>
      <dt class="mono">agent</dt>
      <dd class="mono">{{ job.agent }}<template v-if="job.model"> · {{ job.model }}</template></dd>
      <dt class="mono">提交者</dt>
      <dd class="mono" data-test="ap-submitter">{{ submitter || '—' }}</dd>
      <dt class="mono">提交时间</dt>
      <dd class="mono">{{ fmtTime(job.started_at) }}</dd>
      <dt class="mono">附带</dt>
      <dd class="mono" data-test="ap-extras">{{ extras.join(' · ') }}</dd>
    </dl>

    <div v-if="rejectOpen" class="ap-reject" data-test="ap-reject-form">
      <textarea
        v-model="rejectNote"
        class="ap-note mono"
        rows="3"
        placeholder="拒绝理由（可不填）：会写进 job 的错误信息，提交者能看到"
        aria-label="拒绝理由（可不填）"
      ></textarea>
    </div>

    <p v-if="error" class="ap-err mono" role="alert" data-test="ap-error">{{ error }}</p>

    <div class="ap-actions">
      <template v-if="!rejectOpen">
        <button
          class="ap-btn ap-btn--ok mono"
          type="button"
          data-test="ap-approve"
          :disabled="!!busy"
          @click="actions.approve"
        >{{ busy === 'approve' ? '批准中…' : '批准' }}</button>
        <button
          class="ap-btn ap-btn--bad mono"
          type="button"
          data-test="ap-reject"
          :disabled="!!busy"
          @click="actions.openReject"
        >拒绝</button>
      </template>
      <template v-else>
        <button
          class="ap-btn ap-btn--bad mono"
          type="button"
          data-test="ap-reject-confirm"
          :disabled="!!busy"
          @click="actions.reject"
        >{{ busy === 'reject' ? '拒绝中…' : '确认拒绝' }}</button>
        <button class="ap-btn mono" type="button" :disabled="!!busy" @click="actions.cancelReject">返回</button>
      </template>
    </div>
  </section>
</template>

<style scoped>
.ap {
  margin: 12px 0;
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  background: var(--panel);
  min-width: 0;
}
.ap-head {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 6px 12px;
  padding: 8px 12px;
  border-bottom: 1px solid var(--line);
}
.ap-title {
  color: var(--phosphor);
  font-size: 13px;
  letter-spacing: 0.06em;
}
.ap-count {
  color: var(--queue);
  font-size: 12px;
}
.ap-count--bad {
  color: var(--fail);
}
.ap-reason {
  margin: 0;
  padding: 10px 12px 4px;
  color: var(--paper);
  font-size: 16px;
  font-weight: 600;
  line-height: 1.45;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.ap-block {
  margin: 6px 12px;
  min-width: 0;
}
.ap-k {
  color: var(--queue);
  font-size: 11px;
  letter-spacing: 0.04em;
  margin-bottom: 4px;
  cursor: default;
}
summary.ap-k {
  cursor: pointer;
}
/* 完整命令：折行、不横滚（手机上一眼看全每个参数）。 */
.ap-cmd {
  margin: 0;
  padding: 8px 10px;
  color: var(--paper);
  background: var(--term-bg);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 13px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-all;
  overflow-x: hidden;
}
.ap-facts {
  display: grid;
  grid-template-columns: max-content minmax(0, 1fr);
  gap: 4px 12px;
  margin: 8px 12px;
  font-size: 12px;
}
.ap-facts dt {
  color: var(--queue);
}
.ap-facts dd {
  margin: 0;
  color: var(--paper);
  min-width: 0;
  overflow-wrap: anywhere;
}
.ap-reject {
  margin: 8px 12px 0;
}
.ap-note {
  box-sizing: border-box;
  width: 100%;
  padding: 6px 8px;
  color: var(--paper);
  background: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 13px;
  resize: vertical;
}
.ap-err {
  margin: 8px 12px 0;
  color: var(--fail);
  font-size: 12px;
  overflow-wrap: anywhere;
}
.ap-actions {
  display: flex;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--line);
  background: var(--panel);
}
.ap-btn {
  min-height: 32px;
  padding: 4px 18px;
  color: var(--paper);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  font-size: 13px;
}
.ap-btn:disabled {
  opacity: 0.55;
}
.ap-btn--ok {
  color: var(--ink);
  background: var(--done);
  border-color: var(--done);
  font-weight: 600;
}
.ap-btn--bad {
  color: var(--fail);
  border-color: var(--fail);
}

/* 手机：信息单列；按钮条贴在视口底部，按钮等分且触控高度 ≥44px。 */
@media (max-width: 640px) {
  .ap-facts {
    grid-template-columns: minmax(0, 1fr);
    gap: 0;
  }
  .ap-facts dd {
    margin-bottom: 6px;
  }
  .ap-actions {
    position: sticky;
    bottom: 0;
    z-index: 5;
    box-shadow: 0 -4px 12px rgba(0, 0, 0, 0.25);
  }
  .ap-btn {
    flex: 1;
    min-height: 44px;
    font-size: 15px;
  }
}
</style>
