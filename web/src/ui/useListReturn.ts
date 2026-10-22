// 列表页记录「返回时回到这里」的位置（筛选 + 滚动）。详情页的 BackLink 读它跳回来。
import { onBeforeUnmount, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { readListReturn, saveListReturn } from './listReturn'

// listId：与 BackLink 的 list 一致；listPath：本列表的路由（只在这条路由上记录）。
export function useListReturn(listId: string, listPath: string): void {
  const route = useRoute()

  function save(): void {
    if (typeof window === 'undefined' || route.path !== listPath) return
    saveListReturn(listId, { url: route.fullPath, scrollY: window.scrollY || 0 })
  }

  onMounted(() => {
    if (typeof window === 'undefined') return
    const saved = readListReturn(listId)
    // 只有回到「同一个筛选视图」时才复原滚动位置，换了筛选就从顶部开始
    if (saved && saved.url === route.fullPath && saved.scrollY > 0) {
      requestAnimationFrame(() => window.scrollTo(0, saved.scrollY))
    }
    save()
    window.addEventListener('scroll', save, { passive: true })
  })

  onBeforeUnmount(() => {
    if (typeof window === 'undefined') return
    window.removeEventListener('scroll', save)
    save()
  })
}
