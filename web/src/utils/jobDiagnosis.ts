// job 诊断（`gofer job diagnose` / GET /v1/jobs/{id}/diagnosis，计划 R10 / R12）的纯逻辑：
// 续跑链关系的界面名、时间线的读法、一次诊断请求失败时的文案。
//
// 诊断全部由 gofer 已有字段推导（internal/job/diagnose.go）：没有的段落会在 timeline 里
// present=false 并带 note，界面照实显示，不补猜时间。
import { t, te } from '../i18n'
import type { JobDiagnosis, JobDiagnosisLink } from '../api/types'

// diagnosisRelationLabel 是续跑链一环的关系名（target / resume / source / failover / retry）。
// 未知关系原样显示，避免把新版服务端的关系名藏掉。
export function diagnosisRelationLabel(relation: string): string {
  const key = `jobs.diagnosis.relation.${relation}`
  return relation && te(key) ? t(key) : relation
}

// diagnosisIsFirstFailure 判断某一环是不是诊断给出的「首错」（续跑链里最早失败的那个）。
export function diagnosisIsFirstFailure(d: JobDiagnosis | null, link: JobDiagnosisLink): boolean {
  return !!d?.first_failure && d.first_failure.job_id === link.job_id
}

// diagnosisChainCount 是续跑链的长度（只有本 job 一环 = 没有续跑）。
export function diagnosisChainCount(d: JobDiagnosis | null): number {
  return d?.chain.length ?? 0
}

// diagnosisErrorText 把一次诊断请求的失败翻成文案；服务端的 detail 原样带出来。
export function diagnosisErrorText(e: unknown): string {
  const err = e instanceof Error ? e.message : String(e)
  return t('jobs.diagnosis.error', { err })
}
