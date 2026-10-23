<script setup lang="ts">
// 选中仓库后的信息条（UI 重设计 P6，issue gofer-hez1）：项目 · 前缀，加上完整路径。
// 路径换行显示、不省略中间（唯一能区分仓库的常常就是中间那两级），旁边是复制按钮。
import { computed, ref } from 'vue'
import { Check, Copy } from '@lucide/vue'
import Icon from './Icon.vue'
import { t } from '../i18n'
import { copyText } from '../utils/sessionMessaging'
import { trackerRepoLabel, trackerRepoMeta, type TrackerRepoLike } from '../utils/trackerView'

const props = defineProps<{
  repo?: TrackerRepoLike | null
  // 没有选中仓库时的占位文案（各页自己的 key，两边措辞不同）。
  placeholder: string
}>()

const meta = computed(() => (props.repo ? trackerRepoMeta(props.repo) : ''))
const path = computed(() => (props.repo?.rel_path ?? '').trim())
const title = computed(() => (props.repo ? trackerRepoLabel(props.repo) : props.placeholder))
const copied = ref(false)
const failed = ref(false)

// 剪贴板不可用（非安全上下文）时不报错，就地提示；路径本身可以手动选中复制。
async function copy(): Promise<void> {
  if (!path.value) return
  const ok = await copyText(path.value)
  copied.value = ok
  failed.value = !ok
  if (ok) {
    window.setTimeout(() => {
      copied.value = false
    }, 1500)
  }
}
</script>

<template>
  <div class="trp-info" data-test="tracker-repo-info" :title="title">
    <template v-if="repo">
      <span class="trp-info-meta" lang="en">{{ meta }}</span>
      <span class="trp-info-path mono" lang="en" data-test="tracker-repo-path">{{ path || '—' }}</span>
      <button
        class="trp-info-copy"
        type="button"
        data-test="tracker-repo-copy"
        :aria-label="t('util.repo.copyPath')"
        :title="copied ? t('util.repo.copied') : t('util.repo.copyPath')"
        @click="copy"
      >
        <Icon :icon="copied ? Check : Copy" size="sm" />
        <span class="trp-info-copy-label">{{ copied ? t('util.repo.copied') : t('util.repo.copyPath') }}</span>
      </button>
      <span v-if="failed" class="trp-info-fail" role="alert" data-test="tracker-repo-copy-failed">{{ t('util.repo.copyFailed') }}</span>
    </template>
    <span v-else class="trp-info-empty">{{ placeholder }}</span>
  </div>
</template>

<style scoped>
.trp-info {
  display: flex;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 2px var(--s2);
  flex: 1 1 320px;
  min-width: 0;
}
.trp-info-meta {
  color: var(--ink-3);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
/* 完整路径：换行而不是省略号，中间各级都看得见。 */
.trp-info-path {
  color: var(--ink);
  font-size: var(--fs-sm);
  white-space: normal;
  overflow-wrap: anywhere;
  min-width: 0;
}
.trp-info-copy {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 2px var(--s1);
  background: transparent;
  color: var(--ink-3);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  font: inherit;
  font-size: var(--fs-xs);
  cursor: pointer;
  white-space: nowrap;
}
.trp-info-copy:hover {
  color: var(--ink);
  border-color: var(--accent);
}
.trp-info-copy:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.trp-info-fail {
  color: var(--fail);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}
.trp-info-empty {
  color: var(--ink-3);
  font-size: var(--fs-sm);
}
/* 手机上路径本来就长：文案与路径都换行，复制按钮给足触摸高度。 */
@media (max-width: 760px) {
  .trp-info {
    flex: 1 1 100%;
  }
  .trp-info-copy {
    min-height: 36px;
  }
}
</style>
