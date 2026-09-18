<script setup lang="ts">
// 轻量 unified diff 渲染器（REV-01 决策 1：自写，不引第三方 diff 库）。
// 输入是后端 changes.diff 的原文（`git diff` 输出）。worktree job 的原文是两段，
// 段标题行（`=== committed (<sha>..HEAD) ===` / `=== uncommitted ===`）各成一组。
//
// 解析：`diff --git` 开文件块 → 收 meta（index / new file / deleted file / rename /
// Binary files，任意顺序，git 的字段行有固定前缀）→ `@@` 开 hunk → `+`/`-`/` ` 为
// 内容行，据此统计每文件 +N −M。
// 上限（决策 1）：原文 > 1MB 或 > 5000 行只渲染前 5000 行，底部给「下载完整 diff」；
// 二进制文件只渲染一行（内容不可读，base85 段整块跳过）。
import { computed, ref } from 'vue'

const props = defineProps<{ text: string; downloadName?: string }>()

const MAX_LINES = 5000
const MAX_BYTES = 1024 * 1024

// git 在每个 hunk 之前给出的字段行前缀（这些行不是文件内容）。
const META_PREFIXES = [
  'index ',
  'new file mode ',
  'deleted file mode ',
  'old mode ',
  'new mode ',
  'similarity index ',
  'dissimilarity index ',
  'rename from ',
  'rename to ',
  'copy from ',
  'copy to ',
  'Binary files ',
  'GIT binary patch',
  '--- ',
  '+++ ',
]

// 段标题行（worktree 两段）：不是 diff 头部也不是内容，单独起一组。
const SECTION_TITLE_RE = /^=== .+ ===$/

interface DiffLine {
  kind: '+' | '-' | ' '
  text: string
}

interface DiffHunk {
  header: string
  lines: DiffLine[]
}

interface DiffFile {
  path: string
  note: string
  binary: boolean
  meta: string[]
  additions: number
  deletions: number
  hunks: DiffHunk[]
}

interface DiffGroup {
  title: string
  files: DiffFile[]
}

function parseDiff(text: string): DiffGroup[] {
  const groups: DiffGroup[] = []
  let group: DiffGroup = { title: '', files: [] }
  groups.push(group)
  let file: DiffFile | null = null
  let hunk: DiffHunk | null = null
  let renameFrom = ''

  for (const raw of text.split('\n')) {
    // CRLF 容错：行内容跟 '\n' 一起 split，残留的 '\r' 会污染 hunk 头匹配。
    const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw

    if (line.startsWith('diff --git ')) {
      // 路径先取头部里的 b/<path>；`---`/`+++`/rename 行随后会覆盖成更准的值。
      const tail = line.indexOf(' b/')
      file = {
        path: tail >= 0 ? line.slice(tail + 3) : line.slice('diff --git '.length),
        note: '',
        binary: false,
        meta: [],
        additions: 0,
        deletions: 0,
        hunks: [],
      }
      group.files.push(file)
      hunk = null
      renameFrom = ''
      continue
    }
    if (line.length === 0) {
      continue
    }
    if (line.startsWith('@@')) {
      if (!file) {
        continue
      }
      hunk = { header: line, lines: [] }
      file.hunks.push(hunk)
      continue
    }
    if (file && file.binary) {
      // 二进制：`Binary files ... differ` 只有一行，`GIT binary patch` 之后是不可读的
      // base85 段——一律不渲染，只等下一个文件或段标题。
      if (SECTION_TITLE_RE.test(line.trim())) {
        group = { title: line.trim(), files: [] }
        groups.push(group)
        file = null
        hunk = null
      }
      continue
    }
    const c = line[0]
    if (hunk && (c === '+' || c === '-' || c === ' ' || c === '\\')) {
      // '\ No newline at end of file' 是标记而非内容，按上下文行淡显。
      hunk.lines.push(c === '\\' ? { kind: ' ', text: line } : { kind: c, text: line.slice(1) })
      if (file) {
        if (c === '+') {
          file.additions += 1
        } else if (c === '-') {
          file.deletions += 1
        }
      }
      continue
    }
    if (file && META_PREFIXES.some((p) => line.startsWith(p))) {
      file.meta.push(line)
      if (line.startsWith('Binary files ') || line.startsWith('GIT binary patch')) {
        file.binary = true
      }
      if (line.startsWith('new file mode')) {
        file.note = '新增'
      }
      if (line.startsWith('deleted file mode')) {
        file.note = '删除'
      }
      if (line.startsWith('rename from ')) {
        renameFrom = line.slice('rename from '.length)
        file.note = '重命名'
      }
      if (line.startsWith('rename to ')) {
        file.path = renameFrom ? `${renameFrom} → ${line.slice('rename to '.length)}` : file.path
      }
      // `--- a/x` 在前、`+++ b/x` 在后；`/dev/null`（新增/删除的另一侧）不覆盖已有路径。
      if (line.startsWith('--- ') && !line.slice(4).startsWith('/dev/null')) {
        file.path = line.slice(4).replace(/^[ab]\//, '')
      }
      if (line.startsWith('+++ ') && !line.slice(4).startsWith('/dev/null')) {
        file.path = line.slice(4).replace(/^[ab]\//, '')
      }
      continue
    }
    // 既不是内容也不是 git 字段行 → 段标题（worktree 的 committed / uncommitted 两段）。
    group = { title: line.trim(), files: [] }
    groups.push(group)
    file = null
    hunk = null
  }

  // 空组（标题后没有文件，或纯空 diff）剔除，免得多渲染一个光杆标题。
  return groups.filter((g) => g.files.length > 0)
}

const lineCount = computed(() =>
  props.text === '' ? 0 : props.text.replace(/\n+$/, '').split('\n').length,
)

// 超过上限只渲染前 5000 行；原文仍在内存里，可原样下载。
const truncated = computed(() => props.text.length > MAX_BYTES || lineCount.value > MAX_LINES)

const groups = computed(() =>
  parseDiff(truncated.value ? props.text.split('\n', MAX_LINES).join('\n') : props.text),
)

const sizeText = computed(() =>
  props.text.length < 1024
    ? `${props.text.length} B`
    : props.text.length < 1048576
      ? `${(props.text.length / 1024).toFixed(1)} KB`
      : `${(props.text.length / 1048576).toFixed(1)} MB`,
)

// 折叠状态按 组:文件 下标记（同一份 diff 内稳定；重新拉取会整体重置）。
const collapsed = ref<Set<string>>(new Set())

function toggleFile(key: string): void {
  const next = new Set(collapsed.value)
  if (next.has(key)) {
    next.delete(key)
  } else {
    next.add(key)
  }
  collapsed.value = next
}

function downloadFull(): void {
  const url = URL.createObjectURL(new Blob([props.text], { type: 'text/plain;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = props.downloadName ?? 'changes.diff'
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
</script>

<template>
  <div class="ud">
    <p v-if="groups.length === 0" class="ud-empty mono">（无 diff 内容）</p>

    <section v-for="(g, gi) in groups" :key="gi" class="ud-group">
      <h3 v-if="g.title" class="ud-group-title mono">{{ g.title }}</h3>

      <div v-for="(f, fi) in g.files" :key="fi" class="ud-file">
        <button
          class="ud-file-head mono"
          type="button"
          :aria-expanded="!collapsed.has(`${gi}:${fi}`)"
          @click="toggleFile(`${gi}:${fi}`)"
        >
          <span class="ud-caret">{{ collapsed.has(`${gi}:${fi}`) ? '▸' : '▾' }}</span>
          <span class="ud-path" :title="f.path">{{ f.path }}</span>
          <span v-if="f.note" class="ud-note">{{ f.note }}</span>
          <span class="ud-counts">
            <span class="ud-add">+{{ f.additions }}</span>
            <span class="ud-del">−{{ f.deletions }}</span>
          </span>
        </button>

        <template v-if="!collapsed.has(`${gi}:${fi}`)">
          <!-- 二进制：只一行说明（内容不可读，不渲染 base85 段）。 -->
          <p v-if="f.binary" class="ud-binary mono">二进制文件 · 不显示内容</p>
          <template v-else>
            <pre v-if="f.meta.length" class="ud-meta mono">{{ f.meta.join('\n') }}</pre>
            <pre
              v-for="(h, hi) in f.hunks"
              :key="hi"
              class="ud-hunk mono"
            ><span class="ud-hunk-head">{{ h.header }}</span><span v-for="(l, li) in h.lines" :key="li" class="ud-line" :class="l.kind === '+' ? 'ud-line--add' : l.kind === '-' ? 'ud-line--del' : ''">{{ l.kind }}{{ l.text }}</span></pre>
          </template>
        </template>
      </div>
    </section>

    <div v-if="truncated" class="ud-truncated">
      <span class="mono">
        已截断：仅渲染前 {{ MAX_LINES }} 行（全文 {{ lineCount }} 行 / {{ sizeText }}）
      </span>
      <button class="ud-dl mono" type="button" @click="downloadFull">下载完整 diff</button>
    </div>
  </div>
</template>

<style scoped>
.ud {
  font-size: 12px;
}
.ud-empty {
  color: var(--queue);
  margin: 0;
}

.ud-group + .ud-group {
  margin-top: 14px;
}
/* 段标题（worktree 的 committed / uncommitted）：把它当小节标题，别和文件头混在一起。 */
.ud-group-title {
  margin: 0 0 6px;
  padding: 3px 8px;
  background: var(--term-bg);
  border-left: 2px solid var(--phosphor);
  color: var(--phosphor);
  font-size: 12px;
  font-weight: 600;
}

.ud-file {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  margin-bottom: 8px;
  overflow: hidden;
}
.ud-file-head {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  background: var(--panel);
  color: var(--paper);
  border: none;
  padding: 6px 8px;
  font-size: 12px;
  text-align: left;
}
.ud-file-head:hover {
  color: var(--phosphor);
}
.ud-caret {
  color: var(--queue);
  flex: none;
}
.ud-path {
  flex: 1 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.ud-note {
  flex: none;
  color: var(--queue);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 0 5px;
  font-size: 11px;
}
.ud-counts {
  flex: none;
  display: inline-flex;
  gap: 6px;
  font-size: 11px;
}
.ud-add {
  color: var(--done);
}
.ud-del {
  color: var(--fail);
}

/* 字段行（index / new file mode / rename / Binary files）：淡色小字，不参与滚动横向对齐。 */
.ud-meta {
  margin: 0;
  padding: 4px 8px;
  color: var(--queue);
  border-bottom: 1px solid var(--line);
  white-space: pre-wrap;
  word-break: break-all;
  font-size: 11px;
}
.ud-binary {
  margin: 0;
  padding: 6px 8px;
  color: var(--queue);
  font-size: 12px;
}

/* hunk：行不换行（diff 的对齐靠列），超宽横向滚动。 */
.ud-hunk {
  margin: 0;
  padding: 0;
  background: var(--term-bg);
  white-space: pre;
  overflow-x: auto;
  font-size: 12px;
  line-height: 1.5;
}
.ud-hunk + .ud-hunk {
  border-top: 1px solid var(--line);
}
.ud-hunk-head {
  display: block;
  padding: 2px 8px;
  color: var(--phosphor);
  background: var(--panel);
}
.ud-line {
  display: block;
  padding: 0 8px;
  color: var(--paper);
}
.ud-line--add {
  background: rgba(91, 166, 110, 0.14);
  color: var(--done);
}
.ud-line--del {
  background: rgba(200, 85, 61, 0.14);
  color: var(--fail);
}

.ud-truncated {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 8px;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  background: var(--panel);
  color: var(--queue);
  font-size: 12px;
}
.ud-dl {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--phosphor);
  border-radius: var(--radius);
  padding: 3px 10px;
  font-size: 12px;
}
.ud-dl:hover {
  background: var(--phosphor);
  color: var(--ink);
}

/* 窄屏（940px 以下）：文件头挤压时先丢计数列的字号优势，路径仍可省略号截断。 */
@media (max-width: 940px) {
  .ud-file-head {
    font-size: 11px;
  }
}
</style>
