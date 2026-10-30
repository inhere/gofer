<script setup lang="ts">
// 反向链接面板：列出引用了当前对象（job / plan / issue 等）的来源，点击跳到来源详情。
//  - 自己按 kind + id 拉 GET /v1/refs；传了 items 就直接用（不请求，单测与预取用）。
//  - 没有引用（或加载失败）时整块不渲染，不占版面。
import { computed, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { listRefs } from '../api/client'
import { fmtAgo } from '../api/time'
import type { RefItem } from '../api/types'
import { t } from '../i18n'

const props = defineProps<{ kind: string; id: string; items?: RefItem[] }>()

const fetched = ref<RefItem[]>([])
const rows = computed(() => props.items ?? fetched.value)

let seq = 0
async function load(): Promise<void> {
  if (props.items || !props.kind || !props.id) {
    fetched.value = []
    return
  }
  const mine = ++seq
  try {
    const got = await listRefs(props.kind, props.id)
    if (mine === seq) fetched.value = got
  } catch {
    // 反向链接是附加信息：失败就不显示，不打断详情页。
    if (mine === seq) fetched.value = []
  }
}
watch(() => [props.kind, props.id, props.items] as const, load, { immediate: true })

function kindLabel(kind: string): string {
  switch (kind) {
    case 'job': return t('refs.kind.job')
    case 'plan': return t('refs.kind.plan')
    case 'todo': return t('refs.kind.todo')
    case 'comment': return t('refs.kind.comment')
    case 'mail': return t('refs.kind.mail')
    default: return kind
  }
}

function fieldLabel(field: string): string {
  switch (field) {
    case 'title': return t('refs.field.title')
    case 'description': return t('refs.field.description')
    case 'report': return t('refs.field.report')
    case 'body': return t('refs.field.body')
    default: return t('refs.field.other', { name: field })
  }
}

// 能跳转的来源：job / plan 有详情页，mail 去收件页；todo / comment 没有独立页面，只显示。
function targetOf(r: RefItem): string {
  switch (r.source_kind) {
    case 'job': return `/jobs/${encodeURIComponent(r.source_id)}`
    case 'plan': return `/plans/${encodeURIComponent(r.source_id)}`
    case 'mail': return '/mail'
    default: return ''
  }
}

// 评论的标题是正文，可能很长：压成一行并截断。
function titleOf(r: RefItem): string {
  const raw = (r.source_title || '').replace(/\s+/g, ' ').trim()
  if (!raw) return r.source_id
  return raw.length > 80 ? `${raw.slice(0, 80)}…` : raw
}
</script>

<template>
  <section v-if="rows.length > 0" class="refs-panel" data-test="refs-panel">
    <h3 class="refs-title" data-test="refs-title">{{ t('refs.title', { n: rows.length }) }}</h3>
    <ul class="refs-list">
      <li v-for="r in rows" :key="`${r.source_kind}:${r.source_id}:${r.field}`" class="refs-item" data-test="refs-item">
        <span class="refs-kind mono">{{ kindLabel(r.source_kind) }}</span>
        <RouterLink v-if="targetOf(r)" class="refs-link" data-test="refs-link" :to="targetOf(r)">{{ titleOf(r) }}</RouterLink>
        <span v-else class="refs-link refs-link--plain">{{ titleOf(r) }}</span>
        <span class="refs-field mono">{{ fieldLabel(r.field) }}</span>
        <span class="refs-time mono">{{ fmtAgo(r.created_at) }}</span>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.refs-panel { margin-top: 16px; }
.refs-title { margin: 0 0 8px; font-size: 14px; }
.refs-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.refs-item { display: flex; flex-wrap: wrap; align-items: baseline; gap: 8px; font-size: 13px; }
.refs-kind { padding: 0 6px; border: 1px solid var(--line); border-radius: 4px; font-size: 12px; }
.refs-link { min-width: 0; overflow-wrap: anywhere; }
.refs-field, .refs-time { opacity: 0.7; font-size: 12px; }
</style>
