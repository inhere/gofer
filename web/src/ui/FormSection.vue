<script setup lang="ts">
// FormSection：设置页统一的分节（设计稿 §7.4、§8）：左边写标题和一句话说明，右边放字段。
// 桌面两栏（220px + 内容），手机堆成一行（说明在上）。斜杠分节线只出现在非首节。
defineProps<{
  /** 分节标题（走 i18n，调用方负责翻译）。 */
  title: string
  /** 一句话说明（可选）。 */
  desc?: string
}>()
</script>

<template>
  <section class="form-sec" data-testid="form-section">
    <div class="lead">
      <h2>{{ title }}</h2>
      <p v-if="desc">{{ desc }}</p>
    </div>
    <div class="body">
      <slot />
    </div>
  </section>
</template>

<style scoped>
.form-sec {
  padding: 22px 0;
  border-top: 1px solid var(--line);
  display: grid;
  grid-template-columns: 220px minmax(0, 1fr);
  gap: 16px 28px;
}
.form-sec:first-of-type {
  border-top: 0;
  padding-top: 4px;
}
.lead h2 {
  font-size: var(--fs-md);
  margin: 0 0 4px;
  color: var(--ink);
}
.lead p {
  margin: 0;
  font-size: var(--fs-sm);
  color: var(--ink-3);
}
.body {
  min-width: 0;
}

@media (max-width: 1099px) {
  .form-sec {
    grid-template-columns: minmax(0, 1fr);
    gap: 10px;
  }
}
</style>
