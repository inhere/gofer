<script setup lang="ts">
// 评论区（MCP-05 阶段 A）：一个 job / plan / todo 的评论线程 + 发一条评论。
//  - user 作者的评论里 @<agent|role> 会派活；行上显示「→ 已派发 job xxx」并可点进那个 job。
//  - agent/system 作者的评论只记录（服务端闸门），这里照原样展示，不做本地判断。
//  - 组件自带加载：挂上就拉一次线程，发完评论把返回的行直接追加（POST 响应带 dispatched 全量）。
import { onMounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { listComments, postComment } from '../api/client'
import { fmtDateTime } from '../api/time'
import type { Comment, CommentDispatch, CommentScope } from '../api/types'

const props = defineProps<{
  scope: CommentScope
  id: string
  // placeholder 由宿主页面给出（例如「@omp 补上测试…」），保持各页面的措辞习惯。
  placeholder?: string
}>()

const comments = ref<Comment[]>([])
const draft = ref('')
const error = ref('')
const loading = ref(false)
const posting = ref(false)

async function load(): Promise<void> {
  if (!props.id) return
  loading.value = true
  error.value = ''
  try {
    comments.value = await listComments(props.scope, props.id)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    comments.value = []
  } finally {
    loading.value = false
  }
}

async function submit(): Promise<void> {
  const body = draft.value.trim()
  if (!body || posting.value) return
  posting.value = true
  error.value = ''
  try {
    const created = await postComment(props.scope, props.id, body)
    comments.value = [...comments.value, created]
    draft.value = ''
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    posting.value = false
  }
}

// parts 把正文切成「普通文本 / @提及」两段，供模板高亮——不注入 HTML。
function parts(body: string): { text: string; mention: boolean }[] {
  const out: { text: string; mention: boolean }[] = []
  const re = /@[A-Za-z0-9_-]+/g
  let last = 0
  for (const m of body.matchAll(re)) {
    const at = m.index ?? 0
    if (at > last) out.push({ text: body.slice(last, at), mention: false })
    out.push({ text: m[0], mention: true })
    last = at + m[0].length
  }
  if (last < body.length) out.push({ text: body.slice(last), mention: false })
  return out
}

// dispatches 统一「刚发的（dispatched 全量）」与「历史行（只有回链）」两种形状，
// 让每一行都按同一种方式渲染。
function dispatches(cm: Comment): CommentDispatch[] {
  if (cm.dispatched && cm.dispatched.length > 0) return cm.dispatched
  if (cm.triggered_job_id) return [{ mention: '', kind: '', job_id: cm.triggered_job_id }]
  return []
}

function authorClass(kind: Comment['author_kind']): string {
  return kind === 'user' ? 'cm-author--user' : kind === 'agent' ? 'cm-author--agent' : 'cm-author--system'
}

onMounted(() => {
  void load()
})
watch(
  () => [props.scope, props.id],
  () => {
    comments.value = []
    void load()
  },
)
</script>

<template>
  <div class="cm">
    <ul v-if="comments.length > 0" class="cm-list">
      <li v-for="cm in comments" :key="cm.id" class="cm-row" :class="authorClass(cm.author_kind)">
        <div class="cm-head mono">
          <span class="cm-author">{{ cm.author }}</span>
          <span class="cm-kind">{{ cm.author_kind }}</span>
          <span class="cm-time">{{ fmtDateTime(cm.created_at) }}</span>
        </div>
        <div class="cm-body">
          <span
            v-for="(p, i) in parts(cm.body)"
            :key="i"
            :class="{ 'cm-mention': p.mention }"
          >{{ p.text }}</span>
        </div>
        <div v-if="dispatches(cm).length > 0" class="cm-dispatched mono">
          <span v-for="d in dispatches(cm)" :key="d.job_id" class="cm-dispatch">
            → 已派发 job
            <RouterLink class="cm-joblink" :to="'/jobs/' + d.job_id">{{ d.job_id }}</RouterLink>
            <span v-if="d.mention" class="cm-dispatch-meta">（@{{ d.mention }}，{{ d.kind }}）</span>
          </span>
        </div>
      </li>
    </ul>
    <p v-else-if="!loading" class="cm-empty mono">还没有评论。评论里 @agent 或 @role 会派一个 job（人发的评论才会派）。</p>

    <form class="cm-form" @submit.prevent="submit">
      <textarea
        v-model="draft"
        class="cm-input mono"
        rows="3"
        :placeholder="placeholder ?? '写点什么…  @omp 补上测试'"
        @keydown.ctrl.enter.prevent="submit"
      ></textarea>
      <div class="cm-form-foot">
        <span class="cm-hint mono">Ctrl+Enter 发送</span>
        <button class="cm-send mono" type="submit" :disabled="posting || draft.trim() === ''">
          {{ posting ? '发送中…' : '发送' }}
        </button>
      </div>
    </form>
    <p v-if="error" class="cm-error mono">{{ error }}</p>
  </div>
</template>

<style scoped>
.cm {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.cm-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.cm-row {
  border: 1px solid var(--line);
  border-left: 2px solid var(--line);
  padding: 8px 10px;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.cm-row.cm-author--user {
  border-left-color: var(--phosphor);
}
.cm-row.cm-author--agent {
  border-left-color: var(--run);
}
.cm-row.cm-author--system {
  border-left-color: var(--queue);
  opacity: 0.85;
}
.cm-head {
  display: flex;
  align-items: baseline;
  gap: 8px;
  font-size: 12px;
  color: var(--muted, #888);
}
.cm-author {
  color: var(--paper);
}
.cm-kind {
  font-size: 11px;
  opacity: 0.7;
}
.cm-time {
  margin-left: auto;
  font-size: 11px;
}
.cm-body {
  white-space: pre-wrap;
  word-break: break-word;
  font-size: 13px;
  line-height: 1.5;
}
.cm-mention {
  color: var(--phosphor);
}
.cm-dispatched {
  display: flex;
  flex-direction: column;
  gap: 2px;
  font-size: 12px;
}
.cm-joblink {
  color: var(--phosphor);
  text-decoration: none;
  border-bottom: 1px dotted var(--phosphor);
}
.cm-dispatch-meta {
  opacity: 0.7;
}
.cm-empty {
  font-size: 12px;
  color: var(--muted, #888);
  margin: 0;
}
.cm-form {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.cm-input {
  width: 100%;
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  padding: 8px;
  font-size: 13px;
  resize: vertical;
}
.cm-input:focus {
  outline: none;
  border-color: var(--phosphor);
}
.cm-form-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.cm-hint {
  font-size: 11px;
  color: var(--muted, #888);
}
.cm-send {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  padding: 4px 12px;
  cursor: pointer;
  font-size: 12px;
}
.cm-send:disabled {
  opacity: 0.4;
  cursor: default;
}
.cm-error {
  color: var(--fail);
  font-size: 12px;
  margin: 0;
}
</style>
