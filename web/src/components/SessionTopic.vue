<script setup lang="ts">
// 会话主题与范围：显示 + 就地编辑（Sessions 卡片详情与 SessionDrawer 共用）。
//  - 推断主题灰显并标「推断」；自述 / 人工显示来源小标签（悬停有说明）。
//  - 编辑权限：自己的会话不需要权限，别人的需要 session.manage.others；已结束的会话不能改（显示原因）。
//  - 范围按项目根相对路径逐行输入，提交前先按服务端同样的规则校验；服务端仍是权威（403 / 409 原样显示）。
import { computed, ref } from 'vue'
import { patchSessionTopic } from '../api/client'
import type { AgentSession } from '../api/types'
import { useWhoami } from '../store/whoami'
import { buildTopicPatch, canEditTopic, SCOPE_MAX, scopeToText, topicDisplay, TOPIC_MAX } from '../utils/sessionTopic'

const props = defineProps<{ session: AgentSession }>()
const emit = defineEmits<{ (e: 'updated', s: AgentSession): void }>()

const { whoami } = useWhoami()
const editing = ref(false)
const topicInput = ref('')
const scopeInput = ref('')
const busy = ref(false)
const error = ref('')

const shown = computed(() => topicDisplay(props.session))
const gate = computed(() => canEditTopic(whoami.value, props.session))
const scope = computed(() => props.session.scope ?? [])

function startEdit(): void {
  if (!gate.value.ok) return
  // 推断值不是真实主题，不预填，免得一保存就把推断值固化下来。
  topicInput.value = props.session.topic_source === 'inferred' ? '' : (props.session.topic ?? '')
  scopeInput.value = scopeToText(props.session.scope)
  error.value = ''
  editing.value = true
}

function cancel(): void {
  editing.value = false
  error.value = ''
}

async function save(): Promise<void> {
  if (busy.value) return
  const built = buildTopicPatch(props.session, topicInput.value, scopeInput.value)
  if (built.error) {
    error.value = built.error
    return
  }
  if (built.unchanged) {
    editing.value = false
    return
  }
  busy.value = true
  error.value = ''
  try {
    const updated = await patchSessionTopic(props.session.session_id, built.patch)
    emit('updated', updated)
    editing.value = false
  } catch (e) {
    error.value = e instanceof Error ? (('detail' in e && (e as { detail?: string }).detail) || e.message) : String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="stopic mono" data-test="session-topic" @click.stop>
    <template v-if="!editing">
      <div class="stopic-row">
        <span class="stopic-label">主题</span>
        <span v-if="shown.text" class="stopic-text" :class="{ 'stopic-text--inferred': shown.inferred }" :title="shown.title" data-test="topic-text">{{ shown.text }}</span>
        <span v-else class="stopic-text stopic-text--empty" data-test="topic-empty">未设置</span>
        <span v-if="shown.tag" class="stopic-tag" :class="{ 'stopic-tag--inferred': shown.inferred }" :title="shown.title" data-test="topic-tag">{{ shown.tag }}</span>
        <button
          class="stopic-edit mono"
          type="button"
          data-test="topic-edit"
          :disabled="!gate.ok"
          :title="gate.ok ? '修改主题与范围' : gate.reason"
          @click="startEdit"
        >编辑</button>
      </div>
      <div class="stopic-row">
        <span class="stopic-label">范围</span>
        <span v-if="scope.length" class="stopic-scope" data-test="topic-scope">
          <code v-for="p in scope" :key="p" class="stopic-chip">{{ p }}</code>
        </span>
        <span v-else class="stopic-text stopic-text--empty" data-test="scope-empty">未设置</span>
      </div>
      <p v-if="!gate.ok" class="stopic-why" data-test="topic-why">{{ gate.reason }}</p>
    </template>
    <form v-else class="stopic-form" data-test="topic-form" @submit.prevent="save">
      <label class="stopic-field">主题（一句话，最多 {{ TOPIC_MAX }} 字）
        <input v-model="topicInput" type="text" :maxlength="TOPIC_MAX" placeholder="这个会话在做什么" data-test="topic-input" />
      </label>
      <label class="stopic-field">范围（项目根相对路径，每行一个，最多 {{ SCOPE_MAX }} 项；清空 = 不限定）
        <textarea v-model="scopeInput" rows="3" placeholder="internal/job&#10;web/src" data-test="scope-input"></textarea>
      </label>
      <p v-if="error" class="stopic-error" role="alert" data-test="topic-error">{{ error }}</p>
      <div class="stopic-actions">
        <button class="stopic-btn stopic-btn--primary mono" type="submit" :disabled="busy" data-test="topic-save">{{ busy ? '保存中…' : '保存' }}</button>
        <button class="stopic-btn mono" type="button" :disabled="busy" @click="cancel">取消</button>
      </div>
    </form>
  </div>
</template>

<style scoped>
.stopic { display: flex; flex-direction: column; gap: 6px; font-size: 12px; min-width: 0; }
.stopic-row { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; min-width: 0; }
.stopic-label { color: var(--queue); letter-spacing: 0.06em; flex: 0 0 auto; }
.stopic-text { color: var(--paper); overflow-wrap: anywhere; min-width: 0; }
.stopic-text--inferred { color: var(--queue); font-style: italic; }
.stopic-text--empty { color: var(--queue); opacity: 0.7; }
.stopic-tag {
  padding: 0 6px; border: 1px solid var(--line); border-radius: 999px;
  color: var(--phosphor); font-size: 11px; line-height: 16px;
}
.stopic-tag--inferred { color: var(--queue); border-style: dashed; }
.stopic-scope { display: inline-flex; flex-wrap: wrap; gap: 4px; min-width: 0; }
.stopic-chip {
  padding: 0 6px; border: 1px solid var(--line); border-radius: var(--radius);
  background: var(--term-bg); color: var(--paper); font-size: 11px; overflow-wrap: anywhere;
}
.stopic-edit, .stopic-btn {
  margin-left: auto; padding: 1px 8px; border: 1px solid var(--line); border-radius: var(--radius);
  background: transparent; color: var(--paper); font-size: 11px; cursor: pointer;
}
.stopic-edit:hover:not(:disabled), .stopic-btn:hover:not(:disabled) { border-color: var(--phosphor); color: var(--phosphor); }
.stopic-edit:disabled, .stopic-btn:disabled { opacity: 0.45; cursor: not-allowed; }
.stopic-why { margin: 0; color: var(--queue); font-size: 11px; }
.stopic-form { display: flex; flex-direction: column; gap: 8px; }
.stopic-field { display: flex; flex-direction: column; gap: 4px; color: var(--queue); }
.stopic-field input, .stopic-field textarea {
  width: 100%; box-sizing: border-box; padding: 5px 8px; border: 1px solid var(--line);
  border-radius: var(--radius); background: var(--term-bg); color: var(--paper);
  font: inherit; font-size: 12px;
}
.stopic-field input:focus, .stopic-field textarea:focus { outline: none; border-color: var(--phosphor); }
.stopic-error { margin: 0; color: var(--fail); overflow-wrap: anywhere; }
.stopic-actions { display: flex; gap: 8px; }
.stopic-actions .stopic-btn { margin-left: 0; }
.stopic-btn--primary { border-color: var(--phosphor); color: var(--phosphor); }
@media (max-width: 640px) {
  .stopic-edit { padding: 4px 12px; font-size: 12px; }
  .stopic-field input, .stopic-field textarea { font-size: 16px; }
}
</style>
