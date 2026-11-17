import { describe, expect, it } from 'vitest'

const composer = Object.values(import.meta.glob('./WorkbenchComposer.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string
const workbench = Object.values(import.meta.glob('../../views/Workbench.vue', { eager: true, query: '?raw', import: 'default' }))[0] as string

describe('mobile session launcher placement', () => {
  it('keeps the floating launcher away from an open session and puts a launcher in the top bar', () => {
    expect(composer).toContain('v-if="!open && !sessionOpen"')
    expect(composer).toContain('defineProps<{ sessionOpen: boolean }>()')
    expect(workbench).toContain('class="session-new"')
    // R13：手机顶栏的「＋」现在开新建会话表单（ACP 默认）；只读上限下不给。
    expect(workbench).toContain("v-if=\"mobilePane === 'main' && !readOnly\"")
    expect(workbench).toContain('@click="openNewSession"')
  })
})
