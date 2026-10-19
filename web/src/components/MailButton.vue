<script setup lang="ts">
// 顶栏信箱入口：信封按钮（在「随手记」旁边），带「待指派」信件数徽标——那些信没人接手，
// 要人去改投或撤回，所以按「需要我处理的信件数」计数（GET /v1/mail?state=held）。
//
//  - 不做权限门槛：读信件对所有调用方都允许（含只读 token）。
//  - 服务端没有 mail 推送主题，所以计数走低频轮询（createPoller：后台标签页 / 失焦窗口停表，
//    回来立刻拉一次），与顶栏其它兜底刷新的节奏一致。
import { onMounted, onUnmounted, ref } from 'vue'
import { RouterLink } from 'vue-router'
import { listMail } from '../api/client'
import { MAIL_PAGE_LINK, mailBadgeLabel, mailHeldQuery } from '../utils/mail'
import { createPoller } from '../utils/poller'

const REFRESH_MS = 30_000

const count = ref(0)

async function refresh(): Promise<void> {
  try {
    const resp = await listMail(mailHeldQuery())
    // count 是本次返回的条数（服务端按 limit 截断），顶栏只需要量级。
    count.value = resp.count ?? 0
  } catch {
    // 只是提醒：拉不到就保留上一帧；信箱页有完整错误信息与重试。
  }
}

const poller = createPoller(refresh, REFRESH_MS)
onMounted(() => poller.start())
onUnmounted(() => poller.stop())
</script>

<template>
  <RouterLink
    :to="MAIL_PAGE_LINK"
    class="mail-btn mono"
    title="信箱"
    aria-label="信箱"
    data-test="mail-btn"
    data-testid="mail-button"
  >
    <span aria-hidden="true">✉</span>
    <span v-if="count > 0" class="mail-n" :title="`${count} 封信待指派`" data-test="mail-badge">{{ mailBadgeLabel(count) }}</span>
  </RouterLink>
</template>

<style scoped>
.mail-btn {
  position: relative;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 4px 9px;
  font-size: 13px;
  line-height: 1.2;
  color: var(--queue);
  background: transparent;
  border: 1px solid var(--line);
  border-radius: var(--radius);
  white-space: nowrap;
}
.mail-btn:hover {
  border-color: var(--phosphor);
  color: var(--paper);
  text-decoration: none;
}
.mail-n {
  min-width: 16px;
  padding: 0 4px;
  border-radius: 999px;
  background: var(--run);
  color: var(--ink);
  font-size: 10px;
  line-height: 14px;
  font-weight: 600;
  text-align: center;
}
</style>
