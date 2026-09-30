import { fmtDuration } from '../api/time'

/** Format the effective timeout shown next to a job's elapsed duration. */
export function fmtJobTimeout(timeoutSec: number | null | undefined): string {
  return timeoutSec != null && timeoutSec > 0 ? `超时 ${fmtDuration(timeoutSec)}` : '不限时'
}

/** Explain a server-side timeout clamp when the requested and effective values differ. */
export function jobTimeoutTitle(
  requestedSec: number | null | undefined,
  effectiveSec: number | null | undefined,
): string | undefined {
  if (
    requestedSec == null ||
    requestedSec <= 0 ||
    effectiveSec == null ||
    requestedSec === effectiveSec
  ) {
    return undefined
  }
  const effectiveText = effectiveSec > 0 ? fmtDuration(effectiveSec) : '不限时'
  return `请求 ${fmtDuration(requestedSec)}，已按上限夹紧为 ${effectiveText}`
}
