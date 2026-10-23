// 量一个元素的宽度（任务页按宽度决定列表+详情并排，见 utils/jobsLayout.ts）。
// ResizeObserver 优先；没有（老浏览器、部分测试环境）就退回 window.resize 与一次首测。
import { onBeforeUnmount, onMounted, ref, type Ref } from 'vue'

export function useElementWidth(): { el: Ref<HTMLElement | null>; width: Ref<number> } {
  const el = ref<HTMLElement | null>(null)
  const width = ref(0)
  let observer: ResizeObserver | null = null

  function measure(): void {
    width.value = el.value?.getBoundingClientRect().width ?? 0
  }

  onMounted(() => {
    measure()
    if (typeof ResizeObserver !== 'undefined' && el.value) {
      observer = new ResizeObserver(measure)
      observer.observe(el.value)
    }
    if (typeof window !== 'undefined') window.addEventListener('resize', measure)
  })

  onBeforeUnmount(() => {
    observer?.disconnect()
    observer = null
    if (typeof window !== 'undefined') window.removeEventListener('resize', measure)
  })

  return { el, width }
}
