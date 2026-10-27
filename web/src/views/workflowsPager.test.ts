// 工作流列表的服务端分页接线（每页 50 条）：照本项目既有的列表分页写法（Issues / 记忆），
// 页码进 ?page=、切状态过滤回第 1 页、limit / offset 交给接口、total 算页数。
// 页面测试在本仓库是结构 / 接线断言（没有 DOM 环境），纯逻辑在 utils/issuePaging.test.ts。
import { describe, expect, it } from 'vitest'

const source = Object.values(
  import.meta.glob('./Workflows.vue', { eager: true, query: '?raw', import: 'default' }),
)[0] as string

describe('Workflows 列表分页', () => {
  it('每页 50 条，limit / offset 交给服务端', () => {
    expect(source).toContain('const PAGE_SIZE = 50')
    expect(source).toContain('limit: PAGE_SIZE')
    expect(source).toContain('offset: (currentPage.value - 1) * PAGE_SIZE')
  })

  it('复用既有分页写法：pageWindow / clampPage / ?page= 读写', () => {
    expect(source).toContain("from '../utils/issuePaging'")
    expect(source).toContain('pageWindow(currentPage, pageCount)')
    expect(source).toContain('clampPage(p, pageCount.value)')
    expect(source).toContain('const page = ref(readPageQuery(route.query))')
    expect(source).toContain('writeQuery(pageQueryUpdate(route.query, page.value))')
  })

  it('页数由接口返回的 total 算，不把整表拉下来再切页', () => {
    expect(source).toContain('total.value = resp.total')
    expect(source).toContain('pageCountOf(total.value, PAGE_SIZE)')
    // 客户端切页的老写法（visible 过滤当前页）已经去掉。
    expect(source).not.toContain('const visible = computed')
  })

  it('切状态过滤回到第 1 页并把页码从 URL 上抹掉', () => {
    expect(source).toMatch(/watch\(statusFilter, \(\) => \{\s*page\.value = 1\s*writeQuery\(pageQueryUpdate\(route\.query, 1\)\)/)
    // 状态过滤交给服务端（?status=），不再只在客户端收窄。
    expect(source).toContain('status: statusFilter.value || undefined')
  })

  it('后退 / 前进时页码跟着 URL 走', () => {
    expect(source).toMatch(/\(\) => route\.query\.page,/)
    expect(source).toContain('const next = readPageQuery({ page: value })')
    expect(source).toContain('page.value = next')
  })

  it('页脚有上一页 / 页码 / 下一页与「共 N 条」', () => {
    for (const key of ['data-test="pager"', 'data-test="page-summary"', 'data-test="page-prev"', 'data-test="page-next"']) {
      expect(source).toContain(key)
    }
    expect(source).toContain("t('workflows.pager.label')")
  })

  it('手机档下分页按钮也放大到 40px', () => {
    const mobile = source.slice(source.indexOf('@media (max-width: 760px)'))
    expect(mobile).toMatch(/\.page-btn[^{]*\{[^}]*min-height: 40px/)
  })
})
