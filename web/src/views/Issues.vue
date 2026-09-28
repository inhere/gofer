<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { listTrackerIssues, getTrackerIssue, updateTrackerIssue, commentTrackerIssue } from '../api/client'
import type { TrackerIssue, TrackerIssueView } from '../api/types'

const trackerId = ref('')
const query = ref('')
const issues = ref<TrackerIssue[]>([])
const loading = ref(false)
const route = useRoute()
const error = ref('')
const status = ref(''); const type = ref(''); const tag = ref(''); const selected = ref<TrackerIssueView|null>(null); const editTitle=ref(''); const comment=ref('')

const filtered = computed(() => issues.value.filter((item) => {
  let body:any = {}; try { body=JSON.parse(item.body_json) } catch {}
  if (status.value && body.status !== status.value) return false
  if (type.value && body.type !== type.value) return false
  if (tag.value && !(body.tags ?? []).includes(tag.value)) return false
  if (!query.value) return true
  return item.id.toLowerCase().includes(query.value.toLowerCase()) || item.body_json.toLowerCase().includes(query.value.toLowerCase())
}))

async function load() {
  if (!trackerId.value) return
  loading.value = true; error.value = ''
  try { issues.value = (await listTrackerIssues(trackerId.value)).issues ?? [] } catch (e) { error.value = e instanceof Error ? e.message : String(e) } finally { loading.value = false }
}
async function openIssue(id:string){ selected.value=await getTrackerIssue(trackerId.value,id); editTitle.value=selected.value.title }
async function saveIssue(){ if(!selected.value)return; await updateTrackerIssue(trackerId.value,selected.value.id,{...selected.value,title:editTitle.value}); await openIssue(selected.value.id); await load() }
async function addComment(){ if(!selected.value||!comment.value)return; await commentTrackerIssue(trackerId.value,selected.value.id,comment.value); comment.value=''; await openIssue(selected.value.id) }

onMounted(async () => { trackerId.value = localStorage.getItem('gofer.tracker_id') ?? ''; await load(); const wanted=String(route.query.issue||''); if(wanted) await openIssue(wanted) })
</script>

<template>
  <main class="page issues-page">
    <header class="page-header"><div><h1>Issues</h1><p class="muted">Tracker mirror issues</p></div><button class="btn" :disabled="loading" @click="load">刷新</button></header>
    <section class="card issue-toolbar"><input v-model="trackerId" placeholder="tracker_id" @keyup.enter="load"><input v-model="query" placeholder="搜索 ID 或内容"><input v-model="status" placeholder="状态"><input v-model="type" placeholder="类型"><input v-model="tag" placeholder="标签"></section>
    <p v-if="error" class="error">{{ error }}</p><p v-else-if="loading" class="muted">加载中…</p>
    <section v-else class="card issue-list"><div v-for="issue in filtered" :key="issue.id" class="issue-row" @click="openIssue(issue.id)"><strong>{{ issue.id }}</strong><span class="mono">rev {{ issue.rev }}</span><pre>{{ issue.body_json }}</pre></div><p v-if="filtered.length === 0" class="muted">暂无 issue</p></section>
    <section v-if="selected" class="card issue-detail"><h2>{{ selected.id }}</h2><input v-model="editTitle"><button class="btn" @click="saveIssue">保存</button><p>{{ selected.description }}</p><ul><li v-for="c in selected.comments" :key="c.at">{{ c.by }}: {{ c.text }}</li></ul><input v-model="comment" placeholder="评论"><button class="btn" @click="addComment">评论</button></section>
  </main>
</template>
