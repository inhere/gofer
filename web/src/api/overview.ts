// GET /v1/stats/overview（gofer-yelm 统计墙）：服务端按 ended_at 归属、按 (range, tz) 缓存；
// 没有数据来源的指标是 null，页面显示「—」。
import { request } from './client'

// today = 浏览器时区今天 0 点到现在（额外带 24 行 hourly）；不传 range 时服务端默认 7d。
export type OverviewRange = 'today' | '7d' | '30d' | 'all'

export interface OverviewJobBrief {
  id: string
  title: string
  agent: string
  project: string
  status: string
  turns: number | null
  active_sec: number | null
  wall_sec: number
}

export interface OverviewDay {
  day: string // YYYY-MM-DD（浏览器时区）
  done: number
  failed: number
  commits: number
  wall_sec: number
}

// OverviewHour 是 range=today 的一个本地小时（0–23，补零，含尚未到的小时）。
export interface OverviewHour {
  hour: number
  done: number
  failed: number
  commits: number
  wall_sec: number
}

export interface OverviewModelRow {
  model: string // '' = agent 默认模型
  agent?: string
  source: 'job' | 'session' | 'job+session'
  cost_usd: number
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
}

export interface Overview {
  range: { key: OverviewRange; from: number; to: number; tz: number; first_job_at: number }
  totals: { jobs: number; sessions: number; wall_sec: number }
  jobs: {
    total: number
    done: number
    failed: number
    cancelled: number
    rejected: number
    needs_review: number
    in_progress: number
    success_rate: number | null
  }
  time: {
    wall_sec: number
    avg_sec: number | null
    median_sec: number | null
    active_sec: number | null
    human_wait_sec: number | null
  }
  git: {
    commits: number
    jobs_with_commits: number
    files: number | null
    insertions: number | null
    deletions: number | null
    git_jobs: number
  }
  signal: {
    turns: number | null
    tool_calls: number | null
    human: number
    jobs_with_human: number
    denominator_jobs: number
    turn_jobs: number
    tool_jobs: number
    coverage: number
  } | null
  daily: OverviewDay[]
  hourly?: OverviewHour[] // 仅 range=today
  best: {
    weekday: { dow: number; avg: number } | null
    day: { day: string; done: number } | null
    month?: { month: string; done: number } | null
    daily_avg?: number | null
    hour?: { hour: number; done: number } | null // 仅 range=today（此时 weekday / day / daily_avg 为 null）
    streak_days: number
  }
  heatmap: { weeks: number; levels: number[]; days: { day: string; done: number }[] }
  agents: { agent: string; jobs: number; done: number; success_rate: number | null; avg_sec: number | null }[]
  projects: { project: string; jobs: number; wall_sec: number; commits: number }[]
  review: {
    reviewed: number
    accepted: number
    rejected: number
    rerun: number
    accept_rate: number | null
    reject_rate: number | null
    wait_avg_sec: number | null
    wait_median_sec: number | null
    pending_now: number
    plans_done: number
    todos_done: number
  }
  workload: { longest: OverviewJobBrief[]; quickest: OverviewJobBrief[] }
  usage: {
    cost_usd: number
    job_cost_usd: number
    session_cost_usd: number
    input_tokens: number
    output_tokens: number
    cache_read_tokens: number
    per_turn_usd: number | null
    per_job_usd: number | null
    jobs_with_usage: number
    sessions: number
    by_model: OverviewModelRow[]
  } | null
  notes: string[]
  generated_at: number
  cached: boolean
}

// browserTZ 是浏览器的 UTC 偏移（分钟，东正：UTC+8 = 480）——getTimezoneOffset 的符号相反。
export function browserTZ(d: Date = new Date()): number {
  return -d.getTimezoneOffset()
}

export function getStatsOverview(range: OverviewRange, tz: number = browserTZ()): Promise<Overview> {
  const q = new URLSearchParams({ range, tz: String(tz) })
  return request<Overview>(`/v1/stats/overview?${q.toString()}`)
}
