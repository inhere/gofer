// N3「稍后」（T3，design §2.3）的纯逻辑：菜单选项、提示文案、唤醒标记。
import type { TodayCard, TodaySnoozed } from '../api/today'
import { minutesText, tomorrowNine } from './today'
import { t } from '../i18n'

export interface SnoozeOption {
  id: 'hour' | 'morning' | 'job'
  label: string
  until_at?: number
  until_job_id?: string
}

// 菜单：1 小时 / 明早 9:00 / 等相关 job 结束（卡上有 job 才有）。
export function snoozeOptions(card: TodayCard, now = new Date()): SnoozeOption[] {
  const nowSec = Math.floor(now.getTime() / 1000)
  const out: SnoozeOption[] = [
    { id: 'hour', label: t('today.snooze.hour'), until_at: nowSec + 3600 },
    { id: 'morning', label: t('today.snooze.morning'), until_at: tomorrowNine(now) },
  ]
  if (card.refs.job_id) out.push({ id: 'job', label: t('today.snooze.job'), until_job_id: card.refs.job_id })
  return out
}

// 菜单底部提示：有新动静会提前回来；会超时的卡还要说清超时兜底照旧。
export function snoozeHints(card: TodayCard, nowSec: number): { text: string; bad?: boolean }[] {
  const out: { text: string; bad?: boolean }[] = [{ text: t('today.snooze.hintReturn') }]
  if (card.expires_at) {
    const left = card.expires_at - nowSec
    out.push({
      text: left > 0 ? t('today.snooze.hintFallback', { left: minutesText(left) }) : t('today.snooze.hintFallbackNow'),
      bad: true,
    })
  }
  return out
}

// toast 文案。
export function snoozeDoneLabel(opt: SnoozeOption): string {
  return opt.id === 'job' ? t('today.snooze.doneJob') : t('today.snooze.done', { label: opt.label })
}

// 回来的卡上的标记。
export function wokeText(card: TodayCard): string {
  if (!card.woke) return ''
  switch (card.woke_reason) {
    case 'time':
      return t('today.woke.time')
    case 'job':
      return t('today.woke.job')
  }
  return t('today.woke.other')
}

// 「已稍后」抽屉里一行的「到什么时候」。
export function snoozedUntilText(s: TodaySnoozed, fmt: (sec: number) => string): string {
  if (s.until_job_id) return t('today.snoozed.untilJob', { id: s.until_job_id })
  return s.until_at ? t('today.snoozed.until', { time: fmt(s.until_at) }) : ''
}
