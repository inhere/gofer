<script setup lang="ts">
import { ref, watch } from 'vue'
import MarkdownBlock from './MarkdownBlock.vue'
import type { TrackerIssueView } from '../api/types'

const props = defineProps<{ issue: TrackerIssueView; saving: boolean; expectedRev: number }>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'save', value: { title: string; status: string; priority: number; description: string }): void
  (event: 'comment', text: string): void
}>()
const title = ref(''); const status = ref(''); const priority = ref(0); const description = ref(''); const comment = ref('')
watch(() => props.issue, (issue) => { title.value = issue.title; status.value = issue.status; priority.value = issue.priority; description.value = issue.description ?? '' }, { immediate: true })
function save(): void { emit('save', { title: title.value, status: status.value, priority: priority.value, description: description.value }) }
function sendComment(): void { if (comment.value.trim()) { emit('comment', comment.value.trim()); comment.value = '' } }
</script>

<template>
  <div class="backdrop" @click="emit('close')"></div>
  <aside class="drawer">
    <header class="drawer-head"><div><p class="eyebrow mono">ISSUE DETAIL</p><h2>{{ issue.id }}</h2></div><button class="drawer-close" type="button" @click="emit('close')">×</button></header>
    <div class="drawer-body">
      <label>标题<input v-model="title" class="editor-input" /></label>
      <dl class="issue-meta"><div><dt>类型</dt><dd>{{ issue.type || '—' }}</dd></div><div><dt>标签</dt><dd>{{ issue.tags?.join(' · ') || '—' }}</dd></div><div><dt>创建人</dt><dd>{{ issue.created_by || '—' }}</dd></div><div><dt>创建时间</dt><dd>{{ issue.created_at || '—' }}</dd></div></dl>
      <div class="drawer-grid"><label>状态<select v-model="status" class="filter-select"><option>open</option><option>in_progress</option><option>blocked</option><option>closed</option></select></label><label>优先级<input v-model.number="priority" class="editor-input" type="number" min="0" max="4" /></label></div>
      <label>描述<textarea v-model="description" class="editor-input" rows="5" /></label>
      <MarkdownBlock v-if="issue.description" :text="issue.description" />
      <div class="actions"><button class="primary-btn" type="button" :disabled="saving" @click="save">保存</button></div>
      <h3>notes</h3><ul v-if="issue.notes?.length"><li v-for="note in issue.notes" :key="`${note.at}-${note.by}`"><span class="mono">{{ note.at }} · {{ note.by }}</span><p>{{ note.text }}</p></li></ul><p v-else class="empty-line mono">暂无</p>
      <h3>comments</h3><ul v-if="issue.comments?.length"><li v-for="item in issue.comments" :key="`${item.at}-${item.by}`"><span class="mono">{{ item.at }} · {{ item.by }}</span><p>{{ item.text }}</p></li></ul><p v-else class="empty-line mono">暂无</p>
      <div class="comment-form"><textarea v-model="comment" class="editor-input" rows="4" placeholder="发表评论" /><div class="comment-actions"><button class="secondary-btn" type="button" @click="sendComment">评论</button></div></div>
    </div>
  </aside>
</template>

<style scoped>
.backdrop{position:fixed;inset:0;background:color-mix(in srgb,var(--ink) 64%,transparent);z-index:70}.drawer{position:fixed;top:0;right:0;bottom:0;width:min(520px,92vw);z-index:80;background:var(--panel);border-left:1px solid var(--line);box-shadow:-8px 0 24px color-mix(in srgb,var(--ink) 45%,transparent);overflow:auto}.drawer-head{display:flex;justify-content:space-between;align-items:flex-start;padding:16px;border-bottom:1px solid var(--line)}.drawer-head h2{margin:4px 0 0;font-size:16px}.drawer-close{border:1px solid var(--line);background:transparent;color:var(--paper);border-radius:var(--radius);font-size:20px;line-height:1;width:28px;height:28px}.drawer-body{padding:16px}.drawer-body label{display:flex;flex-direction:column;gap:5px;color:var(--queue);font-size:11px;margin-bottom:12px}.drawer-grid{display:grid;grid-template-columns:1fr 1fr;gap:10px}.drawer-body h3{color:var(--queue);font:12px var(--font-mono);letter-spacing:.08em;text-transform:uppercase}.drawer-body ul{padding-left:18px}.drawer-body li{margin:10px 0}.drawer-body li span{color:var(--queue);font-size:10px}.drawer-body li p{margin:3px 0;color:var(--paper)}.eyebrow{color:var(--queue);font-size:10px;letter-spacing:.18em;margin:0}.primary-btn,.secondary-btn,.editor-input,.filter-select{font:inherit;border-radius:var(--radius)}.primary-btn{background:var(--phosphor);color:var(--ink);border:1px solid var(--phosphor);padding:5px 10px}.secondary-btn{background:transparent;color:var(--phosphor);border:1px solid var(--line);padding:5px 10px}.editor-input,.filter-select{background:var(--panel);color:var(--paper);border:1px solid var(--line);padding:6px 8px}.actions{display:flex;gap:8px;margin-top:10px}
.issue-meta{display:grid;grid-template-columns:1fr 1fr;gap:8px;margin:0 0 14px;padding:10px;border:1px solid var(--line);border-radius:var(--radius)}.issue-meta div{display:flex;gap:8px;font-size:11px}.issue-meta dt{color:var(--queue)}.issue-meta dd{margin:0;color:var(--paper)}.comment-form{margin-top:10px}.comment-form .editor-input{display:block;width:100%}.comment-actions{display:flex;justify-content:flex-end;margin-top:6px}
.empty-line{color:var(--queue);margin:4px 0 10px}
</style>
