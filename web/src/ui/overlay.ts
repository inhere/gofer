// 抽屉 / 底部抽屉外壳共用的行为（设计稿 §10）：Esc 关闭、焦点回到打开它的元素、
// Tab 在面板内循环、<body> 不跟着滚。纯逻辑 + 一个 composable，Sheet 与 Drawer 都用它。
import { onBeforeUnmount, watch, type Ref } from 'vue'

export const FOCUSABLE_SELECTOR = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(', ')

export function focusables(root: HTMLElement | null | undefined): HTMLElement[] {
  if (!root) return []
  return Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (el) => el.offsetParent !== null || el === document.activeElement,
  )
}

// Tab / Shift+Tab 在面板内循环；返回 true 表示事件已被处理（调用方 preventDefault）。
export function trapTab(root: HTMLElement | null | undefined, backwards: boolean): boolean {
  const items = focusables(root)
  if (items.length === 0) {
    root?.focus()
    return true
  }
  const current = document.activeElement as HTMLElement | null
  const index = current ? items.indexOf(current) : -1
  if (index === -1) {
    ;(backwards ? items[items.length - 1] : items[0]).focus()
    return true
  }
  const next = (index + (backwards ? -1 : 1) + items.length) % items.length
  items[next].focus()
  return true
}

export interface OverlayOptions {
  /** 面板（dialog）根元素，用来做焦点循环与初始聚焦 */
  panel: Ref<HTMLElement | null>
  /** 面板里优先聚焦的元素，缺省取第一个可聚焦元素 */
  initialFocus?: Ref<HTMLElement | null>
  /** 打开时锁 body 滚动（默认开） */
  lockScroll?: boolean
}

// 外壳的统一行为：打开时记住打开者并聚焦面板，关闭时把焦点还回去。
export function useOverlayBehavior(open: Ref<boolean>, opts: OverlayOptions): (event: KeyboardEvent) => void {
  let opener: HTMLElement | null = null
  let locked = false

  function lock(): void {
    if (opts.lockScroll === false || locked || typeof document === 'undefined') return
    document.body.style.overflow = 'hidden'
    locked = true
  }

  function unlock(): void {
    if (!locked) return
    document.body.style.overflow = ''
    locked = false
  }

  watch(
    open,
    (isOpen) => {
      if (typeof document === 'undefined') return
      if (isOpen) {
        opener = (document.activeElement as HTMLElement | null) ?? null
        lock()
        // 面板在 v-if 后才存在，等一帧再聚焦
        requestAnimationFrame(() => {
          const target = opts.initialFocus?.value ?? focusables(opts.panel.value)[0] ?? opts.panel.value
          target?.focus?.()
        })
      } else {
        unlock()
        opener?.focus?.()
        opener = null
      }
    },
    { immediate: true },
  )

  onBeforeUnmount(unlock)

  function onKeydown(event: KeyboardEvent): void {
    if (!open.value) return
    if (event.key === 'Escape') {
      event.stopPropagation()
      event.preventDefault()
      open.value = false
      return
    }
    if (event.key === 'Tab') {
      event.preventDefault()
      trapTab(opts.panel.value, event.shiftKey)
    }
  }

  return onKeydown
}
