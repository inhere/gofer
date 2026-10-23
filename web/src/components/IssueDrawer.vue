<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import StatusPill from '../ui/StatusPill.vue'
import Drawer from '../ui/Drawer.vue'
import { issueRelations, type IssueNode } from '../utils/issueTree'
import MarkdownBlock from './MarkdownBlock.vue'
import type { TrackerIssueView } from '../api/types'
import { ISSUE_STATUS_VALUES, issueStatusText, type IssueStatus } from '../utils/statusText'
import { t, te } from '../i18n'

const props = defineProps<{ issue: TrackerIssueView; saving: boolean; expectedRev: number; allIssues?: IssueNode[]; writeBlock?: string }>()
const emit = defineEmits<{
  (event: 'close'): void
  (event: 'save', value: { title: string; status: string; priority: number; description: string }): void
  (event: 'comment', text: string): void
  (event: 'open-issue', id: string): void
}>()
const title = ref(''); const status = ref(''); const priority = ref(0); const description = ref(''); const comment = ref('')
watch(() => props.issue, (issue) => { title.value = issue.title; status.value = issue.status; priority.value = issue.priority; description.value = issue.description ?? '' }, { immediate: true })
const rel = computed(() => issueRelations(props.allIssues ?? [], props.issue.id))
const hasRelations = computed(() => !!(rel.value.parentId || rel.value.children.length || rel.value.blockedBy.length || rel.value.blocks.length || rel.value.other.length))
function statusLabel(s: IssueStatus): string {
  return te(`status.${s}`) ? t(`status.${s}`) : issueStatusText(s)
}
function save(): void { emit('save', { title: title.value, status: status.value, priority: priority.value, description: description.value }) }
function sendComment(): void { if (comment.value.trim()) { emit('comment', comment.value.trim()); comment.value = '' } }
</script>

<template>
  <!-- 侧抽屉外壳换成统一的 ui/Drawer（设计稿 §7.3、§8）：Esc 关闭、焦点循环与归还由它负责。 -->
  <Drawer :model-value="true" :label="`${t('issues.drawer.title')} · ${issue.id}`" width="520px" @update:model-value="emit('close')">
    <div class="drawer-body">
      <p class="issue-id mono" data-test="drawer-issue-id">{{ issue.id }}</p>
      <label class="field">{{ t('issues.drawer.titleField') }}<input v-model="title" class="in" /></label>
      <dl class="issue-meta">
        <div><dt>{{ t('issues.drawer.type') }}</dt><dd>{{ issue.type || '—' }}</dd></div>
        <div><dt>{{ t('issues.drawer.tags') }}</dt><dd>{{ issue.tags?.join(' · ') || '—' }}</dd></div>
        <div><dt>{{ t('issues.drawer.createdBy') }}</dt><dd>{{ issue.created_by || '—' }}</dd></div>
        <div><dt>{{ t('issues.drawer.createdAt') }}</dt><dd>{{ issue.created_at || '—' }}</dd></div>
      </dl>
      <div class="drawer-grid">
        <label class="field">{{ t('issues.drawer.status') }}<select v-model="status" class="in"><option v-for="s in ISSUE_STATUS_VALUES" :key="s" :value="s">{{ statusLabel(s) }}</option></select></label>
        <label class="field">{{ t('issues.drawer.priority') }}<input v-model.number="priority" class="in" type="number" min="0" max="4" /></label>
      </div>
      <section v-if="hasRelations" class="relations" data-test="relations">
        <h3>{{ t('issues.drawer.relations') }}</h3>
        <div v-if="rel.parentId" class="rel-group"><span class="rel-label">{{ t('issues.drawer.rel.parent') }}</span><a href="#" class="rel-link" data-test="rel-parent" @click.prevent="emit('open-issue', rel.parentId!)"><span class="mono">{{ rel.parentId }}</span> {{ rel.parent?.title ?? t('issues.drawer.rel.missing') }}</a><StatusPill v-if="rel.parent" :status="(rel.parent.status || 'open') as any" /></div>
        <div v-if="rel.children.length" class="rel-group"><span class="rel-label">{{ t('issues.drawer.rel.children', { closed: rel.children.filter((c) => c.status === 'closed').length, total: rel.children.length }) }}</span><ul class="rel-list"><li v-for="c in rel.children" :key="c.id" data-test="rel-child"><a href="#" class="rel-link" @click.prevent="emit('open-issue', c.id)"><span class="mono">{{ c.id }}</span> {{ c.title }}</a><StatusPill :status="(c.status || 'open') as any" /></li></ul></div>
        <div v-if="rel.blockedBy.length" class="rel-group"><span class="rel-label">{{ t('issues.drawer.rel.blockedBy') }}</span><ul class="rel-list"><li v-for="b in rel.blockedBy" :key="b.id" :class="{ 'rel-open': b.open }" data-test="rel-blockedby"><a href="#" class="rel-link" @click.prevent="emit('open-issue', b.id)"><span class="mono">{{ b.id }}</span> {{ b.node?.title ?? t('issues.drawer.rel.missing') }}</a><StatusPill v-if="b.node" :status="(b.node.status || 'open') as any" /><em v-if="b.open" class="rel-warn">{{ t('issues.drawer.rel.open') }}</em></li></ul></div>
        <div v-if="rel.blocks.length" class="rel-group"><span class="rel-label">{{ t('issues.drawer.rel.blocks') }}</span><ul class="rel-list"><li v-for="b in rel.blocks" :key="b.id" data-test="rel-blocks"><a href="#" class="rel-link" @click.prevent="emit('open-issue', b.id)"><span class="mono">{{ b.id }}</span> {{ b.node?.title }}</a><StatusPill v-if="b.node" :status="(b.node.status || 'open') as any" /></li></ul></div>
        <div v-if="rel.other.length" class="rel-group"><span class="rel-label">{{ t('issues.drawer.rel.other') }}</span><ul class="rel-list"><li v-for="o in rel.other" :key="`${o.type}-${o.id}`" data-test="rel-other"><span class="rel-type mono">{{ o.type }}</span><a href="#" class="rel-link" @click.prevent="emit('open-issue', o.id)"><span class="mono">{{ o.id }}</span> {{ o.node?.title ?? t('issues.drawer.rel.missing') }}</a><StatusPill v-if="o.node" :status="(o.node.status || 'open') as any" /></li></ul></div>
      </section>
      <label class="field">{{ t('issues.drawer.desc') }}<textarea v-model="description" class="in" rows="5" /></label>
      <MarkdownBlock v-if="issue.description" :text="issue.description" />
      <div class="actions"><button class="btn btn--primary" type="button" :disabled="saving || !!props.writeBlock" :title="props.writeBlock || undefined" @click="save">{{ t('issues.drawer.save') }}</button></div>
      <h3>{{ t('issues.drawer.notes') }}</h3>
      <ul v-if="issue.notes?.length" class="thread"><li v-for="note in issue.notes" :key="`${note.at}-${note.by}`"><span class="meta mono">{{ note.at }} · {{ note.by }}</span><p>{{ note.text }}</p></li></ul>
      <p v-else class="empty-line">{{ t('issues.drawer.none') }}</p>
      <h3>{{ t('issues.drawer.comments') }}</h3>
      <ul v-if="issue.comments?.length" class="thread"><li v-for="item in issue.comments" :key="`${item.at}-${item.by}`"><span class="meta mono">{{ item.at }} · {{ item.by }}</span><p>{{ item.text }}</p></li></ul>
      <p v-else class="empty-line">{{ t('issues.drawer.none') }}</p>
      <div class="comment-form"><textarea v-model="comment" class="in" rows="4" :placeholder="t('issues.drawer.commentPlaceholder')" /><div class="comment-actions"><button class="btn" type="button" :disabled="!!props.writeBlock" :title="props.writeBlock || undefined" @click="sendComment">{{ t('issues.drawer.comment') }}</button></div></div>
    </div>
  </Drawer>
</template>

<style scoped>
.issue-id {
  margin: 0 0 var(--s2);
  color: var(--accent);
  font-size: var(--fs-sm);
}
.field {
  display: flex;
  flex-direction: column;
  gap: 5px;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  margin-bottom: var(--s3);
}
.drawer-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--s2);
}
.in {
  width: 100%;
  min-height: 32px;
  padding: 4px var(--s2);
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
}
.drawer-body h3 {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  letter-spacing: 0.08em;
  text-transform: uppercase;
  margin: var(--s3) 0 var(--s1);
}
.btn {
  display: inline-flex;
  align-items: center;
  min-height: 32px;
  padding: 0 var(--s3);
  background: var(--surface-2);
  color: var(--ink);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-ctl);
  font: inherit;
  font-size: var(--fs-sm);
}
.btn--primary {
  background: var(--accent);
  border-color: var(--accent);
  color: var(--accent-ink);
  font-weight: 600;
}
.btn:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
.actions {
  display: flex;
  gap: var(--s2);
}
.issue-meta {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--s2);
  margin: 0 0 var(--s3);
  padding: var(--s2);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.issue-meta div {
  display: flex;
  gap: var(--s2);
  font-size: var(--fs-xs);
  min-width: 0;
}
.issue-meta dt {
  color: var(--ink-3);
  white-space: nowrap;
}
.issue-meta dd {
  margin: 0;
  color: var(--ink);
  overflow-wrap: anywhere;
}
.thread {
  list-style: none;
  margin: 0;
  padding: 0;
}
.thread li {
  margin: var(--s2) 0;
}
.thread .meta {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
.thread p {
  margin: 3px 0;
  color: var(--ink);
}
.empty-line {
  color: var(--ink-3);
  margin: var(--s1) 0 var(--s3);
  font-size: var(--fs-sm);
}
.comment-form {
  margin-top: var(--s2);
}
.comment-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: var(--s1);
}
.relations {
  margin-bottom: var(--s3);
  padding: var(--s2);
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
}
.relations h3 {
  margin: 0 0 var(--s2);
}
.rel-group {
  display: flex;
  flex-wrap: wrap;
  gap: 4px var(--s2);
  align-items: center;
  margin-bottom: var(--s2);
  font-size: var(--fs-sm);
}
.rel-group .rel-list {
  flex: 0 0 100%;
}
.rel-label {
  flex: 0 0 100%;
  color: var(--ink-3);
  font-size: var(--fs-xs);
  margin-bottom: 3px;
}
.rel-list {
  list-style: none;
  padding: 0;
  margin: 0;
}
.rel-list li {
  display: flex;
  gap: 6px;
  align-items: center;
  flex-wrap: wrap;
  margin: 3px 0;
}
.rel-link {
  color: var(--accent);
  overflow-wrap: anywhere;
  min-width: 0;
}
.rel-open {
  border-left: 2px solid var(--fail);
  padding-left: 6px;
}
.rel-warn {
  color: var(--fail);
  font-style: normal;
  font-size: var(--fs-xs);
}
.rel-type {
  color: var(--ink-3);
  font-size: var(--fs-xs);
}
@media (max-width: 760px) {
  .drawer-grid,
  .issue-meta {
    grid-template-columns: 1fr;
  }
  .btn,
  .in {
    min-height: 40px;
  }
}
</style>
