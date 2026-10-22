import { describe, expect, it } from 'vitest'
import type { RouteLocation } from 'vue-router'
import { routes } from './router.routes'

// 设计稿 §7.1 / §16-3：任务列表改名 /jobs，旧链接 /board 带原 query 重定向。
// 这里直接对路由表断言（不建 router：入口守卫需要 token，与重定向无关）。
function entry(path: string) {
  return routes.find((r) => r.path === path)
}

describe('/board → /jobs rename', () => {
  it('serves the job list on /jobs under the name "jobs"', () => {
    const jobs = entry('/jobs')
    expect(jobs).toBeTruthy()
    expect(jobs?.name).toBe('jobs')
    expect(jobs?.component).toBeTypeOf('function')
  })

  it('keeps /board as a redirect that carries the original query', () => {
    const board = entry('/board')
    expect(board).toBeTruthy()
    expect(board?.component).toBeUndefined()
    const redirect = board?.redirect
    expect(redirect).toBeTypeOf('function')
    const to = { path: '/board', query: { status: 'needs_review', project: 'p1' } } as unknown as RouteLocation
    expect((redirect as (t: RouteLocation) => unknown)(to)).toEqual({
      path: '/jobs',
      query: { status: 'needs_review', project: 'p1' },
    })
  })

  it('keeps the job detail route addressable and the other legacy redirects in place', () => {
    expect(entry('/jobs/:id')?.name).toBe('job-detail')
    expect(entry('/skills')?.redirect).toBe('/settings/skills')
    expect(entry('/cluster')?.redirect).toBe('/runners')
    expect(entry('/config')?.redirect).toBe('/settings/config')
  })
})
