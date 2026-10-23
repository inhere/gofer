<script setup lang="ts">
// 设置 → 权限：谁能做什么。四个页签——授权 / 会话授权 / 权限目录 / 审计。
// 确认规则等纯逻辑在 utils/permissions.ts（有单测）；展开预览调服务端 GET /v1/auth/expand（防抖），
// 展开规则只在服务端一份；这里只管取数与渲染。whoami 来自共享的 store/whoami.ts。
// 管理类接口只有根管理员（auth.manage）能调；其余人与没有根管理员的部署只读，并给出说明。
// UI 重设计 P5-d：页头 + 四个页签的文案走 i18n（settings.perm.*）；权限名、风险/范围/来源等
// 来自服务端与 utils/permissions.ts 的标签保持原样。
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
import GrantRenewDialog from '../../components/GrantRenewDialog.vue'
import { fmtAgo, fmtDateTime } from '../../api/time'
import { callerLabel, callerTitle } from '../../utils/callerLabel'
import { refreshWhoami, useWhoami } from '../../store/whoami'
import { ceilingReadOnlyNotice, ceilingSummary } from '../../utils/ceiling'
import { createPreviewScheduler } from '../../utils/expandPreview'
import { t } from '../../i18n'
import { canRenewGrant, grantRemainingText, grantRenewState } from '../../utils/grantRenew'
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
  { key: 'grants', label: 'settings.perm.tab.grants', managed: true },
  { key: 'sessions', label: 'settings.perm.tab.sessions', managed: true },
  { key: 'catalog', label: 'settings.perm.tab.catalog', managed: false },
  { key: 'audit', label: 'settings.perm.tab.audit', managed: true },
]
const tab = ref<Tab>('grants')

const { whoami, mode, callerNames, ceiling, readOnly } = useWhoami()
// 本人调用方上限（ceiling）摘要：这一页也是「我为什么做不了某件事」的落地点。
// 上限由配置 server.callers[].ceiling 给定，判定文案与后端 internal/config/ceiling.go 同口径。
const ceilingRows = computed(() => ceilingSummary(ceiling.value))
const ceilingNotice = computed(() => (readOnly.value ? ceilingReadOnlyNotice() : ''))
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
    notice.value = t('settings.perm.revoked', { pattern: g.pattern ?? g.id })
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
    notice.value = t('settings.perm.activated', { pattern: g.pattern ?? g.id })
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
onUnmounted(() => {
  previewer.cancel()
  window.clearInterval(clock)
})
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
    notice.value = t('settings.perm.created', { pattern: form.pattern })
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
// 续期：生效中的行显示剩余时间与「续期」，未生效的行说明要重新授权。nowSec 每 30 秒走一次，
// 剩余时间与「临近到期」的提示会自己更新。
const nowSec = ref(Math.floor(Date.now() / 1000))
let clock = 0
const renewTarget = ref<AuthGrant | null>(null)
function renewStateOf(g: AuthGrant) {
  return grantRenewState(g, nowSec.value)
}
function remainsOf(g: AuthGrant): string {
  return grantRemainingText(g.expires_at, nowSec.value)
}
function onRenewed(): void {
  actionError.value = ''
  notice.value = t('settings.perm.renewed')
  void reloadGrants()
}

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
  if (!g.expires_at) return t('settings.perm.expiryForever')
  return g.expires_at * 1000 <= Date.now()
    ? t('settings.perm.expiredAt', { at: fmtDateTime(g.expires_at) })
    : t('settings.perm.untilAt', { at: fmtDateTime(g.expires_at) })
}

onMounted(() => {
  void loadAll()
  clock = window.setInterval(() => { nowSec.value = Math.floor(Date.now() / 1000) }, 30000)
})
</script>

<template>
  <section class="perm" data-test="perm-page">
    <header class="page-head">
      <h1>{{ t('settings.perm.title') }}</h1>
      <p class="sub">{{ t('settings.perm.sub') }}</p>
    </header>

    <header class="who" data-test="perm-who">
      <template v-if="whoami">
        <span class="who-id mono" :title="callerTitle(whoami.caller_id, namesOf)">{{ callerLabel(whoami.caller_id, namesOf) }}</span>
        <span class="who-role mono" :class="{ 'who-role--root': whoami.root }">
          {{ whoami.root ? t('settings.perm.role.root') : mode.canManage ? t('settings.perm.role.admin') : t('settings.perm.role.user') }}
        </span>
        <span class="who-count mono">{{ t('settings.perm.held', { n: whoami.permissions.length }) }}</span>
      </template>
      <span v-else class="who-count mono">{{ loading ? t('settings.perm.reading') : '' }}</span>
    </header>

    <div v-if="ceilingRows.length" class="ceiling-box" data-test="perm-ceiling">
      <p class="ceiling-title mono">
        {{ t('settings.perm.ceilingTitle') }}
        <span v-if="readOnly" class="ceiling-ro mono">{{ t('settings.perm.readonlyTag') }}</span>
      </p>
      <ul class="ceiling-list mono">
        <li v-for="line in ceilingRows" :key="line">{{ line }}</li>
      </ul>
      <p v-if="ceilingNotice" class="ceiling-note mono">{{ ceilingNotice }}</p>
    </div>

    <p v-if="loadError" class="msg msg--error mono" role="alert">{{ loadError }}</p>
    <p v-if="whoami && mode.readOnly" class="msg msg--warn" data-test="perm-readonly">
      <strong>{{ t('settings.perm.readonlyStrong') }}</strong>{{ mode.reason || whoami.hint }}
    </p>

    <nav class="tabs mono" role="tablist" :aria-label="t('settings.nav.permissions')">
      <button
        v-for="tb in tabs"
        :key="tb.key"
        type="button"
        role="tab"
        class="tab"
        :class="{ 'tab--active': tab === tb.key }"
        :aria-selected="tab === tb.key"
        :disabled="tb.managed && !mode.canQueryGrants"
        :data-test="`perm-tab-${tb.key}`"
        @click="tab = tb.key"
      >
        {{ t(tb.label) }}
        <span v-if="tb.key === 'grants' && groups.some((g) => g.pending)" class="dot" :title="t('settings.perm.pendingTitle')" />
      </button>
    </nav>

    <p v-if="actionError" class="msg msg--error mono" role="alert">{{ actionError }}</p>
    <p v-if="notice" class="msg msg--ok mono" role="status">{{ notice }}</p>

    <!-- 授权 -->
    <div v-if="tab === 'grants' && mode.canQueryGrants" class="pane" data-test="perm-grants">
      <div class="bar">
        <button type="button" class="btn btn--primary" data-test="perm-new" @click="formOpen = !formOpen">
          {{ formOpen ? t('settings.perm.collapse') : t('settings.perm.newGrant') }}
        </button>
        <label class="check mono"><input v-model="showInactive" type="checkbox" /> {{ t('settings.perm.showInactive') }}</label>
      </div>

      <form v-if="formOpen" class="form" data-test="perm-form" @submit.prevent="onCreate">
        <div class="row">
          <label class="field">
            <span>{{ t('settings.perm.field.subject') }}</span>
            <select v-model="form.kind" class="mono" data-test="perm-kind">
              <option value="users">{{ t('settings.perm.subject.users') }}</option>
              <option value="caller">{{ t('settings.perm.subject.caller') }}</option>
            </select>
          </label>
          <label v-if="form.kind === 'caller'" class="field">
            <span>{{ t('settings.perm.field.callerId') }}</span>
            <input v-model="form.callerId" class="mono" list="perm-callers" :placeholder="t('settings.perm.callerPlaceholder')" data-test="perm-caller" />
            <datalist id="perm-callers"><option v-for="c in callerIds" :key="c" :value="c" /></datalist>
          </label>
          <label class="field">
            <span>{{ t('settings.perm.field.expiry') }}</span>
            <select v-model.number="form.expiry" class="mono">
              <option v-for="o in EXPIRY_OPTIONS" :key="o.sec" :value="o.sec">{{ o.label }}</option>
            </select>
          </label>
        </div>

        <div class="presets" data-test="perm-presets">
          <span class="presets-label mono">{{ t('settings.perm.presets') }}</span>
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
          <span>{{ t('settings.perm.field.pattern') }}</span>
          <input v-model="form.pattern" class="mono" :placeholder="t('settings.perm.patternPlaceholder')" data-test="perm-pattern" autocomplete="off" />
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
            {{ t('settings.perm.skippedDangerous', { names: expansion.skippedDangerous.join('、') }) }}
          </p>
          <p v-if="expansion.skippedRoot " class="preview-note mono">{{ t('settings.perm.skippedRoot') }}</p>
        </div>

        <div v-if="previewRules.needsConfirm" class="confirm" data-test="perm-confirm">
          <p>
            <template v-if="previewRules.dangerous.length">
              {{ t('settings.perm.dangerous') }}<code v-for="n in previewRules.dangerous" :key="n">{{ n }}</code>。
            </template>
            <template v-if="previewRules.high.length">
              {{ t('settings.perm.highRisk') }}<code v-for="n in previewRules.high" :key="n">{{ n }}</code>。
            </template>
            {{ t('settings.perm.effectiveNow') }}
          </p>
          <label class="check"><input v-model="form.confirmed" type="checkbox" data-test="perm-confirm-box" /> {{ t('settings.perm.confirmBox') }}</label>
        </div>

        <label class="field">
          <span>
            {{ t('settings.perm.field.note') }}
            <em v-if="previewRules.needsReason" class="req">{{ t('settings.perm.noteRequired') }}</em><em v-else class="opt">{{ t('settings.perm.noteOptional') }}</em>
          </span>
          <input v-model="form.note" :placeholder="t('settings.perm.notePlaceholder')" data-test="perm-note" />
        </label>

        <div class="row row--end">
          <span v-if="blocker" class="blocker mono" data-test="perm-blocker">{{ blocker }}</span>
          <button type="submit" class="btn btn--primary" :disabled="!!blocker || saving" data-test="perm-submit">
            {{ saving ? t('settings.perm.submitting') : t('settings.perm.submit') }}
          </button>
        </div>
      </form>

      <p v-if="groups.length === 0" class="empty">{{ t('settings.perm.emptyGrants') }}</p>

      <section v-for="g in groups" :key="g.subject" class="group" :data-test="`perm-group-${g.subject}`">
        <h3 class="group-head mono">
          <span>{{ g.label }}</span>
          <span v-if="g.pending" class="tag tag--warn">{{ t('settings.perm.pendingTag', { n: g.pending }) }}</span>
          <span v-if="g.orphaned" class="tag tag--warn">{{ t('settings.perm.orphanedTag', { n: g.orphaned }) }}</span>
        </h3>

        <article v-for="d in g.derived" :key="d.id" class="grant grant--config">
          <div class="grant-main">
            <span class="tag mono">{{ t('settings.perm.fromConfig') }}</span>
            <span class="grant-perms mono">{{ d.permissions.join('  ') }}</span>
          </div>
          <p class="grant-meta mono">{{ t('settings.perm.fromConfigMeta') }}</p>
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
            <span class="tag mono">{{ gr.source === 'config' ? t('settings.perm.fromConfig') : t('settings.perm.manual') }}</span>
            <span class="grant-actions">
              <button v-if="canActivate(gr)" type="button" class="btn" :disabled="busyId === gr.id" data-test="perm-activate" @click="onActivate(gr)">{{ t('settings.perm.activate') }}</button>
              <button v-if="canRevoke(gr)" type="button" class="btn btn--danger" :disabled="busyId === gr.id" data-test="perm-revoke" @click="onRevoke(gr)">{{ t('settings.perm.revoke') }}</button>
            </span>
          </div>
          <p v-if="grantStateHint(gr)" class="grant-hint">{{ grantStateHint(gr) }}</p>
          <p v-if="pendingNewPermissions(gr).length" class="grant-hint grant-hint--warn" data-test="perm-pending">
            {{ t('settings.perm.pendingHint') }}<code v-for="n in pendingNewPermissions(gr)" :key="n">{{ n }}</code>{{ t('settings.perm.pendingHintTail') }}
          </p>
          <p class="grant-perms mono">{{ gr.permissions.join('  ') || t('settings.perm.none') }}</p>
          <p class="grant-meta mono">
            {{ expiryText(gr) }}
            <template v-if="gr.granted_by"> · {{ t('settings.perm.grantedBy') }} <span :title="callerTitle(gr.granted_by, namesOf)">{{ callerLabel(gr.granted_by, namesOf) }}</span></template>
            <template v-if="gr.granted_at"> · {{ fmtAgo(gr.granted_at) }}</template>
            <template v-if="gr.use_count"> · {{ t('settings.perm.usedTimes', { n: gr.use_count }) }}</template>
            <template v-if="gr.note"> · {{ t('settings.perm.reason', { note: gr.note }) }}</template>
            · <a href="#" @click.prevent="auditByGrant(gr.id)">{{ t('settings.perm.audit') }}</a>
          </p>
        </article>
      </section>
    </div>

    <!-- 会话授权 -->
    <div v-else-if="tab === 'sessions' && mode.canQueryGrants" class="pane" data-test="perm-sessions">
      <p class="hint">{{ t('settings.perm.sessionsHint') }}</p>
      <p v-if="sessions.length === 0" class="empty">{{ t('settings.perm.emptySessions') }}</p>
      <article v-for="g in sessions" :key="g.id" class="grant" :class="[`grant--${g.state}`]" :data-test="`perm-session-${g.id}`">
        <div class="grant-main">
          <code class="grant-pattern">{{ subjectLabel(g.subject, namesOf) }}</code>
          <span class="tag mono" :class="{ 'tag--warn': g.state !== 'active' }">{{ grantStateLabel(g.state) }}</span>
          <span class="tag mono" :class="{ 'tag--warn': !effectiveOf(g).effective }" data-test="perm-session-effective">{{ effectiveOf(g).text }}</span>
          <span v-if="remainsOf(g)" class="tag mono" :class="{ 'tag--warn': renewStateOf(g).soon }" data-test="perm-session-remaining">{{ remainsOf(g) }}</span>
          <span class="grant-actions">
            <button
              v-if="canRenewGrant(mode.canManage, g, nowSec)"
              type="button"
              class="btn"
              :title="t('settings.perm.renewTitle')"
              data-test="perm-session-renew"
              @click="renewTarget = g"
            >{{ t('settings.perm.renew') }}</button>
            <button v-if="canRevoke(g)" type="button" class="btn btn--danger" :disabled="busyId === g.id" data-test="perm-revoke" @click="onRevoke(g)">{{ t('settings.perm.revoke') }}</button>
          </span>
        </div>
        <p class="grant-perms mono">{{ g.pattern }} → {{ g.permissions.join('  ') || t('settings.perm.none') }}</p>
        <p class="grant-meta mono">
          {{ t('settings.perm.owner') }} <span :title="callerTitle(g.session_owner, namesOf)">{{ callerLabel(g.session_owner, namesOf) || '—' }}</span> · {{ expiryText(g) }}
          <template v-if="g.granted_by"> · {{ t('settings.perm.grantedBy') }} <span :title="callerTitle(g.granted_by, namesOf)">{{ callerLabel(g.granted_by, namesOf) }}</span></template>
          <template v-if="g.note"> · {{ t('settings.perm.reason', { note: g.note }) }}</template>
          · <a href="#" @click.prevent="auditByGrant(g.id)">{{ t('settings.perm.audit') }}</a>
        </p>
        <p v-if="!renewStateOf(g).canRenew" class="grant-hint" data-test="perm-session-needs-grant">{{ renewStateOf(g).reason }}。</p>
      </article>
      <GrantRenewDialog
        v-if="renewTarget"
        :ids="[renewTarget.id]"
        :label="subjectLabel(renewTarget.subject, namesOf)"
        :expires-at="renewTarget.expires_at"
        @close="renewTarget = null"
        @renewed="onRenewed"
      />
    </div>

    <!-- 权限目录 -->
    <div v-else-if="tab === 'catalog'" class="pane" data-test="perm-catalog">
      <div class="bar">
        <input v-model="catFilter.text" class="mono search" :placeholder="t('settings.perm.catSearch')" data-test="perm-cat-search" />
        <select v-model="catFilter.risk" class="mono">
          <option value="">{{ t('settings.perm.riskAll') }}</option>
          <option value="low">{{ t('settings.perm.risk.low') }}</option>
          <option value="medium">{{ t('settings.perm.risk.medium') }}</option>
          <option value="high">{{ t('settings.perm.risk.high') }}</option>
          <option value="dangerous">{{ t('settings.perm.risk.dangerous') }}</option>
          <option value="root">{{ t('settings.perm.risk.root') }}</option>
        </select>
        <label class="check mono"><input v-model="catFilter.sessionOnly" type="checkbox" /> {{ t('settings.perm.sessionGrantable') }}</label>
      </div>
      <p v-if="catRows.length === 0" class="empty">{{ t('settings.perm.emptyCatalog') }}</p>
      <div class="cat">
        <div v-for="p in catRows" :key="p.name" class="cat-row" :class="`risk--${riskTone(p.risk)}`">
          <div class="cat-name">
            <code>{{ p.name }}</code>
            <span class="risk-chip mono">{{ p.risk_label }}</span>
            <span v-if="myPerms.has(p.name)" class="tag tag--ok mono">{{ t('settings.perm.heldTag') }}</span>
          </div>
          <p class="cat-desc">{{ p.description }}</p>
          <p class="cat-meta mono">
            {{ p.session_grantable ? t('settings.perm.sessionGrantable') : t('settings.perm.sessionNotGrantable') }} · {{ t('settings.perm.scope', { value: p.scope_label }) }} · {{ t('settings.perm.source', { value: p.source_label }) }}
          </p>
        </div>
      </div>
    </div>

    <!-- 审计 -->
    <div v-else-if="tab === 'audit' && mode.canQueryGrants" class="pane" data-test="perm-audit">
      <form class="bar" @submit.prevent="loadAudit">
        <input v-model="auditFilter.subject" class="mono" :placeholder="t('settings.perm.auditSubject')" data-test="perm-audit-subject" />
        <input v-model="auditFilter.permission" class="mono" :placeholder="t('settings.perm.auditPermission')" data-test="perm-audit-permission" />
        <input v-model="auditFilter.grant_id" class="mono" :placeholder="t('settings.perm.auditGrant')" data-test="perm-audit-grant" />
        <button type="submit" class="btn" :disabled="auditLoading">{{ auditLoading ? t('settings.perm.querying') : t('settings.perm.query') }}</button>
      </form>
      <p v-if="!auditLoading && audit.length === 0" class="empty">{{ t('settings.perm.emptyAudit') }}</p>
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
.page-head h1 { margin: 0 0 4px; font-size: var(--fs-xl); color: var(--ink); }
.page-head .sub { margin: 0; font-size: var(--fs-sm); color: var(--ink-3); }

/* 身份条：先说"你是谁"，再说能做什么 */
.who { display: flex; align-items: baseline; flex-wrap: wrap; gap: 10px; padding: 10px 14px; border: 1px solid var(--line); border-left: 3px solid var(--ink-3); border-radius: var(--r-chip); background: var(--surface); }
.who-id { color: var(--ink); font-size: 14px; }
.who-role { font-size: 12px; color: var(--ink-3); }
.who-role--root { color: var(--accent); }
.who-count { margin-left: auto; font-size: 12px; color: var(--ink-3); }

/* 调用方上限摘要：与身份条同一套面板样式；手机上两列自动折成一列。 */
.ceiling-box { display: grid; gap: 6px; padding: 10px 14px; border: 1px solid var(--line); border-left: 3px solid var(--run); border-radius: var(--r-chip); background: var(--surface); }
.ceiling-title { margin: 0; font-size: 12px; color: var(--ink-3); display: flex; align-items: center; gap: 8px; }
.ceiling-ro { color: var(--run); border: 1px solid var(--run); border-radius: var(--r-chip); padding: 0 6px; font-size: 11px; }
.ceiling-list { margin: 0; padding: 0; list-style: none; display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 2px 14px; font-size: 12px; color: var(--ink); overflow-wrap: anywhere; }
.ceiling-note { margin: 0; font-size: 12px; color: var(--run); }

.msg { margin: 0; padding: 9px 12px; border: 1px solid var(--line); border-radius: var(--r-chip); font-size: 13px; overflow-wrap: anywhere; }
.msg--error { color: var(--fail); border-color: var(--fail); }
.msg--warn { color: var(--run); border-color: var(--run); }
.msg--ok { color: var(--accent); }

.tabs { display: flex; gap: 2px; border-bottom: 1px solid var(--line); overflow-x: auto; scrollbar-width: thin; }
.tab { flex: none; position: relative; padding: 8px 14px; font: inherit; font-size: 12px; color: var(--ink-3); background: transparent; border: 0; border-bottom: 2px solid transparent; cursor: pointer; }
.tab:hover:not(:disabled) { color: var(--ink); }
.tab:disabled { opacity: 0.4; cursor: not-allowed; }
.tab--active { color: var(--accent); border-bottom-color: var(--accent); }
.tab:focus-visible, .btn:focus-visible, .chip:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.dot { display: inline-block; width: 6px; height: 6px; margin-left: 6px; border-radius: 50%; background: var(--run); vertical-align: middle; }

.pane { display: grid; gap: 12px; min-width: 0; }
.bar { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.hint, .empty { margin: 0; color: var(--ink-3); font-size: 13px; }
.empty { padding: 18px 0; }

input, select { background: var(--sunken); color: var(--ink); border: 1px solid var(--line); border-radius: var(--r-chip); padding: 6px 8px; font-size: 13px; min-width: 0; }
input:focus-visible, select:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }
.search { flex: 1 1 200px; }
.btn { font: inherit; font-size: 12px; padding: 6px 12px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: var(--r-chip); cursor: pointer; }
.btn:hover:not(:disabled) { border-color: var(--accent); }
.btn:disabled { opacity: 0.45; cursor: not-allowed; }
.btn--primary { color: var(--bg); background: var(--accent); border-color: var(--accent); }
.btn--danger { color: var(--fail); border-color: var(--fail); }
.check { display: inline-flex; align-items: center; gap: 6px; font-size: 12px; color: var(--ink-3); }

/* 新增表单 */
.form { display: grid; gap: 12px; padding: 14px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--surface); }
.row { display: flex; flex-wrap: wrap; gap: 12px; }
.row--end { align-items: center; justify-content: flex-end; }
.field { display: grid; gap: 4px; font-size: 12px; color: var(--ink-3); min-width: 0; flex: 1 1 180px; }
.req { color: var(--run); font-style: normal; }
.opt { font-style: normal; }
.presets { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; }
.presets-label { font-size: 12px; color: var(--ink-3); margin-right: 4px; }
.chip { font: inherit; font-size: 12px; padding: 4px 10px; color: var(--ink); background: transparent; border: 1px solid var(--line); border-radius: 999px; cursor: pointer; }
.chip:hover { border-color: var(--accent); color: var(--accent); }
.blocker { font-size: 12px; color: var(--run); }

/* 展开预览：每行左侧的色条就是风险档，一眼扫出这条授权里有没有重的 */
.preview { min-height: 40px; padding: 8px; border: 1px dashed var(--line); border-radius: var(--r-chip); background: var(--sunken); }
.preview-hint { margin: 0; font-size: 12px; color: var(--ink-3); }
.preview-note { margin: 6px 0 0; font-size: 12px; color: var(--run); }
.manifest { list-style: none; margin: 0; padding: 0; display: grid; gap: 2px; max-height: 260px; overflow: auto; }
.mf, .cat-row { --risk: var(--ink-3); border-left: 3px solid var(--risk); padding: 4px 8px; }
.mf { display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px 10px; }
.mf-desc { color: var(--ink-3); font-size: 12px; flex: 1 1 200px; min-width: 0; }
.risk--low { --risk: var(--ink-3); }
.risk--medium { --risk: var(--accent); }
.risk--high { --risk: var(--run); }
.risk--danger { --risk: var(--fail); }
.risk-chip { font-size: 11px; color: var(--risk); border: 1px solid var(--risk); border-radius: 3px; padding: 0 5px; }
code { font-family: var(--font-mono); font-size: 12.5px; }
.confirm { padding: 10px 12px; border: 1px solid var(--run); border-radius: var(--r-chip); display: grid; gap: 8px; font-size: 13px; }
.confirm p { margin: 0; }
.confirm code { margin-right: 6px; color: var(--run); }

/* 授权分组 */
.group { display: grid; gap: 8px; }
.group-head { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin: 8px 0 0; font-size: 13px; font-weight: 600; color: var(--ink); padding-bottom: 4px; border-bottom: 1px solid var(--line); }
.grant { display: grid; gap: 4px; padding: 10px 12px; border: 1px solid var(--line); border-radius: var(--r-chip); background: var(--surface); min-width: 0; }
.grant--revoked, .grant--expired { opacity: 0.6; }
.grant--orphaned, .grant--unmatched { border-color: var(--run); }
.grant--config { background: transparent; border-style: dashed; }
.grant-main { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.grant-pattern { color: var(--accent); font-size: 13.5px; overflow-wrap: anywhere; }
.grant-actions { margin-left: auto; display: inline-flex; gap: 6px; }
.grant-perms { margin: 0; font-size: 12px; color: var(--ink); overflow-wrap: anywhere; }
.grant-meta { margin: 0; font-size: 11.5px; color: var(--ink-3); overflow-wrap: anywhere; }
.grant-hint { margin: 0; font-size: 12.5px; color: var(--ink-3); }
.grant-hint--warn { color: var(--run); }
.grant-hint code { margin: 0 4px; }
.tag { font-size: 11px; color: var(--ink-3); border: 1px solid var(--line); border-radius: 3px; padding: 0 6px; }
.tag--warn { color: var(--run); border-color: var(--run); }
.tag--ok { color: var(--done); border-color: var(--done); }

/* 目录 */
.cat { display: grid; gap: 6px; }
.cat-row { background: var(--surface); border-top: 1px solid var(--line); border-right: 1px solid var(--line); border-bottom: 1px solid var(--line); border-radius: 0 var(--r-chip) var(--r-chip) 0; padding: 8px 12px; min-width: 0; }
.cat-name { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
.cat-desc { margin: 2px 0; font-size: 13px; }
.cat-meta { margin: 0; font-size: 11.5px; color: var(--ink-3); }

/* 审计 */
.audit { list-style: none; margin: 0; padding: 0; display: grid; gap: 0; border: 1px solid var(--line); border-radius: var(--r-chip); }
.audit-row { display: grid; grid-template-columns: 150px 120px 1fr; gap: 10px; padding: 7px 12px; border-bottom: 1px solid var(--line); font-size: 12.5px; align-items: baseline; }
.audit-row:last-child { border-bottom: 0; }
.audit-at { color: var(--ink-3); font-size: 11.5px; }
.audit-detail { color: var(--ink-3); font-size: 11.5px; overflow-wrap: anywhere; }

@media (max-width: 760px) {
  .who-count { margin-left: 0; width: 100%; }
  .audit-row { grid-template-columns: 1fr; gap: 2px; }
  .grant-actions { margin-left: 0; width: 100%; }
  .field { flex-basis: 100%; }
}
@media (prefers-reduced-motion: reduce) {
  * { transition: none !important; animation: none !important; }
}
</style>
