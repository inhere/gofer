<script setup lang="ts">
// 撤销窗口 toast（design §2.3）：「已批准 · 撤销 5s」，按 z 或点按钮撤销；操作失败时显示原因。
// 外观与可访问性在 ui/Toast.vue；这里只把 today 的撤销队列接上去。
import { actionError, undoQueue } from '../../store/today'
import { t } from '../../i18n'
import Toast from '../../ui/Toast.vue'

const current = undoQueue.current

function clearError(): void {
  actionError.value = ''
}
</script>

<template>
  <Toast
    v-if="current"
    :text="current.label"
    :meta="`${current.left}s`"
    :action-label="t('today.undo.action')"
    data-test="undo-toast"
    @action="undoQueue.undo()"
  />
  <Toast
    v-else-if="actionError"
    tone="danger"
    :text="t('today.undo.failed', { err: actionError })"
    :action-label="t('today.undo.dismiss')"
    data-test="undo-toast"
    @action="clearError"
  />
</template>
