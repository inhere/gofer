<script setup lang="ts">
// 关于页里「这个 server 不能一键更新」的说明块。
//
// 以前这里只有一行灰色小字（settings.about.manual），操作员扫过去以为一键更新坏了；
// 现在做成警示色 callout：为什么没有按钮、现在怎么手动更新、以及怎么改成服务
// （`gofer serve register`，之后这里就有「立即更新」按钮）。
//
// 行为：命令一律等宽，每条带复制按钮（clipboard 不可用时在框底提示手动复制）；
// 文案全走 i18n（settings.about.manual.*），一段 / 一步一个 key，不把多行塞进一个键。
import { ref } from 'vue'
import { Check, Copy, TriangleAlert } from '@lucide/vue'
import Icon from '../ui/Icon.vue'
import { t } from '../i18n'
import { copyText } from '../utils/sessionMessaging'

// managed：server 是受管服务（`gofer serve register` 注册过）时为 true，那时关于页显示一键更新
// 按钮，本块渲染空；字段缺省（undefined）按未托管处理。
defineProps<{ managed?: boolean }>()

// 命令本身是 ASCII，不进字典（改了命令也不会漏翻）；旁边的说明文案才是 i18n 的。
const MANUAL_CMDS = [
  { id: 'now-server', cmd: 'gofer serve' },
  { id: 'now-worker', cmd: 'gofer worker upgrade <id>' },
]

// 改成服务的四步：step1 没有命令可复制（停前台进程是人手动 Ctrl+C），其余各一条。
const STEPS: Array<{ id: string; text: string; cmd?: string }> = [
  { id: 'step1', text: 'settings.about.manual.step1' },
  { id: 'step2', text: 'settings.about.manual.step2', cmd: 'gofer serve register' },
  { id: 'step3', text: 'settings.about.manual.step3', cmd: 'gofer serve start' },
  { id: 'step4', text: 'settings.about.manual.step4', cmd: 'gofer serve status' },
]

const copied = ref('')
const copyFailed = ref('')

async function copy(cmd: string): Promise<void> {
  const ok = await copyText(cmd)
  copied.value = ok ? cmd : ''
  copyFailed.value = ok ? '' : cmd
  if (!ok) return
  window.setTimeout(() => {
    if (copied.value === cmd) copied.value = ''
  }, 1500)
}
</script>

<template>
  <div v-if="!managed" class="manual" data-testid="update-manual">
    <p class="manual-head">
      <Icon :icon="TriangleAlert" class="manual-icon" />
      <strong class="manual-title">{{ t('settings.about.manual.title') }}</strong>
    </p>

    <div class="manual-block">
      <p class="manual-sub">{{ t('settings.about.manual.now') }}</p>
      <p class="manual-text">{{ t('settings.about.manual.nowBody') }}</p>
      <div v-for="item in MANUAL_CMDS" :key="item.id" class="manual-cmd" :data-testid="`update-manual-${item.id}`">
        <code class="mono manual-cmd-text">{{ item.cmd }}</code>
        <button
          class="manual-copy"
          type="button"
          data-testid="update-manual-copy"
          :aria-label="t('settings.about.manual.copy')"
          :title="copied === item.cmd ? t('settings.about.manual.copied') : t('settings.about.manual.copy')"
          @click="copy(item.cmd)"
        >
          <Icon :icon="copied === item.cmd ? Check : Copy" size="sm" />
        </button>
      </div>
    </div>

    <div class="manual-block">
      <p class="manual-sub">{{ t('settings.about.manual.service') }}</p>
      <ol class="manual-steps">
        <li v-for="step in STEPS" :key="step.id" class="manual-step" :data-testid="`update-manual-${step.id}`">
          <span class="manual-text">{{ t(step.text) }}</span>
          <span v-if="step.cmd" class="manual-cmd">
            <code class="mono manual-cmd-text">{{ step.cmd }}</code>
            <button
              class="manual-copy"
              type="button"
              data-testid="update-manual-copy"
              :aria-label="t('settings.about.manual.copy')"
              :title="copied === step.cmd ? t('settings.about.manual.copied') : t('settings.about.manual.copy')"
              @click="copy(step.cmd)"
            >
              <Icon :icon="copied === step.cmd ? Check : Copy" size="sm" />
            </button>
          </span>
        </li>
      </ol>
    </div>

    <!-- 复制失败（非安全上下文等）：只提示一次，命令本身可手动选中。 -->
    <p v-if="copyFailed" class="manual-fail" role="alert">{{ t('settings.about.manual.copyFailed') }}</p>
    <p class="manual-more">{{ t('settings.about.manual.more') }}</p>
  </div>
</template>

<style scoped>
/* 警示色（--need / --need-soft）只用于「需要人处理」的提示，深浅两套色板都已定义，
   正文用 --ink、标题用 --need-ink，两套主题下都 ≥4.5:1。 */
.manual {
  display: flex;
  flex-direction: column;
  gap: var(--s3);
  min-width: 0;
  margin-top: 12px;
  padding: 10px 12px;
  border: 1px solid var(--need);
  border-left: 4px solid var(--need);
  border-radius: var(--r-ctl);
  background: var(--need-soft);
}

.manual-head {
  display: flex;
  align-items: flex-start;
  gap: var(--s2);
  margin: 0;
  min-width: 0;
}
.manual-icon {
  flex: none;
  margin-top: 2px;
  color: var(--need);
}
.manual-title {
  color: var(--need-ink);
  font-size: var(--fs-md);
  font-weight: 600;
  overflow-wrap: anywhere;
}

.manual-block {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  min-width: 0;
}
.manual-sub {
  margin: 0;
  color: var(--ink);
  font-size: var(--fs-sm);
  font-weight: 600;
}
.manual-text {
  margin: 0;
  color: var(--ink);
  font-size: var(--fs-sm);
  line-height: 1.6;
  overflow-wrap: anywhere;
}

.manual-cmd {
  display: flex;
  align-items: center;
  gap: var(--s2);
  min-width: 0;
  max-width: 100%;
}
/* 长命令（含占位符）在自己的行里横滚，不把 callout 撑宽。 */
.manual-cmd-text {
  flex: 1 1 auto;
  min-width: 0;
  padding: 3px var(--s2);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: var(--sunken);
  color: var(--ink);
  font-size: var(--fs-sm);
  overflow-x: auto;
  white-space: pre;
}
.manual-copy {
  flex: none;
  display: inline-flex;
  align-items: center;
  padding: 3px var(--s1);
  border: 1px solid var(--line);
  border-radius: var(--r-chip);
  background: transparent;
  color: var(--ink-2);
  font: inherit;
  cursor: pointer;
}
.manual-copy:hover {
  border-color: var(--need);
  color: var(--ink);
}
.manual-copy:focus-visible {
  outline: 2px solid var(--focus);
  outline-offset: 1px;
}
.manual-fail {
  margin: 0;
  color: var(--fail);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}

.manual-steps {
  display: flex;
  flex-direction: column;
  gap: var(--s2);
  margin: 0;
  padding-left: var(--s5);
}
.manual-step {
  display: flex;
  flex-direction: column;
  gap: var(--s1);
  min-width: 0;
}

.manual-more {
  margin: 0;
  color: var(--ink-2);
  font-size: var(--fs-xs);
  overflow-wrap: anywhere;
}

/* 手机：命令与复制按钮各占一行，触摸高度够大。 */
@media (max-width: 760px) {
  .manual {
    padding: var(--s3);
  }
  .manual-cmd {
    flex-wrap: wrap;
  }
  .manual-copy {
    min-height: 36px;
  }
}
</style>
