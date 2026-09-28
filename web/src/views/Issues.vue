<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { listTrackerIssues } from '../api/client'
import type { TrackerIssue } from '../api/types'

const trackerId = ref('')
const query = ref('')
const issues = ref<TrackerIssue[]>([])
const loading = ref(false)
const error = ref('')

const filtered = computed(() => issues.value.filter((item) => {
  if (!query.value) return true
  return item.id.toLowerCase().includes(query.value.toLowerCase()) || item.body_json.toLowerCase().includes(query.value.toLowerCase())
}))

async function load() {
  if (!trackerId.value) return
  loading.value = true; error.value = ''
  try { issues.value = (await listTrackerIssues(trackerId.value)).issues ?? [] } catch (e) { error.value = e instanceof Error ? e.message : String(e) } finally { loading.value = false }
}

onMounted(() => { trackerId.value = localStorage.getItem('gofer.tracker_id') ?? ''; void load() })
</script>

<template>
  <main class="page issues-page">
    <header class="page-header"><div><h1>Issues</h1><p class="muted">Tracker mirror issues</p></div><button class="btn" :disabled="loading" @click="load">刷新</button></header>
    <section class="card issue-toolbar"><input v-model="trackerId" placeholder="tracker_id" @keyup.enter="load"><input v-model="query" placeholder="搜索 ID 或内容"></section>
    <p v-if="error" class="error">{{ error }}</p><p v-else-if="loading" class="muted">加载中…</p>
    <section v-else class="card issue-list"><div v-for="issue in filtered" :key="issue.id" class="issue-row"><strong>{{ issue.id }}</strong><span class="mono">rev {{ issue.rev }}</span><pre>{{ issue.body_json }}</pre></div><p v-if="filtered.length === 0" class="muted">暂无 issue</p></section>
  </main>
</template>
