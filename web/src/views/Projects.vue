<script setup lang="ts">
// Projects：左列项目 keys（listProjects），选中拉详情（getProject），右列展示配置（只读，无 validate）。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { getProject, listProjects } from '../api/client'
import type { ProjectDetail } from '../api/types'

const router = useRouter()

const keys = ref<string[]>([])
const selected = ref<string>('')
const detail = ref<ProjectDetail | null>(null)
const loadingList = ref(false)
const loadingDetail = ref(false)
const listError = ref('')
const detailError = ref('')

async function loadList() {
  loadingList.value = true
  listError.value = ''
  try {
    const resp = await listProjects()
    keys.value = resp.projects ?? []
    if (keys.value.length > 0) {
      await selectKey(keys.value[0])
    }
  } catch (e) {
    listError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingList.value = false
  }
}

async function selectKey(key: string) {
  selected.value = key
  detail.value = null
  detailError.value = ''
  loadingDetail.value = true
  try {
    detail.value = await getProject(key)
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingDetail.value = false
  }
}

function viewOnBoard(key: string) {
  void router.push({ path: '/board', query: { project: key } })
}

onMounted(() => {
  void loadList()
})
</script>

<template>
  <div class="projects">
    <h1 class="title mono">PROJECTS</h1>
    <p v-if="listError" class="error mono">{{ listError }}</p>

    <div class="layout">
      <!-- 列表 -->
      <ul class="list" aria-label="项目列表">
        <li v-for="key in keys" :key="key">
          <button
            type="button"
            class="list-item mono"
            :class="{ 'list-item--active': key === selected }"
            :title="key"
            @click="selectKey(key)"
          >
            {{ key }}
          </button>
        </li>
        <li v-if="keys.length === 0 && !loadingList && !listError" class="list-empty mono">
          暂无项目
        </li>
        <li v-if="loadingList" class="list-empty mono">加载中…</li>
      </ul>

      <!-- 详情 -->
      <section class="detail" aria-label="项目详情">
        <p v-if="detailError" class="error mono">{{ detailError }}</p>
        <p v-else-if="loadingDetail" class="placeholder mono">加载中…</p>
        <p v-else-if="!detail" class="placeholder mono">选择左侧项目查看配置</p>

        <div v-else class="detail-body">
          <div class="detail-head">
            <span class="detail-key mono">{{ detail.key }}</span>
            <button class="board-link mono" type="button" @click="viewOnBoard(detail.key)">
              在看板查看 &#9656;
            </button>
          </div>

          <dl class="kv">
            <dt class="mono">host_path</dt>
            <dd class="mono">{{ detail.host_path }}</dd>

            <template v-if="detail.container_path">
              <dt class="mono">container_path</dt>
              <dd class="mono">{{ detail.container_path }}</dd>
            </template>

            <dt class="mono">default_agent</dt>
            <dd class="mono">{{ detail.default_agent || '—' }}</dd>

            <dt class="mono">allowed_agents</dt>
            <dd class="mono">
              <template v-if="detail.allowed_agents && detail.allowed_agents.length">
                <span v-for="a in detail.allowed_agents" :key="a" class="tag">{{ a }}</span>
              </template>
              <span v-else>—</span>
            </dd>

            <dt class="mono">allowed_runners</dt>
            <dd class="mono">
              <template v-if="detail.allowed_runners && detail.allowed_runners.length">
                <span v-for="r in detail.allowed_runners" :key="r" class="tag">{{ r }}</span>
              </template>
              <span v-else>—</span>
            </dd>

            <dt class="mono">allow_exec</dt>
            <dd class="mono">
              <span class="flag" :class="detail.allow_exec ? 'flag--yes' : 'flag--no'">
                {{ detail.allow_exec ? '是' : '否' }}
              </span>
            </dd>

            <dt class="mono">max_concurrent_jobs</dt>
            <dd class="mono">{{ detail.max_concurrent_jobs ?? '—' }}</dd>
          </dl>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.projects {
  max-width: 1100px;
  margin: 0 auto;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0 0 14px;
}

.layout {
  display: grid;
  grid-template-columns: 260px 1fr;
  gap: 16px;
  align-items: start;
}

.list {
  list-style: none;
  margin: 0;
  padding: 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.list-item {
  display: block;
  width: 100%;
  text-align: left;
  background: transparent;
  color: var(--paper);
  border: 1px solid transparent;
  border-radius: var(--radius);
  padding: 7px 9px;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.list-item:hover {
  background: var(--ink);
}
.list-item--active {
  color: var(--phosphor);
  border-color: var(--line);
  background: var(--ink);
}
.list-empty {
  color: var(--queue);
  font-size: 12px;
  padding: 6px 9px;
}

.detail {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  padding: 16px 18px;
  min-height: 120px;
}
.placeholder {
  color: var(--queue);
  font-size: 13px;
  margin: 0;
}

.detail-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
  padding-bottom: 12px;
  border-bottom: 1px solid var(--line);
}
.detail-key {
  font-size: 15px;
  color: var(--paper);
  font-weight: 600;
}
.board-link {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 12px;
}
.board-link:hover {
  border-color: var(--phosphor);
}

.kv {
  display: grid;
  grid-template-columns: 160px 1fr;
  gap: 8px 16px;
  margin: 0;
  font-size: 12px;
}
.kv dt {
  color: var(--queue);
  letter-spacing: 0.04em;
}
.kv dd {
  margin: 0;
  color: var(--paper);
  word-break: break-all;
}

.tag {
  display: inline-block;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 7px;
  margin: 0 6px 4px 0;
  color: var(--phosphor);
  font-size: 11px;
}

.flag {
  display: inline-block;
  border-radius: var(--radius);
  padding: 1px 8px;
  font-size: 11px;
  letter-spacing: 0.04em;
}
.flag--yes {
  color: var(--done);
  border: 1px solid var(--done);
}
.flag--no {
  color: var(--queue);
  border: 1px solid var(--line);
}

.error {
  color: var(--fail);
  font-size: 12px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 12px;
  word-break: break-word;
}

@media (max-width: 768px) {
  .layout {
    grid-template-columns: 1fr;
  }
  .kv {
    grid-template-columns: 120px 1fr;
    gap: 6px 10px;
  }
}
</style>
