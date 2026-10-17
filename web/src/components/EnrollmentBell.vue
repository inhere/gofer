<script setup lang="ts">
// 顶栏「worker 申请 N」提醒（worker 自助注册）：有未决申请时显示带计数的入口，点它跳到
// Runners 页的「待接受」区（/runners#enrollments）。样式沿用顶栏既有提醒（EscalationBell）。
//
//  - 只对持有 worker.manage 的人显示：拿不到 whoami 时不显示吗？——can() 的既有口径是
//    「whoami 未知就不隐藏」，但那会让没有权限的连接去订阅属于 worker.manage 的主题，
//    服务端会拒（error 帧）。所以这里以 can() 同样口径判断，只是不订阅、不请求；
//  - 主题 worker-enrollments 是无负载失效通知：收到就重新拉一次受保护的列表接口取计数。
import { computed, onUnmounted, ref, watch } from 'vue'
import { RouterLink } from 'vue-router'
import { listEnrollments } from '../api/client'
import { TOPIC_WORKER_ENROLLMENTS } from '../api/live'
import { useWhoami } from '../store/whoami'
import { ENROLLMENTS_LINK } from '../utils/enrollments'
import { createLiveTopic } from '../utils/useLiveTopic'

const { can } = useWhoami()
const allowed = computed(() => can('worker.manage'))
const count = ref(0)

async function fetchCount(): Promise<void> {
  if (!allowed.value) {
    count.value = 0
    return
  }
  try {
    const resp = await listEnrollments()
    // 功能关闭时列表恒为空，也不该有提醒。
    count.value = resp.enabled ? resp.pending_count : 0
  } catch {
    // 只是提醒：拉不到就保留上一帧；Runners 页有完整错误信息与重试。
  }
}

const live = createLiveTopic(TOPIC_WORKER_ENROLLMENTS, { initial: false, fetch: fetchCount })

watch(
  allowed,
  (on) => {
    if (on) {
      void fetchCount()
      live.start()
    } else {
      live.stop()
      count.value = 0
    }
  },
  { immediate: true },
)

onUnmounted(() => live.stop())
</script>

<template>
  <RouterLink
    v-if="allowed && count > 0"
    :to="ENROLLMENTS_LINK"
    class="enroll-bell mono"
    :title="`${count} 个 worker 申请注册，点击去接受`"
    data-test="enroll-bell"
  >
    <span class="enroll-bell-label">worker 申请</span>
    <span class="enroll-bell-n">{{ count }}</span>
  </RouterLink>
</template>

<style scoped>
.enroll-bell {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: 1px solid var(--fail);
  border-radius: var(--radius);
  padding: 4px 9px;
  font-size: 12px;
  line-height: 1.2;
  white-space: nowrap;
  color: var(--paper);
}
.enroll-bell:hover {
  text-decoration: none;
  border-color: var(--paper);
}
.enroll-bell-n {
  min-width: 16px;
  text-align: center;
  font-weight: 600;
  color: var(--fail);
}
@media (max-width: 768px) {
  .enroll-bell-label {
    display: none;
  }
  .enroll-bell::before {
    content: '申请';
  }
}
</style>
