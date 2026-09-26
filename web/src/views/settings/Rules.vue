<script setup lang="ts">
// JOB-06① / WEB-12：/settings/rules —— 强制规则库的管理页。
//
// 规则与技能（/skills）的区别是这一页的全部理由：技能是"按需阅读"的参考资料，agent 可以不读；
// 规则是**必须遵守**的约束，server 在提交时把它拼到 prompt 最前面（标题固定），每个 job 自动带上。
// 所以这里只做三件事：读库（列表）、写库（左编辑右预览）、反查绑定（哪一层在强制它）。
//
// 绑定关系本身不在这里编辑：它是 server.rules / agents.<key>.rules / projects.<key>.rules 三个配置
// 位置，配置页与项目页负责写入；本页只从 GET /v1/config 读回来做反查（不新增端点）。
// 写操作（PUT/DELETE）要 can_admin，服务端的 403/400 原因原样显示在编辑器下方。
import { computed, onMounted, ref } from 'vue'
import MarkdownBlock from '../../components/MarkdownBlock.vue'
import { getConfig } from '../../api/client'
import { deleteRule, getRule, listRules, putRule } from '../../api/rules'
import { fmtAgo, fmtDateTime } from '../../api/time'
import { fmtBytes } from '../../utils/bytes'
import type { ConfigView, Rule } from '../../api/types'

// NEW_RULE_TEMPLATE 是新规则的起始正文：description 决定列表里的"描述"列（库里只索引为首部
// frontmatter 的这一个键），留空也可以保存。
const NEW_RULE_TEMPLATE = '---\ndescription:\n---\n\n'

const rules = ref<Rule[]>([])
const loading = ref(false)
const loadError = ref('')
// actionError / notice：写操作的失败（403 无权限、400 名字非法/正文为空/超过 rules_max_bytes）
// 与成功回执。
const actionError = ref('')
const notice = ref('')

// bindings：规则名 -> 绑定它的位置（server / agent <key> / project <key>），从配置视图反查算出。
// bindError 记配置读取失败——列表仍然可用，只是"被谁绑定"一列没有答案。
const bindings = ref<Record<string, string[]>>({})
const bindError = ref('')

// 编辑器状态：creating=true 时名字可改（新建），否则编辑已存在的那条（名字即路径，不可改）。
const editorOpen = ref(false)
const creating = ref(false)
const editorName = ref('')
const editorText = ref('')
const saving = ref(false)
const saveError = ref('')
// busy 是正在跑的删除（规则名）：按钮据此禁用，避免重复提交。
const busy = ref('')

const hasRules = computed(() => rules.value.length > 0)
// previewText：只渲染正文（frontmatter 是给库看的元数据，注入时不进 prompt，预览也不该显示）。
const previewText = computed(() => stripFrontmatter(editorText.value))

function errorMessage(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

// stripFrontmatter mirrors internal/rule.BodyOf / template.SplitFrontmatter: a leading
// `---` block is metadata, everything after it is the injected text.
function stripFrontmatter(raw: string): string {
  let s = raw.replace(/^[ \t\r\n]+/, '')
  if (!s.startsWith('---')) {
    return s.trim()
  }
  s = s.slice(3)
  const end = s.indexOf('\n---')
  if (end < 0) {
    return raw.trim()
  }
  let rest = s.slice(end + 4)
  const nl = rest.indexOf('\n')
  rest = nl >= 0 ? rest.slice(nl + 1) : ''
  return rest.trim()
}

function boundAt(name: string): string[] {
  return bindings.value[name] ?? []
}

function loadBindings(cfg: ConfigView): Record<string, string[]> {
  const out: Record<string, string[]> = {}
  const add = (rule: string, where: string): void => {
    const list = out[rule]
    if (list) {
      list.push(where)
    } else {
      out[rule] = [where]
    }
  }
  for (const r of cfg.server.rules ?? []) {
    add(r, 'server')
  }
  for (const a of cfg.agents) {
    for (const r of a.rules ?? []) {
      add(r, `agent ${a.key}`)
    }
  }
  for (const p of cfg.projects) {
    for (const r of p.rules ?? []) {
      add(r, `project ${p.key}`)
    }
  }
  return out
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    const resp = await listRules()
    rules.value = resp.rules ?? []
    if (!creating.value && editorName.value && !rules.value.some((r) => r.name === editorName.value)) {
      // 编辑中那条被别处删了：收起编辑器，别让它保存出一个"改回来"的意外。
      closeEditor()
    }
  } catch (e) {
    loadError.value = errorMessage(e)
  } finally {
    loading.value = false
  }
}

// loadConfigOnly 只更新绑定反查所需的配置视图。失败不挡列表：绑定列会写"读取失败"。
async function loadConfigOnly(): Promise<void> {
  bindError.value = ''
  try {
    bindings.value = loadBindings(await getConfig())
  } catch (e) {
    bindings.value = {}
    bindError.value = errorMessage(e)
  }
}

function openCreate(): void {
  creating.value = true
  editorName.value = ''
  editorText.value = NEW_RULE_TEMPLATE
  saveError.value = ''
  actionError.value = ''
  notice.value = ''
  editorOpen.value = true
}

async function openEdit(name: string): Promise<void> {
  creating.value = false
  editorName.value = name
  editorText.value = ''
  saveError.value = ''
  actionError.value = ''
  notice.value = ''
  editorOpen.value = true
  try {
    const detail = await getRule(name)
    // 载入的是**文件原文**（frontmatter 一起），保存时原样回发：页面不认识别的 frontmatter
    // 键（如提示用的 agents:），原样往返才不会把它们弄丢。
    editorText.value = detail.content
  } catch (e) {
    saveError.value = errorMessage(e)
  }
}

function closeEditor(): void {
  editorOpen.value = false
  creating.value = false
  editorName.value = ''
  editorText.value = ''
  saveError.value = ''
}

async function save(): Promise<void> {
  if (saving.value) {
    return
  }
  const name = editorName.value.trim()
  if (name === '') {
    saveError.value = '请填写规则名（小写字母/数字/中划线，如 house-rules）'
    return
  }
  saving.value = true
  saveError.value = ''
  notice.value = ''
  try {
    const saved = await putRule(name, editorText.value)
    notice.value = `已保存规则 ${saved.name}（${fmtBytes(saved.size ?? 0)}${saved.description ? ` · ${saved.description}` : ''}）`
    creating.value = false
    editorName.value = saved.name
    await load()
    await loadConfigOnly()
  } catch (e) {
    // 400（名字非法、正文为空、单条超过 server.rules_max_bytes）与 403 的文案都显示在编辑器下方。
    saveError.value = errorMessage(e)
  } finally {
    saving.value = false
  }
}

async function removeRule(name: string): Promise<void> {
  if (busy.value !== '') {
    return
  }
  const where = boundAt(name)
  const stillBound =
    where.length > 0
      ? `\n\n它仍被绑定：${where.join('、')}。删除后这些绑定会在**提交时报未知规则**（unknown rule "${name}"）——` +
        '请在配置页/项目页或 config.yaml 里一并解绑。'
      : '\n\n库里没有任何绑定指向它。'
  if (!window.confirm(`删除规则「${name}」？规则文件会一起删掉。${stillBound}`)) {
    return
  }
  busy.value = name
  actionError.value = ''
  notice.value = ''
  try {
    await deleteRule(name)
    notice.value = `规则 ${name} 已删除`
    if (editorName.value === name) {
      closeEditor()
    }
    await load()
    await loadConfigOnly()
  } catch (e) {
    actionError.value = `删除 ${name} 失败：${errorMessage(e)}`
  } finally {
    busy.value = ''
  }
}

onMounted(() => {
  void load()
  void loadConfigOnly()
})
</script>

<template>
  <div class="rules">
    <div class="head">
      <h1 class="title mono">RULES</h1>
      <button class="mini-btn mono" type="button" :disabled="loading" @click="load()">
        {{ loading ? '刷新中…' : '刷新' }}
      </button>
      <button class="mini-btn mono" type="button" @click="openCreate()">新建规则</button>
      <span class="poll-hint mono" :class="{ 'poll-hint--on': loading }">●</span>
    </div>

    <p class="scope-note mono">
      规则是<b>必须遵守</b>的约束，每个 job 自动注入 prompt 顶部；长篇参考资料请用
      <RouterLink to="/skills">Skills</RouterLink>。<b>不要在规则里放密钥</b>——
      规则原文会随 job 落进 request_json。库在 serve 主机的 &lt;config-dir&gt;/rules/ 下，写操作需要 can_admin。
    </p>

    <p v-if="loadError" class="error mono">{{ loadError }}</p>
    <p v-if="actionError" class="error mono">{{ actionError }}</p>
    <p v-if="notice" class="notice mono">{{ notice }}</p>
    <p v-if="bindError" class="error mono">绑定反查不可用（读取配置失败）：{{ bindError }}</p>

    <section v-if="editorOpen" class="panel editor-panel">
      <h2 class="panel-title mono">
        {{ creating ? '新建规则' : `编辑 ${editorName}` }}
      </h2>
      <label v-if="creating" class="field field--name">
        <span class="field-name">名称（小写字母/数字/中划线，文件名即 &lt;name&gt;.md）</span>
        <input v-model="editorName" class="input mono" placeholder="house-rules" />
      </label>
      <div class="editor-split">
        <label class="field field--pane">
          <span class="field-name">正文（markdown；首部 frontmatter 的 description: 是列表里的"描述"）</span>
          <textarea v-model="editorText" class="input textarea mono" rows="18" spellcheck="false"></textarea>
        </label>
        <div class="field field--pane">
          <span class="field-name">预览（仅正文，frontmatter 不注入）</span>
          <div class="preview"><MarkdownBlock :text="previewText" /></div>
        </div>
      </div>
      <p v-if="saveError" class="error mono">{{ saveError }}</p>
      <div class="editor-actions">
        <button class="mini-btn mono" type="button" :disabled="saving" @click="save()">
          {{ saving ? '保存中…' : '保存' }}
        </button>
        <button class="mini-btn mono" type="button" :disabled="saving" @click="closeEditor()">取消</button>
        <span class="hint mono">保存走 PUT /v1/rules/&lt;name&gt;：整条替换，校验错误原样显示在这里。</span>
      </div>
    </section>

    <div class="table">
      <div class="thead mono">
        <span>名称</span>
        <span>描述</span>
        <span>大小</span>
        <span>更新时间</span>
        <span>更新人</span>
        <span>被谁绑定</span>
        <span>操作</span>
      </div>

      <div v-for="r in rules" :key="r.name" class="trow">
        <span class="cell-name mono" :title="`sha256 ${r.sha256 ?? '-'}`">{{ r.name }}</span>
        <span class="cell-desc">{{ r.description || '—' }}</span>
        <span class="cell-size mono">{{ fmtBytes(r.size ?? 0) }}</span>
        <span class="cell-time mono" :title="fmtDateTime(r.updated_at)">{{ fmtAgo(r.updated_at) }}</span>
        <span class="cell-by mono">{{ r.updated_by || '—' }}</span>
        <span class="cell-bind">
          <template v-if="boundAt(r.name).length > 0">
            <span
              v-for="w in boundAt(r.name)"
              :key="w"
              class="tag mono"
              :class="{ 'tag--server': w === 'server' }"
              >{{ w }}</span
            >
          </template>
          <span v-else class="muted mono">—</span>
        </span>
        <span class="cell-act">
          <button class="mini-btn mono" type="button" :disabled="busy !== ''" @click="openEdit(r.name)">
            编辑
          </button>
          <button
            class="mini-btn mini-btn--danger mono"
            type="button"
            :disabled="busy !== ''"
            @click="removeRule(r.name)"
          >
            {{ busy === r.name ? '…' : '删除' }}
          </button>
        </span>
      </div>

      <div v-if="!hasRules && !loadError && !loading" class="empty mono">规则库为空</div>
      <div v-if="loading && !hasRules" class="empty mono">加载中…</div>
    </div>

    <p class="hint mono">
      绑定在 <RouterLink to="/settings/config">配置管理</RouterLink>（server / agent 两层）与
      <RouterLink to="/projects">项目</RouterLink>页编辑；项目仓库里的 <code>.gofer/RULES.md</code>
      自动成为该项目的一条规则（名为 <code>project:&lt;key&gt;</code>，随仓库版本走，不在这个库里、不需要绑定）。
      规则总长上限是 server.rules_max_bytes（默认 16KiB），超限的提交被拒。
    </p>
  </div>
</template>

<style scoped>
.head {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 14px;
}
.title {
  font-size: 16px;
  letter-spacing: 0.08em;
  color: var(--paper);
  margin: 0;
}
.head .mini-btn {
  margin-left: 0;
}
.head .mini-btn:first-of-type {
  margin-left: auto;
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--phosphor);
}
.scope-note {
  color: var(--queue);
  font-size: 12px;
  line-height: 1.7;
  margin: 0 0 12px;
}
.scope-note a,
.hint a {
  color: var(--phosphor);
}
.scope-note b {
  color: var(--paper);
}
.error,
.notice {
  font-size: 12px;
  border-radius: var(--radius);
  padding: 8px 10px;
  margin: 0 0 10px;
  word-break: break-word;
}
.error {
  color: var(--fail);
  border: 1px solid var(--fail);
}
.notice {
  color: var(--done);
  border: 1px solid var(--done);
}
.muted {
  color: var(--queue);
}

.panel {
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 14px;
}
.editor-panel {
  margin-bottom: 14px;
}
.panel-title {
  font-size: 13px;
  letter-spacing: 0.04em;
  color: var(--paper);
  margin: 0 0 10px;
}
.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 11px;
}
.field--name {
  margin-bottom: 10px;
}
.editor-split {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
  gap: 12px;
}
.field--pane {
  min-width: 0;
}
.field-name {
  color: var(--queue);
}
.input {
  background: transparent;
  color: var(--paper);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 5px 8px;
  font-family: inherit;
  font-size: 12px;
}
.input:focus {
  border-color: var(--phosphor);
}
.textarea {
  resize: vertical;
  line-height: 1.6;
}
.preview {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 8px 10px;
  min-height: 200px;
  max-height: 420px;
  overflow: auto;
}
.mini-btn {
  background: transparent;
  color: var(--phosphor);
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 4px 10px;
  font-size: 11px;
  cursor: pointer;
}
.mini-btn:hover:not(:disabled) {
  border-color: var(--phosphor);
}
.mini-btn:disabled {
  color: var(--queue);
  cursor: default;
}
.mini-btn--danger {
  color: var(--fail);
}
.mini-btn--danger:hover:not(:disabled) {
  border-color: var(--fail);
}
.editor-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
}
.editor-actions .hint {
  margin: 0;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  overflow: hidden;
}
.thead,
.trow {
  display: grid;
  grid-template-columns: 160px minmax(0, 1fr) 80px 110px 110px 220px 130px;
  align-items: center;
  gap: 12px;
  padding: 9px 14px;
}
.thead {
  background: var(--panel);
  border-bottom: 1px solid var(--line);
  font-size: 11px;
  letter-spacing: 0.06em;
  color: var(--queue);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: 13px;
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--panel);
}
.cell-name {
  color: var(--phosphor);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-desc {
  color: var(--paper);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-size,
.cell-time,
.cell-by {
  color: var(--queue);
  font-size: 12px;
}
.cell-bind {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
}
.tag {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 1px 6px;
  font-size: 11px;
  color: var(--queue);
}
.tag--server {
  color: var(--phosphor);
  border-color: var(--phosphor);
}
.cell-act {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.empty {
  padding: 14px;
  color: var(--queue);
  font-size: 12px;
}
.hint {
  font-size: 11px;
  color: var(--queue);
  margin: 10px 0 0;
  line-height: 1.7;
}
.hint code {
  font-family: var(--font-mono, monospace);
  background: var(--term-bg);
  padding: 1px 5px;
  border-radius: 3px;
}

/* 窄屏：编辑器的左右分栏堆叠（预览在编辑区下方）。 */
@media (max-width: 900px) {
  .editor-split {
    grid-template-columns: minmax(0, 1fr);
  }
}
</style>
