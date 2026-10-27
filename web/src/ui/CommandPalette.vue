<script setup lang="ts">
// 全局命令面板（设计稿 §6.5，P1 范围）：Ctrl/⌘K 或非输入状态下按 `/` 打开，
// P1 只做「页面跳转」与 `p:<项目>` 前缀（跳到当前页的项目筛选版本）。
//
// 任务 / 会话 / 计划三类结果在 P3 随页面迁移接入；这里保持 entry 结构可扩展（utils/shell.ts 的 PALETTE_PAGES）。
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { t } from '../i18n'
import { paletteHits, parsePaletteQuery, SHELL_SHORTCUTS } from '../utils/shell'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ (e: 'update:open', v: boolean): void }>()

const router = useRouter()
const query = ref('')
const active = ref(0)
const inputEl = ref<HTMLInputElement | null>(null)

const hits = computed(() => paletteHits(query.value, undefined, (key) => t(key)))
const project = computed(() => parsePaletteQuery(query.value).project)
const shortcuts = SHELL_SHORTCUTS

watch(
  () => props.open,
  async (on) => {
    if (!on) return
    query.value = ''
    active.value = 0
    await nextTick()
    inputEl.value?.focus()
  },
)

// 结果变了就把高亮拉回第一行，避免回车落到看不见的项上。
watch(hits, () => {
  active.value = 0
})

function close(): void {
  emit('update:open', false)
}

function pick(to: string): void {
  close()
  void router.push(to)
}

function onKeydown(e: KeyboardEvent): void {
  if (e.key === 'Escape') {
    close()
    return
  }
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (hits.value.length) active.value = (active.value + 1) % hits.value.length
    return
  }
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (hits.value.length) active.value = (active.value - 1 + hits.value.length) % hits.value.length
    return
  }
  if (e.key === 'Enter') {
    const hit = hits.value[active.value]
    if (hit) {
      e.preventDefault()
      pick(hit.to)
    }
  }
}

// 全局快捷键：Ctrl/⌘K 任何时候都能开；`/` 只在非输入状态下开（面板自己打开时忽略）。
function isTypingTarget(el: HTMLElement | null): boolean {
  if (!el) return false
  const tag = el.tagName.toLowerCase()
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true
  return el.isContentEditable
}

function onGlobalKey(e: KeyboardEvent): void {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    emit('update:open', !props.open)
    return
  }
  if (props.open) return
  if (e.key === '/' && !isTypingTarget(document.activeElement as HTMLElement | null)) {
    e.preventDefault()
    emit('update:open', true)
  }
}

onMounted(() => window.addEventListener('keydown', onGlobalKey))
onUnmounted(() => window.removeEventListener('keydown', onGlobalKey))
</script>

<template>
  <div v-if="open" class="cp-scrim" @click.self="close">
    <div class="cp" role="dialog" aria-modal="true" :aria-label="t('palette.title')" data-test="command-palette">
      <input
        ref="inputEl"
        v-model="query"
        class="cp-input"
        type="text"
        :placeholder="t('palette.placeholder')"
        :aria-label="t('palette.placeholder')"
        data-test="palette-input"
        @keydown="onKeydown"
      />
      <p v-if="project" class="cp-hint" data-test="palette-project">{{ t('palette.project', { p: project }) }}</p>
      <ul class="cp-list" role="listbox">
        <li v-for="(hit, i) in hits" :key="hit.to">
          <button
            class="cp-item"
            :class="{ 'cp-item--active': i === active }"
            type="button"
            role="option"
            :aria-selected="i === active"
            data-test="palette-item"
            @click="pick(hit.to)"
            @mouseenter="active = i"
          >
            <span class="cp-name">{{ hit.label }}</span>
            <span class="cp-path">{{ hit.to }}</span>
          </button>
        </li>
        <li v-if="!hits.length" class="cp-empty">{{ t('palette.empty') }}</li>
      </ul>
      <!-- 全局快捷键登记处：SHELL_SHORTCUTS 是唯一清单（新加快捷键在 utils/shell.ts 里加一行） -->
      <div class="cp-keys" data-test="palette-shortcuts">
        <span class="cp-keys-title">{{ t('palette.shortcuts') }}</span>
        <span v-for="s in shortcuts" :key="s.keys" class="cp-key">
          <kbd class="kbd">{{ s.keys }}</kbd>{{ t(s.key) }}
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.cp-scrim {
  position: fixed;
  inset: 0;
  z-index: 90;
  background: rgba(0, 0, 0, 0.45);
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding: 12vh 16px 16px;
}
.cp {
  width: min(560px, 100%);
  background: var(--surface);
  border: 1px solid var(--line-strong);
  border-radius: var(--r-sheet);
  box-shadow: var(--shadow-pop);
  overflow: hidden;
}
.cp-input {
  width: 100%;
  border: 0;
  border-bottom: 1px solid var(--line);
  background: transparent;
  color: var(--ink);
  padding: 14px 16px;
  font-size: 15px;
}
.cp-input:focus {
  outline: none;
}
.cp-hint {
  margin: 0;
  padding: 8px 16px 0;
  color: var(--ink-3);
  font-size: 12px;
}
.cp-list {
  list-style: none;
  margin: 0;
  padding: 6px;
  max-height: 52vh;
  overflow-y: auto;
}
.cp-item {
  display: flex;
  align-items: baseline;
  gap: 10px;
  width: 100%;
  text-align: left;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--r-ctl);
  color: var(--ink);
  padding: 8px 10px;
  font-size: 14px;
}
.cp-item--active {
  background: var(--accent-soft);
  border-color: var(--accent);
}
.cp-path {
  margin-left: auto;
  color: var(--ink-3);
  font-family: var(--font-mono);
  font-size: 11.5px;
}
.cp-empty {
  padding: 10px;
  color: var(--ink-3);
  font-size: 13px;
}
.cp-keys {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 4px 14px;
  padding: 8px 16px;
  border-top: 1px solid var(--line);
  color: var(--ink-3);
  font-size: 11.5px;
}
.cp-keys-title {
  font-weight: 600;
}
.cp-key {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
</style>
