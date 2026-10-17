<script setup lang="ts">
// 设置 → 权限：谁能做什么。四个页签——授权 / 会话授权 / 权限目录 / 审计。
// 确认规则等纯逻辑在 utils/permissions.ts（有单测）；展开预览调服务端 GET /v1/auth/expand（防抖），
// 展开规则只在服务端一份；这里只管取数与渲染。whoami 来自共享的 store/whoami.ts。
// 管理类接口只有根管理员（auth.manage）能调；其余人与没有根管理员的部署只读，并给出说明。
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import {
  activateAuthGrant,
  createAuthGrant,
  expandGrantPattern,
  getPermissionCatalog,
  listAuthAudit,
  listAuthGrants,
  revokeAuthGrant,
} from '../../api/permissions'
import { fmtAgo, fmtDateTime } from '../../api/time'
import { callerLabel, callerTitle } from '../../utils/callerLabel'
import { refreshWhoami, useWhoami } from '../../store/whoami'
import { createPreviewScheduler } from '../../utils/expandPreview'
import { grantEffectiveLabel } from '../../utils/sessionGrant'
import {
  EXPIRY_OPTIONS,
  auditActionLabel,
  auditNewestFirst,
  canActivate,
  canRevoke,
  emptyExpandResult,
  splitPatterns,
  expandProblemText,
  filterCatalog,
  grantConfirmRules,
  grantFormBlocker,
  grantStateHint,
  grantStateLabel,
  groupGrantsBySubject,
  pendingNewPermissions,
  parseSubject,
  riskTone,
  sessionGrants,
  subjectLabel,
} from '../../utils/permissions'
import type {
  AuthAuditEntry,
  AuthGrant,
  AuthGrantsResp,
  ExpandResult,
  PermissionCatalog,
} from '../../utils/permissions'

type Tab = 'grants' | 'sessions' | 'catalog' | 'audit'
const tabs: { key: Tab; label: string; managed: boolean }[] = [
  { key: 'grants', label: '授权', managed: true },
  { key: 'sessions', label: '会话授权', managed: true },
  { key: 'catalog', label: '权限目录', managed: false },
  { key: 'audit', label: '审计', managed: true },
]
const tab = ref<Tab>('grants')

const { whoami, mode, callerNames } = useWhoami()
const catalog = ref<PermissionCatalog>({ permissions: [], presets: [] })
const grants = ref<AuthGrantsResp>({ version: 0, grants: [], derived: [] })
const audit = ref<AuthAuditEntry[]>([])
const loading = ref(true)
const loadError = ref('')
const actionError = ref('')
const notice = ref('')
const busyId = ref('')

const catalogList = computed(() => catalog.value.permissions)

function msg(e: unknown): string {
  return e instanceof Error ? e.message : String(e)
}

async function loadAll(): Promise<void> {
  loading.value = true
  loadError.value = ''
  try {
    // 打开本页顺手刷新共享的 whoami（权限可能刚被改过）；取不到不拦目录。
    const [, cat] = await Promise.all([refreshWhoami(), getPermissionCatalog()])
    catalog.value = cat
    if (mode.value.canQueryGrants) {
      grants.value = await listAuthGrants()
    }
  } catch (e) {
    loadError.value = msg(e)
  } finally {
    loading.value = false
  }
}

async function reloadGrants(): Promise<void> {
  if (!mode.value.canQueryGrants) return
  try {
    grants.value = await listAuthGrants()
  } catch (e) {
    actionError.value = msg(e)
  }
}

// ---- 授权（按主体分组）----
// 授权列表自带的显示名优先，缺的用 whoami 的补。
const namesOf = computed(() => ({ ...callerNames.value, ...(grants.value.caller_names ?? {}) }))
const groups = computed(() => groupGrantsBySubject(grants.value.grants, grants.value.derived, namesOf.value))
const callerIds = computed(() =>
  groups.value.map((g) => parseSubject(g.subject)).flatMap((p) => (p?.kind === 'caller' ? [p.id] : [])),
)
// 服务端判定的「此刻是否生效」（字段缺失时按 state / 有效期估计）。
function effectiveOf(g: AuthGrant) {
  return grantEffectiveLabel(g, Math.floor(Date.now() / 1000))
}
const showInactive = ref(false)
function visibleGrants(list: AuthGrant[]): AuthGrant[] {
  return showInactive.value ? list : list.filter((g) => g.state !== 'revoked' && g.state !== 'expired')
}

async function onRevoke(g: AuthGrant): Promise<void> {
  busyId.value = g.id
  actionError.value = ''
  notice.value = ''
  try {
    await revokeAuthGrant(g.id)
    notice.value = `已撤销 ${g.pattern ?? g.id}，依赖它的长连接会断开。`
    await reloadGrants()
  } catch (e) {
    actionError.value = msg(e)
  } finally {
    busyId.value = ''
  }
}

async function onActivate(g: AuthGrant): Promise<void> {
  busyId.value = g.id
  actionError.value = ''
  notice.value = ''
  try {
    await activateAuthGrant(g.id)
    notice.value = `已确认恢复 ${g.pattern ?? g.id}。`
    await reloadGrants()
  } catch (e) {
    actionError.value = msg(e)
  } finally {
    busyId.value = ''
  }
}

// ---- 新增授权 ----
const formOpen = ref(false)
const form = reactive({
  kind: 'users' as 'users' | 'caller',
  callerId: '',
  pattern: '',
  note: '',
  confirmed: false,
  expiry: 0,
})
const formSubject = computed(() => (form.kind === 'users' ? 'users' : `caller:${form.callerId.trim()}`))
const expansion = ref<ExpandResult>(emptyExpandResult('empty'))
const previewer = createPreviewScheduler(expandGrantPattern, (r) => { expansion.value = r })
// 模式或授权对象变了就（防抖后）重新问服务端；对象是 users 或某个调用方，展开规则相同。
const previewSubject = computed(() => (form.kind === 'caller' && form.callerId.trim() ? formSubject.value : 'users'))
watch([() => form.pattern, previewSubject], () => previewer.schedule(form.pattern, previewSubject.value))
onUnmounted(() => previewer.cancel())
const confirmRules = computed(() => grantConfirmRules(expansion.value))
const previewPerms = computed(() => expansion.value.permissions)
const previewRules = confirmRules
const previewHint = computed(() => expandProblemText(expansion.value.problem, expansion.value.message))
const blocker = computed(() =>
  grantFormBlocker(
    { subject: formSubject.value, pattern: form.pattern, note: form.note, confirmed: form.confirmed },
    expansion.value,
  ),
)
const saving = ref(false)

// 模式一变，之前的确认作废。
watch(() => form.pattern, () => { form.confirmed = false })

function applyPreset(patterns: string[]): void {
  form.pattern = patterns.join(', ')
}

async function onCreate(): Promise<void> {
  if (blocker.value || saving.value) return
  saving.value = true
  actionError.value = ''
  notice.value = ''
  try {
    // 预设可能有多个模式：一个模式一条授权，保持"存的是模式"。
    for (const pattern of splitPatterns(form.pattern)) {
      await createAuthGrant({
        subject: formSubject.value,
        pattern,
        expires_sec: form.expiry || undefined,
        note: form.note.trim() || undefined,
      })
    }
    notice.value = `已授权 ${form.pattern}。`
    form.pattern = ''
    form.note = ''
    form.confirmed = false
    formOpen.value = false
    await reloadGrants()
  } catch (e) {
    actionError.value = msg(e)
  } finally {
    saving.value = false
  }
}

// ---- 会话授权 ----
const sessions = computed(() => sessionGrants(grants.value.grants))

// ---- 权限目录 ----
const catFilter = reactive({ text: '', risk: '', sessionOnly: false })
const catRows = computed(() => filterCatalog(catalogList.value, catFilter))
const myPerms = computed(() => new Map((whoami.value?.permissions ?? []).map((p) => [p.name, p])))

// ---- 审计 ----
const auditFilter = reactive({ subject: '', permission: '', grant_id: '' })
const auditLoading = ref(false)
async function loadAudit(): Promise<void> {
  if (!mode.value.canQueryGrants) return
  auditLoading.value = true
  actionError.value = ''
  try {
    audit.value = auditNewestFirst(await listAuthAudit({ ...auditFilter, limit: 200 }))
  } catch (e) {
    actionError.value = msg(e)
  } finally {
    auditLoading.value = false
  }
}
watch(tab, (t) => { if (t === 'audit' && audit.value.length === 0) void loadAudit() })
function auditByGrant(id?: string): void {
  if (!id) return
  auditFilter.grant_id = id
  tab.value = 'audit'
  void loadAudit()
}

function expiryText(g: AuthGrant): string {
  if (!g.expires_at) return '长期'
  return g.expires_at * 1000 <= Date.now() ? `已于 ${fmtDateTime(g.expires_at)} 过期` : `至 ${fmtDateTime(g.expires_at)}`
}

onMounted(() => void loadAll())
</script>

<template>
  <section class="perm" data-test="perm-page">
    <header class="who" data-test="perm-who">
      <template v-if="whoami">
        <span class="who-id mono" :title="callerTitle(whoami.caller_id, namesOf)">{{ callerLabel(whoami.caller_id, namesOf) }}</span>
        <span class="who-role mono" :class="{ 'who-role--root': whoami.root }">
          {{ whoami.root ? '根管理员' : mode.canManage ? '权限管理员' : '普通账号' }}
        </span>
        <span class="who-count mono">持有 {{ whoami.permissions.length }} 项权限</span>
      </template>
      <span v-else class="who-count mono">{{ loading ? '读取中…' : '' }}</span>
    </header>

    <p v-if="loadError" class="msg msg--error mono" role="alert">{{ loadError }}</p>
    <p v-if="whoami && mode.readOnly" class="msg msg--warn" data-test="perm-readonly">
      <strong>只读。</strong>{{ mode.reason || whoami.hint }}
    </p>

    <nav class="tabs mono" role="tablist" aria-label="权限">
      <button
        v-for="t in tabs"
        :key="t.key"
        type="button"
        role="tab"
        class="tab"
        :class="{ 'tab--active': tab === t.key }"
        :aria-selected="tab === t.key"
        :disabled="t.managed && !mode.canQueryGrants"
        :data-test="`perm-tab-${t.key}`"
        @click="tab = t.key"
      >
        {{ t.label }}
        <span v-if="t.key === 'grants' && groups.some((g) => g.pending)" class="dot" title="有新权限待确认" />
      </button>
    </nav>

    <p v-if="actionError" class="msg msg--error mono" role="alert">{{ actionError }}</p>
    <p v-if="notice" class="msg msg--ok mono" role="status">{{ notice }}</p>

    <!-- 授权 -->
    <div v-if="tab === 'grants' && mode.canQueryGrants" class="pane" data-test="perm-grants">
      <div class="bar">
        <button type="button" class="btn btn--primary" data-test="perm-new" @click="formOpen = !formOpen">
          {{ formOpen ? '收起' : '新增授权' }}
        </button>
        <label class="check mono"><input v-model="showInactive" type="checkbox" /> 显示已撤销 / 已过期</label>
      </div>

      <form v-if="formOpen" class="form" data-test="perm-form" @submit.prevent="onCreate">
        <div class="row">
          <label class="field">
            <span>授权对象</span>
            <select v-model="form.kind" class="mono" data-test="perm-kind">
              <option value="users">全体用户</option>
              <option value="caller">指定调用方</option>
            </select>
          </label>
          <label v-if="form.kind === 'caller'" class="field">
            <span>调用方 ID</span>
            <input v-model="form.callerId" class="mono" list="perm-callers" placeholder="配置里声明的调用方" data-test="perm-caller" />
            <datalist id="perm-callers"><option v-for="c in callerIds" :key="c" :value="c" /></datalist>
          </label>
          <label class="field">
            <span>有效期</span>
            <select v-model.number="form.expiry" class="mono">
              <option v-for="o in EXPIRY_OPTIONS" :key="o.sec" :value="o.sec">{{ o.label }}</option>
            </select>
          </label>
        </div>

        <div class="presets" data-test="perm-presets">
          <span class="presets-label mono">常用</span>
          <button
            v-for="p in catalog.presets"
            :key="p.key"
            type="button"
            class="chip"
            :title="p.permissions.join('\n')"
            @click="applyPreset(p.patterns)"
          >{{ p.label }}</button>
        </div>

        <label class="field">
          <span>授权模式</span>
          <input v-model="form.pattern" class="mono" placeholder="tunnel.* 、job.*.others、tunnel.open" data-test="perm-pattern" autocomplete="off" />
        </label>

        <div class="preview" data-test="perm-preview" aria-live="polite">
          <p v-if="previewHint" class="preview-hint mono">{{ previewHint }}</p>
          <ul v-if="previewPerms.length" class="manifest">
            <li v-for="p in previewPerms" :key="p.name" class="mf" :class="`risk--${riskTone(p.risk)}`">
              <code>{{ p.name }}</code>
              <span class="risk-chip mono">{{ p.risk_label }}</span>
              <span class="mf-desc">{{ p.description }}</span>
            </li>
          </ul>
          <p v-if="expansion.skippedDangerous.length " class="preview-note mono">
            通配符不会授予危险级：{{ expansion.skippedDangerous.join('、') }}。需要请写出完整权限名。
          </p>
          <p v-if="expansion.skippedRoot " class="preview-note mono">auth.manage 不会被任何模式授予。</p>
        </div>

        <div v-if="previewRules.needsConfirm" class="confirm" data-test="perm-confirm">
          <p>
            <template v-if="previewRules.dangerous.length">
              危险级：<code v-for="n in previewRules.dangerous" :key="n">{{ n }}</code>。
            </template>
            <template v-if="previewRules.high.length">
              高风险：<code v-for="n in previewRules.high" :key="n">{{ n }}</code>。
            </template>
            授权后对象立即可用。
          </p>
          <label class="check"><input v-model="form.confirmed" type="checkbox" data-test="perm-confirm-box" /> 我确认要授予以上权限</label>
        </div>

        <label class="field">
          <span>原因<em v-if="previewRules.needsReason" class="req">（危险级必填）</em><em v-else class="opt">（选填）</em></span>
          <input v-model="form.note" placeholder="为什么要授权" data-test="perm-note" />
        </label>

        <div class="row row--end">
          <span v-if="blocker" class="blocker mono" data-test="perm-blocker">{{ blocker }}</span>
          <button type="submit" class="btn btn--primary" :disabled="!!blocker || saving" data-test="perm-submit">
            {{ saving ? '提交中…' : '授权' }}
          </button>
        </div>
      </form>

      <p v-if="groups.length === 0" class="empty">还没有授权。新增一条，让调用方拿到配置之外的权限。</p>

      <section v-for="g in groups" :key="g.subject" class="group" :data-test="`perm-group-${g.subject}`">
        <h3 class="group-head mono">
          <span>{{ g.label }}</span>
          <span v-if="g.pending" class="tag tag--warn">有新权限待确认 {{ g.pending }}</span>
          <span v-if="g.orphaned" class="tag tag--warn">孤儿 {{ g.orphaned }}</span>
        </h3>

        <article v-for="d in g.derived" :key="d.id" class="grant grant--config">
          <div class="grant-main">
            <span class="tag mono">来自配置</span>
            <span class="grant-perms mono">{{ d.permissions.join('  ') }}</span>
          </div>
          <p class="grant-meta mono">只读，随配置变化</p>
        </article>

        <article
          v-for="gr in visibleGrants(g.grants)"
          :key="gr.id"
          class="grant"
          :class="[`grant--${gr.state}`]"
          :data-test="`perm-grant-${gr.id}`"
        >
          <div class="grant-main">
            <code class="grant-pattern">{{ gr.pattern }}</code>
            <span class="tag mono" :class="{ 'tag--warn': gr.state === 'orphaned' || gr.state === 'unmatched' }">{{ grantStateLabel(gr.state) }}</span>
            <span class="tag mono">{{ gr.source === 'config' ? '来自配置' : '手动授权' }}</span>
            <span class="grant-actions">
              <button v-if="canActivate(gr)" type="button" class="btn" :disabled="busyId === gr.id" data-test="perm-activate" @click="onActivate(gr)">确认恢复</button>
              <button v-if="canRevoke(gr)" type="button" class="btn btn--danger" :disabled="busyId === gr.id" data-test="perm-revoke" @click="onRevoke(gr)">撤销</button>
            </span>
          </div>
          <p v-if="grantStateHint(gr)" class="grant-hint">{{ grantStateHint(gr) }}</p>
          <p v-if="pendingNewPermissions(gr).length" class="grant-hint grant-hint--warn" data-test="perm-pending">
            有新权限待确认：这个模式如今还包含 <code v-for="n in pendingNewPermissions(gr)" :key="n">{{ n }}</code>，它们现在已经生效；不想放行就撤销后重建更窄的模式。
          </p>
          <p class="grant-perms mono">{{ gr.permissions.join('  ') || '无' }}</p>
          <p class="grant-meta mono">
            {{ expiryText(gr) }}
            <template v-if="gr.granted_by"> · 授权人 <span :title="callerTitle(gr.granted_by, namesOf)">{{ callerLabel(gr.granted_by, namesOf) }}</span></template>
            <template v-if="gr.granted_at"> · {{ fmtAgo(gr.granted_at) }}</template>
            <template v-if="gr.use_count"> · 用过 {{ gr.use_count }} 次</template>
            <template v-if="gr.note"> · 原因：{{ gr.note }}</template>
            · <a href="#" @click.prevent="auditByGrant(gr.id)">审计</a>
          </p>
        </article>
      </section>
    </div>

    <!-- 会话授权 -->
    <div v-else-if="tab === 'sessions' && mode.canQueryGrants" class="pane" data-test="perm-sessions">
      <p class="hint">会话授权只对一个终端会话生效，由会话的当前所有者使用，最长 24 小时，会话结束即失效。</p>
      <p v-if="sessions.length === 0" class="empty">没有会话授权。</p>
      <article v-for="g in sessions" :key="g.id" class="grant" :class="[`grant--${g.state}`]" :data-test="`perm-session-${g.id}`">
        <div class="grant-main">
          <code class="grant-pattern">{{ subjectLabel(g.subject, namesOf) }}</code>
          <span class="tag mono" :class="{ 'tag--warn': g.state !== 'active' }">{{ grantStateLabel(g.state) }}</span>
          <span class="tag mono" :class="{ 'tag--warn': !effectiveOf(g).effective }" data-test="perm-session-effective">{{ effectiveOf(g).text }}</span>
          <span class="grant-actions">
            <button v-if="canRevoke(g)" type="button" class="btn btn--danger" :disabled="busyId === g.id" data-test="perm-revoke" @click="onRevoke(g)">撤销</button>
          </span>
        </div>
        <p class="grant-perms mono">{{ g.pattern }} → {{ g.permissions.join('  ') || '无' }}</p>
        <p class="grant-meta mono">
          所有者 <span :title="callerTitle(g.session_owner, namesOf)">{{ callerLabel(g.session_owner, namesOf) || '—' }}</span> · {{ expiryText(g) }}
          <template v-if="g.granted_by"> · 授权人 <span :title="callerTitle(g.granted_by, namesOf)">{{ callerLabel(g.granted_by, namesOf) }}</span></template>
          <template v-if="g.note"> · 原因：{{ g.note }}</template>
          · <a href="#" @click.prevent="auditByGrant(g.id)">审计</a>
        </p>
      </article>
    </div>

    <!-- 权限目录 -->
    <div v-else-if="tab === 'catalog'" class="pane" data-test="perm-catalog">
      <div class="bar">
        <input v-model="catFilter.text" class="mono search" placeholder="搜索权限名或说明" data-test="perm-cat-search" />
        <select v-model="catFilter.risk" class="mono">
          <option value="">全部风险</option>
          <option value="low">低</option>
          <option value="medium">中</option>
          <option value="high">高</option>
          <option value="dangerous">危险</option>
          <option value="root">根</option>
        </select>
        <label class="check mono"><input v-model="catFilter.sessionOnly" type="checkbox" /> 可授予会话</label>
      </div>
      <p v-if="catRows.length === 0" class="empty">没有符合条件的权限。</p>
      <div class="cat">
        <div v-for="p in catRows" :key="p.name" class="cat-row" :class="`risk--${riskTone(p.risk)}`">
          <div class="cat-name">
            <code>{{ p.name }}</code>
            <span class="risk-chip mono">{{ p.risk_label }}</span>
            <span v-if="myPerms.has(p.name)" class="tag tag--ok mono">你已持有</span>
          </div>
          <p class="cat-desc">{{ p.description }}</p>
          <p class="cat-meta mono">
            {{ p.session_grantable ? '可授予会话' : '不可授予会话' }} · 范围 {{ p.scope_label }} · 来源 {{ p.source_label }}
          </p>
        </div>
      </div>
    </div>

    <!-- 审计 -->
    <div v-else-if="tab === 'audit' && mode.canQueryGrants" class="pane" data-test="perm-audit">
      <form class="bar" @submit.prevent="loadAudit">
        <input v-model="auditFilter.subject" class="mono" placeholder="对象 users / caller:x" data-test="perm-audit-subject" />
        <input v-model="auditFilter.permission" class="mono" placeholder="权限" data-test="perm-audit-permission" />
        <input v-model="auditFilter.grant_id" class="mono" placeholder="授权 id" data-test="perm-audit-grant" />
        <button type="submit" class="btn" :disabled="auditLoading">{{ auditLoading ? '查询中…' : '查询' }}</button>
      </form>
      <p v-if="!auditLoading && audit.length === 0" class="empty">没有记录。审计保留 90 天。</p>
      <ol v-else class="audit">
        <li v-for="(a, i) in audit" :key="i" class="audit-row">
          <span class="audit-at mono">{{ fmtDateTime(a.at) }}</span>
          <span class="audit-act">{{ auditActionLabel(a.action) }}</span>
          <span class="audit-detail mono">
            {{ [a.actor, a.subject, a.permission, a.grant_id, a.method && a.path ? `${a.method} ${a.path}` : '', a.status || '', a.reason].filter(Boolean).join(' · ') }}
          </span>
        </li>
      </ol>
    </div>
  </section>
</template>

<style scoped>
.perm { display: grid; gap: 12px; }
.mono { font-family: var(--font-mono); }

/* 身份条：先说"你是谁"，再说能做什么 */
.who { display: flex; align-items: baseline; flex-wrap: wrap; gap: 10px; padding: 10px 14px; border: 1px solid var(--line); border-left: 3px solid var(--queue); border-radius: var(--radius); background: var(--panel); }
.who-id { color: var(--paper); font-size: 14px; }
.who-role { font-size: 12px; color: var(--queue); }
.who-role--root { color: var(--phosphor); }
.who-count { margin-left: auto; font-size: 12px; color: var(--queue); }

.msg { margin: 0; padding: 9px 12px; border: 1px solid var(--line); border-radius: var(--radius); font-size: 13px; overflow-wrap: anywhere; }
.msg--error { color: var(--fail); border-color: var(--fail); }
.msg--warn { color: var(--run); border-color: var(--run); }
.msg--ok { color: var(--phosphor); }

.tabs { display: flex; gap: 2px; border-bottom: 1px solid var(--line); overflow-x: auto; scrollbar-width: thin; }
.tab { flex: none; position: relative; padding: 8px 14px; font: inherit; font-size: 12px; color: var(--queue); background: transparent; border: 0; border-bottom: 2px solid transparent; cursor: pointer; }
.tab:hover:not(:disabled) { color: var(--paper); }
.tab:disabled { opacity: 0.4; cursor: not-allowed; }
.tab--active { color: var(--phosphor); border-bottom-color: var(--phosphor); }
.tab:focus-visible, .btn:focus-visible, .chip:focus-visible { outline: 2px solid var(--phosphor); outline-offset: 2px; }
.dot { display: inline-block; width: 6px; height: 6px; margin-left: 6px; border-radius: 50%; background: var(--run); vertical-align: middle; }

.pane { display: grid; gap: 12px; min-width: 0; }
.bar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.hint, .empty { margin: 0; color: var(--queue); font-size: 13px; }
.empty { padding: 18px 0; }

input, select { background: var(--term-bg); color: var(--paper); border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 8px; font-size: 13px; min-width: 0; }
input:focus-visible, select:focus-visible { outline: 2px solid var(--phosphor); outline-offset: 1px; }
.search { flex: 1 1 200px; }
.btn { font: inherit; font-size: 12px; padding: 6px 12px; color: var(--paper); background: transparent; border: 1px solid var(--line); border-radius: var(--radius); cursor: pointer; }
.btn:hover:not(:disabled) { border-color: var(--phosphor); }
.btn:disabled { opacity: 0.45; cursor: not-allowed; }
.btn--primary { color: var(--ink); background: var(--phosphor); border-color: var(--phosphor); }
.btn--danger { color: var(--fail); border-color: var(--fail); }
.check { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--queue); }

/* 新增表单 */
.form { display: grid; gap: 12px; padding: 14px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); }
.row { display: flex; flex-wrap: wrap; gap: 12px; }
.row--end { align-items: center; justify-content: flex-end; }
.field { display: grid; gap: 4px; font-size: 12px; color: var(--queue); min-width: 0; flex: 1 1 180px; }
.req { color: var(--run); font-style: normal; }
.opt { font-style: normal; }
.presets { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.presets-label { font-size: 12px; color: var(--queue); margin-right: 4px; }
.chip { font: inherit; font-size: 12px; padding: 4px 10px; color: var(--paper); background: transparent; border: 1px solid var(--line); border-radius: 999px; cursor: pointer; }
.chip:hover { border-color: var(--phosphor); color: var(--phosphor); }
.blocker { font-size: 12px; color: var(--run); }

/* 展开预览：每行左侧的色条就是风险档，一眼扫出这条授权里有没有重的 */
.preview { min-height: 40px; padding: 8px; border: 1px dashed var(--line); border-radius: var(--radius); background: var(--term-bg); }
.preview-hint { margin: 0; font-size: 12px; color: var(--queue); }
.preview-note { margin: 6px 0 0; font-size: 12px; color: var(--run); }
.manifest { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; max-height: 260px; overflow: auto; }
.mf, .cat-row { --risk: var(--queue); border-left: 3px solid var(--risk); padding: 4px 8px; }
.mf { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; }
.mf-desc { color: var(--queue); font-size: 12px; flex: 1 1 200px; min-width: 0; }
.risk--low { --risk: var(--queue); }
.risk--medium { --risk: var(--phosphor); }
.risk--high { --risk: var(--run); }
.risk--danger { --risk: var(--fail); }
.risk-chip { font-size: 11px; color: var(--risk); border: 1px solid var(--risk); border-radius: 3px; padding: 0 5px; }
code { font-family: var(--font-mono); font-size: 12.5px; }
.confirm { padding: 10px 12px; border: 1px solid var(--run); border-radius: var(--radius); display: grid; gap: 8px; font-size: 13px; }
.confirm p { margin: 0; }
.confirm code { margin-right: 6px; color: var(--run); }

/* 授权分组 */
.group { display: grid; gap: 8px; }
.group-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin: 8px 0 0; font-size: 13px; font-weight: 600; color: var(--paper); padding-bottom: 4px; border-bottom: 1px solid var(--line); }
.grant { display: grid; gap: 4px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--radius); background: var(--panel); min-width: 0; }
.grant--revoked, .grant--expired { opacity: 0.6; }
.grant--orphaned, .grant--unmatched { border-color: var(--run); }
.grant--config { background: transparent; border-style: dashed; }
.grant-main { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.grant-pattern { color: var(--phosphor); font-size: 13.5px; overflow-wrap: anywhere; }
.grant-actions { margin-left: auto; display: inline-flex; gap: 6px; }
.grant-perms { margin: 0; font-size: 12px; color: var(--paper); overflow-wrap: anywhere; }
.grant-meta { margin: 0; font-size: 11.5px; color: var(--queue); overflow-wrap: anywhere; }
.grant-hint { margin: 0; font-size: 12.5px; color: var(--queue); }
.grant-hint--warn { color: var(--run); }
.grant-hint code { margin: 0 4px; }
.tag { font-size: 11px; color: var(--queue); border: 1px solid var(--line); border-radius: 3px; padding: 0 6px; }
.tag--warn { color: var(--run); border-color: var(--run); }
.tag--ok { color: var(--done); border-color: var(--done); }

/* 目录 */
.cat { display: grid; gap: 6px; }
.cat-row { background: var(--panel); border-top: 1px solid var(--line); border-right: 1px solid var(--line); border-bottom: 1px solid var(--line); border-radius: 0 var(--radius) var(--radius) 0; padding: 8px 12px; min-width: 0; }
.cat-name { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.cat-desc { margin: 2px 0; font-size: 13px; }
.cat-meta { margin: 0; font-size: 11.5px; color: var(--queue); }

/* 审计 */
.audit { list-style: none; margin: 0; padding: 0; display: grid; gap: 0; border: 1px solid var(--line); border-radius: var(--radius); }
.audit-row { display: grid; grid-template-columns: 150px 120px 1fr; gap: 10px; padding: 7px 12px; border-bottom: 1px solid var(--line); font-size: 12.5px; align-items: baseline; }
.audit-row:last-child { border-bottom: 0; }
.audit-at { color: var(--queue); font-size: 11.5px; }
.audit-detail { color: var(--queue); font-size: 11.5px; overflow-wrap: anywhere; }

@media (max-width: 640px) {
  .who-count { margin-left: 0; width: 100%; }
  .audit-row { grid-template-columns: 1fr; gap: 2px; }
  .grant-actions { margin-left: 0; width: 100%; }
  .field { flex-basis: 100%; }
}
@media (prefers-reduced-motion: reduce) {
  * { transition: none !important; animation: none !important; }
}
</style>
