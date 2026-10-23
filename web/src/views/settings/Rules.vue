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
// UI 重设计 P5-d：编辑器改用 FormSection，文案走 i18n（settings.rules.*）；表格结构不动。
import { computed, onMounted, ref } from 'vue'
import MarkdownBlock from '../../components/MarkdownBlock.vue'
import { getConfig } from '../../api/client'
import { deleteRule, getRule, listRules, putRule } from '../../api/rules'
import { fmtAgo, fmtDateTime } from '../../api/time'
import { fmtBytes } from '../../utils/bytes'
import { t } from '../../i18n'
import FormSection from '../../ui/FormSection.vue'
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
    saveError.value = t('settings.rules.errName')
    return
  }
  saving.value = true
  saveError.value = ''
  notice.value = ''
  try {
    const saved = await putRule(name, editorText.value)
    notice.value = t('settings.rules.saved', {
      name: saved.name,
      size: fmtBytes(saved.size ?? 0),
      desc: saved.description ? ` · ${saved.description}` : '',
    })
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
      ? '\n\n' + t('settings.rules.stillBound', { where: where.join('、'), name })
      : '\n\n' + t('settings.rules.notBound')
  if (!window.confirm(t('settings.rules.confirmDelete', { name, bound: stillBound }))) {
    return
  }
  busy.value = name
  actionError.value = ''
  notice.value = ''
  try {
    await deleteRule(name)
    notice.value = t('settings.rules.deleted', { name })
    if (editorName.value === name) {
      closeEditor()
    }
    await load()
    await loadConfigOnly()
  } catch (e) {
    actionError.value = t('settings.rules.deleteFailed', { name, error: errorMessage(e) })
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
    <header class="page-head">
      <div class="grow">
        <h1>{{ t('settings.rules.title') }}</h1>
        <p class="sub">{{ t('settings.rules.sub') }}</p>
      </div>
      <div class="head-actions">
        <span class="poll-hint" :class="{ 'poll-hint--on': loading }" aria-hidden="true">●</span>
        <button class="btn" type="button" :disabled="loading" @click="load()">
          {{ loading ? t('settings.rules.refreshing') : t('act.refresh') }}
        </button>
        <button class="btn btn--primary" type="button" @click="openCreate()">
          {{ t('settings.rules.new') }}
        </button>
      </div>
    </header>

    <p class="scope-note">
      {{ t('settings.rules.scopeBefore') }}
      <RouterLink to="/skills">Skills</RouterLink>{{ t('settings.rules.scopeAfter') }}
    </p>

    <p v-if="loadError" class="msg msg--err">{{ loadError }}</p>
    <p v-if="actionError" class="msg msg--err">{{ actionError }}</p>
    <p v-if="notice" class="msg msg--ok">{{ notice }}</p>
    <p v-if="bindError" class="msg msg--err">
      {{ t('settings.rules.bindError', { error: bindError }) }}
    </p>

    <FormSection
      v-if="editorOpen"
      :title="creating ? t('settings.rules.editorCreate') : t('settings.rules.editorEdit', { name: editorName })"
      :desc="t('settings.rules.editorDesc')"
    >
      <label v-if="creating" class="field field--name">
        <span class="field-name">{{ t('settings.rules.nameLabel') }}</span>
        <input v-model="editorName" class="input mono" placeholder="house-rules" />
      </label>
      <div class="editor-split">
        <label class="field field--pane">
          <span class="field-name">{{ t('settings.rules.bodyLabel') }}</span>
          <textarea v-model="editorText" class="input textarea mono" rows="18" spellcheck="false"></textarea>
        </label>
        <div class="field field--pane">
          <span class="field-name">{{ t('settings.rules.previewLabel') }}</span>
          <div class="preview"><MarkdownBlock :text="previewText" /></div>
        </div>
      </div>
      <p v-if="saveError" class="msg msg--err">{{ saveError }}</p>
      <div class="editor-actions">
        <button class="btn btn--primary" type="button" :disabled="saving" @click="save()">
          {{ saving ? t('settings.saving') : t('act.save') }}
        </button>
        <button class="btn" type="button" :disabled="saving" @click="closeEditor()">
          {{ t('act.cancel') }}
        </button>
        <span class="hint">{{ t('settings.rules.saveHint') }}</span>
      </div>
    </FormSection>

    <!-- 表格包在可横向滚动的容器里（手机档列多，压缩到不可读不如让它滚）。 -->
    <div class="table-wrap">
      <div class="table">
        <div class="thead">
          <span>{{ t('settings.rules.col.name') }}</span>
          <span>{{ t('settings.rules.col.desc') }}</span>
          <span>{{ t('settings.rules.col.size') }}</span>
          <span>{{ t('settings.rules.col.updated') }}</span>
          <span>{{ t('settings.rules.col.by') }}</span>
          <span>{{ t('settings.rules.col.bound') }}</span>
          <span>{{ t('settings.rules.col.actions') }}</span>
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
          <button class="btn btn--mini" type="button" :disabled="busy !== ''" @click="openEdit(r.name)">
            {{ t('settings.rules.edit') }}
          </button>
          <button
            class="btn btn--mini btn--danger"
            type="button"
            :disabled="busy !== ''"
            @click="removeRule(r.name)"
          >
            {{ busy === r.name ? '…' : t('settings.rules.delete') }}
          </button>
        </span>
      </div>

      <div v-if="!hasRules && !loadError && !loading" class="empty" data-testid="rules-empty">
        {{ t('settings.rules.empty') }}
      </div>
      <div v-if="loading && !hasRules" class="empty">{{ t('settings.rules.loading') }}</div>
      </div>
    </div>

    <p class="hint">
      {{ t('settings.rules.footerBefore') }}
      <RouterLink to="/settings/config">{{ t('settings.nav.config') }}</RouterLink>
      {{ t('settings.rules.footerMid') }}
      <RouterLink to="/projects">{{ t('settings.nav.projects') }}</RouterLink>
      {{ t('settings.rules.footerAfter') }}
    </p>
  </div>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  margin-bottom: 12px;
}
.page-head .grow {
  flex: 1;
  min-width: 0;
}
.page-head h1 {
  margin: 0 0 4px;
  font-size: var(--fs-xl);
  color: var(--ink);
}
.sub {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
}
.head-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.poll-hint {
  color: var(--line);
  font-size: 10px;
  transition: color 0.2s;
}
.poll-hint--on {
  color: var(--accent);
}
.scope-note {
  color: var(--ink-3);
  font-size: var(--fs-sm);
  line-height: 1.7;
  margin: 0 0 12px;
}
.scope-note a,
.hint a {
  color: var(--accent);
}

.msg {
  font-size: var(--fs-sm);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  margin: 0 0 10px;
  word-break: break-word;
}
.msg--err {
  color: var(--fail);
  border: 1px solid var(--fail);
}
.msg--ok {
  color: var(--accent);
  border: 1px solid var(--accent);
}
.muted {
  color: var(--ink-3);
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: var(--fs-xs);
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
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.input {
  background: var(--sunken);
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 6px 8px;
  font-family: inherit;
  font-size: var(--fs-sm);
}
.input:focus {
  border-color: var(--accent);
}
.textarea {
  resize: vertical;
  line-height: 1.6;
}
.preview {
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 8px 10px;
  min-height: 200px;
  max-height: 420px;
  overflow: auto;
  background: var(--surface);
}

.btn {
  background: transparent;
  color: var(--ink);
  border: 1px solid var(--line);
  border-radius: var(--r-ctl);
  padding: 6px 12px;
  font: inherit;
  font-size: var(--fs-sm);
}
.btn:hover:not(:disabled) {
  border-color: var(--line-strong);
}
.btn:disabled {
  opacity: 0.5;
  cursor: default;
}
.btn--primary {
  color: var(--accent-ink);
  background: var(--accent);
  border-color: var(--accent);
  font-weight: 600;
}
.btn--mini {
  padding: 4px 10px;
  font-size: var(--fs-xs);
}
.btn--danger {
  color: var(--fail);
}
.btn--danger:hover:not(:disabled) {
  border-color: var(--fail);
}
.editor-actions {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
  flex-wrap: wrap;
}
.editor-actions .hint {
  margin: 0;
}

.table {
  border: 1px solid var(--line);
  border-radius: var(--r-panel);
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
  background: var(--surface-2);
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-xs);
  letter-spacing: 0.06em;
  color: var(--ink-3);
  text-transform: uppercase;
}
.trow {
  border-bottom: 1px solid var(--line);
  font-size: var(--fs-md);
}
.trow:last-child {
  border-bottom: none;
}
.trow:hover {
  background: var(--surface);
}
.cell-name {
  color: var(--accent);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-desc {
  color: var(--ink);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.cell-size,
.cell-time,
.cell-by {
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.cell-bind {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  min-width: 0;
}
.tag {
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  padding: 1px 6px;
  font-size: var(--fs-xs);
  color: var(--ink-2);
}
.tag--server {
  color: var(--accent);
  border-color: var(--accent);
}
.cell-act {
  display: flex;
  gap: 6px;
  justify-content: flex-end;
}
.empty {
  padding: 14px;
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
.hint {
  font-size: var(--fs-sm);
  color: var(--ink-3);
  margin: 12px 0 0;
  line-height: 1.7;
}
.hint code {
  font-family: var(--font-mono);
  background: var(--sunken);
  padding: 1px 5px;
  border-radius: 3px;
}

/* 窄屏：编辑器的左右分栏堆叠（预览在编辑区下方）。 */
@media (max-width: 900px) {
  .editor-split {
    grid-template-columns: minmax(0, 1fr);
  }
  /* 手机档：表格改为横向滚动（列多，同 Tunnels 的做法），表头与行一起滚。 */
  .table-wrap {
    overflow-x: auto;
  }
  .table-wrap .table {
    min-width: 860px;
  }
  .page-head {
    flex-direction: column;
  }
}
</style>
