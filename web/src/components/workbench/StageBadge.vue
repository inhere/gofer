<script setup lang="ts">
// 工作台会话线程头上的「当前流程步」小标签（计划 2026-11-26 §2 F5）：「<步骤> n/N」，点它跳工作项详情。
//
// 数据来自会话视图（GET /v1/sessions/{sid} 的 stage 字段，见 http-api.md「会话视图」）：
// 一次只给当前打开的这个线程发一条请求（线程列表本身不带 stage），不是按会话逐个拉。
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { getAgentSession } from '../../api/client'
import { t } from '../../i18n'
import { sessionIDOfThread } from './seatRoster'
import { stageBadgeBlocked, stageBadgeText, stageWorkItemLink } from '../../utils/stage'
import type { SessionStageBrief } from '../../api/types'

const props = defineProps<{
  threadId: string
  // brief 由调用方直接给（给了就不自己拉：名册一次请求就带回了每一行的 stage）。
  brief?: SessionStageBrief | null
  // plain：渲染成 span 而不是链接——名册行本身是个按钮，链接不能嵌在按钮里。
  plain?: boolean
}>()

const fetched = ref<SessionStageBrief | null>(null)
const shown = computed(() => (props.brief !== undefined ? props.brief : fetched.value))
const sessionId = computed(() => sessionIDOfThread(props.threadId))
const label = computed(() => stageBadgeText(shown.value))
const link = computed(() => stageWorkItemLink(shown.value))
const blocked = computed(() => stageBadgeBlocked(shown.value))
const title = computed(() =>
  blocked.value && shown.value?.reason
    ? t('work.stage.badgeBlocked', { reason: shown.value.reason })
    : t('work.stage.badgeTitle'),
)

let stale = false

async function load(): Promise<void> {
  if (props.brief !== undefined) return
  const sid = sessionId.value
  if (!sid) {
    fetched.value = null
    return
  }
  try {
    const d = await getAgentSession(sid, { limit: 1 })
    if (sid !== sessionId.value) return
    fetched.value = d.session?.stage ?? null
  } catch {
    // 没有会话视图（已删除的老会话、权限不足）时不显示标签，不影响线程本身
    if (!stale) fetched.value = null
  }
}

watch(sessionId, () => void load())
onMounted(() => {
  stale = false
  void load()
})
onUnmounted(() => {
  stale = true
})
</script>

<template>
  <RouterLink v-if="label && link && !plain" class="stage-badge" :class="{ 'stage-badge--blocked': blocked }" :to="link" :title="title" data-test="thread-stage">
    {{ label }}
  </RouterLink>
  <span v-else-if="label" class="stage-badge" :class="{ 'stage-badge--blocked': blocked }" :title="title" data-test="stage-badge">
    {{ label }}
  </span>
</template>

<style scoped>
.stage-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 1px 8px;
  font-size: var(--fs-xs);
  color: var(--accent);
  text-decoration: none;
  background: var(--accent-soft);
  border: 1px solid var(--accent);
  border-radius: 999px;
  white-space: nowrap;
}
.stage-badge:hover { text-decoration: underline; }
.stage-badge--blocked { color: var(--fail); background: transparent; border-color: var(--fail); }
</style>
