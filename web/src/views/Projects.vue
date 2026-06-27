<script setup lang="ts">
// Projects：左列项目 keys（listProjects），选中拉详情（getProject），右列展示配置（只读，无 validate）。
// P3 增强（E20/E32）：右列再加 git 状态卡 + 子仓库列表 + 关键文件查看（FilePreview）。
// 刷新策略（D7）：进入项目详情时取 git/repos，git 卡提供手动刷新按钮，不轮询。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  getProject,
  getProjectFile,
  getProjectGit,
  listProjects,
  listRepos,
} from '../api/client'
import type { GitStatus, ProjectDetail, RepoInfo } from '../api/types'
import FilePreview from '../components/FilePreview.vue'

const router = useRouter()

const keys = ref<string[]>([])
const selected = ref<string>('')
const detail = ref<ProjectDetail | null>(null)
const loadingList = ref(false)
const loadingDetail = ref(false)
const listError = ref('')
const detailError = ref('')

// git 状态卡（E20）
const gitStatus = ref<GitStatus | null>(null)
const gitLoading = ref(false)
const gitError = ref('')

// 子仓库列表（E32）
const repos = ref<RepoInfo[]>([])
const reposLoading = ref(false)
const reposError = ref('')

// 关键文件（E32）：白名单候选 → 点击拉内容 → 包 Blob 交 FilePreview 渲染。
const KEY_FILE_CANDIDATES = [
  'README.md',
  'AGENTS.md',
  'CLAUDE.md',
  'go.mod',
  'package.json',
  '.gitignore',
  'LICENSE',
]
const activeFile = ref('') // 当前选中的候选文件名
const fileName = ref('') // 后端回传的 basename（传给 FilePreview 判类型）
const fileBlob = ref<Blob | null>(null) // content 文本包成的 Blob
const fileTruncated = ref(false)
const fileLoading = ref(false)
const fileErr = ref('')

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
  // 切项目：重置 git/repos/关键文件预览状态。
  resetFile()
  try {
    detail.value = await getProject(key)
  } catch (e) {
    detailError.value = e instanceof Error ? e.message : String(e)
  } finally {
    loadingDetail.value = false
  }
  // 详情拉到后并行取 git + repos（失败各自降级，不影响配置展示）。
  void loadGit(key)
  void loadRepos(key)
}

async function loadGit(key: string) {
  gitLoading.value = true
  gitError.value = ''
  gitStatus.value = null
  try {
    gitStatus.value = await getProjectGit(key)
  } catch (e) {
    gitError.value = e instanceof Error ? e.message : String(e)
  } finally {
    gitLoading.value = false
  }
}

async function loadRepos(key: string) {
  reposLoading.value = true
  reposError.value = ''
  repos.value = []
  try {
    const resp = await listRepos(key)
    repos.value = resp.repos ?? []
  } catch (e) {
    reposError.value = e instanceof Error ? e.message : String(e)
  } finally {
    reposLoading.value = false
  }
}

function refreshGit() {
  if (selected.value) {
    void loadGit(selected.value)
  }
}

function resetFile() {
  activeFile.value = ''
  fileName.value = ''
  fileBlob.value = null
  fileTruncated.value = false
  fileErr.value = ''
  fileLoading.value = false
}

async function openFile(name: string) {
  if (!selected.value) {
    return
  }
  activeFile.value = name
  fileLoading.value = true
  fileErr.value = ''
  fileBlob.value = null
  fileName.value = ''
  fileTruncated.value = false
  try {
    const fc = await getProjectFile(selected.value, name)
    // FilePreview 吃 Blob，而 /file 回的是 JSON 文本 → 把 content 包成 Blob 传入。
    fileBlob.value = new Blob([fc.content])
    fileName.value = fc.name
    fileTruncated.value = fc.truncated
  } catch (e) {
    // 文件不存在（404）/ 非白名单（403）/ 二进制（415）等：优雅提示，不破坏页面。
    fileErr.value = e instanceof Error ? e.message : String(e)
  } finally {
    fileLoading.value = false
  }
}

// FilePreview 回退下载（理论上关键文件 ≤256KB 且文本，几乎不触发）：从已载入 Blob 本地另存。
function downloadActiveFile() {
  if (!fileBlob.value) {
    return
  }
  const url = URL.createObjectURL(fileBlob.value)
  const a = document.createElement('a')
  a.href = url
  a.download = fileName.value || activeFile.value
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// Unix 秒 → 本地可读时间。
function fmtTs(ts: number): string {
  if (!ts) {
    return ''
  }
  return new Date(ts * 1000).toLocaleString()
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

          <!-- git 状态卡（E20） -->
          <section class="block" aria-label="git 状态">
            <div class="block-head">
              <h2 class="block-title mono">GIT 状态</h2>
              <button
                class="mini-btn mono"
                type="button"
                :disabled="gitLoading"
                @click="refreshGit"
              >
                {{ gitLoading ? '刷新中…' : '刷新' }}
              </button>
            </div>

            <p v-if="gitError" class="error mono">{{ gitError }}</p>
            <p v-else-if="gitLoading && !gitStatus" class="placeholder mono">加载中…</p>
            <p
              v-else-if="gitStatus && !gitStatus.is_git_repo"
              class="placeholder mono"
            >
              非 git 仓 / 非本地可达
            </p>
            <div v-else-if="gitStatus" class="git-body">
              <div class="git-line mono">
                <span class="git-branch">{{ gitStatus.branch || '—' }}</span>
                <span
                  class="flag"
                  :class="gitStatus.dirty ? 'flag--no' : 'flag--yes'"
                >
                  {{ gitStatus.dirty ? 'dirty' : 'clean' }}
                </span>
              </div>
              <ul class="commits" v-if="gitStatus.recent_commits.length">
                <li
                  v-for="c in gitStatus.recent_commits"
                  :key="c.hash"
                  class="commit mono"
                >
                  <span class="commit-hash">{{ c.hash }}</span>
                  <span class="commit-subject" :title="c.subject">{{ c.subject }}</span>
                  <span class="commit-meta">{{ c.author }} · {{ fmtTs(c.ts) }}</span>
                </li>
              </ul>
              <p v-else class="placeholder mono">无提交记录</p>
            </div>
          </section>

          <!-- 子仓库列表（E32） -->
          <section class="block" aria-label="子仓库">
            <div class="block-head">
              <h2 class="block-title mono">子仓库</h2>
            </div>
            <p v-if="reposError" class="error mono">{{ reposError }}</p>
            <p v-else-if="reposLoading" class="placeholder mono">加载中…</p>
            <p v-else-if="repos.length === 0" class="placeholder mono">未发现 git 仓</p>
            <ul v-else class="repos">
              <li v-for="r in repos" :key="r.rel_path" class="repo mono">
                <span class="repo-path">{{ r.rel_path }}</span>
                <span class="repo-branch">{{ r.branch || '—' }}</span>
                <span class="flag" :class="r.dirty ? 'flag--no' : 'flag--yes'">
                  {{ r.dirty ? 'dirty' : 'clean' }}
                </span>
              </li>
            </ul>
          </section>

          <!-- 关键文件（E32） -->
          <section class="block" aria-label="关键文件">
            <div class="block-head">
              <h2 class="block-title mono">关键文件</h2>
            </div>
            <div class="file-tabs">
              <button
                v-for="name in KEY_FILE_CANDIDATES"
                :key="name"
                type="button"
                class="file-tab mono"
                :class="{ 'file-tab--active': name === activeFile }"
                @click="openFile(name)"
              >
                {{ name }}
              </button>
            </div>

            <div v-if="activeFile" class="file-view">
              <p v-if="fileLoading" class="placeholder mono">加载中…</p>
              <p v-else-if="fileErr" class="error mono">{{ fileErr }}</p>
              <template v-else-if="fileBlob">
                <p v-if="fileTruncated" class="truncated mono">
                  文件超过 256KB，已截断显示
                </p>
                <FilePreview
                  :name="fileName"
                  :blob="fileBlob"
                  @download="downloadActiveFile"
                />
              </template>
            </div>
            <p v-else class="placeholder mono">点击上方文件查看内容</p>
          </section>
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

/* P3 三块：git 状态卡 / 子仓库 / 关键文件。 */
.block {
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px solid var(--line);
}
.block-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 10px;
}
.block-title {
  font-size: 12px;
  letter-spacing: 0.08em;
  color: var(--queue);
  margin: 0;
}
.mini-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.mini-btn:disabled {
  color: var(--queue);
  cursor: default;
}

/* git 状态卡 */
.git-line {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
  margin-bottom: 10px;
}
.git-branch {
  color: var(--phosphor);
  font-weight: 600;
}
.commits {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.commit {
  display: grid;
  grid-template-columns: 72px 1fr;
  gap: 2px 10px;
  font-size: 11px;
  padding: 5px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.commit-hash {
  color: var(--phosphor);
}
.commit-subject {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.commit-meta {
  grid-column: 2;
  color: var(--queue);
}

/* 子仓库列表 */
.repos {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.repo {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 12px;
  padding: 5px 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
}
.repo-path {
  color: var(--paper);
  flex: 1;
  word-break: break-all;
}
.repo-branch {
  color: var(--phosphor);
}

/* 关键文件 */
.file-tabs {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 12px;
}
.file-tab {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 11px;
}
.file-tab:hover {
  border-color: var(--phosphor);
}
.file-tab--active {
  color: var(--phosphor);
  border-color: var(--phosphor);
  background: var(--ink);
}
.file-view {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px;
  background: var(--ink);
}
.truncated {
  color: var(--queue);
  font-size: 11px;
  margin: 0 0 8px;
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
