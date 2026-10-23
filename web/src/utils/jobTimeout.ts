import { fmtDuration } from '../api/time'
import { t } from '../i18n'

/** Format the effective timeout shown next to a job's elapsed duration. */
export function fmtJobTimeout(timeoutSec: number | null | undefined): string {
  return timeoutSec != null && timeoutSec > 0 ? t('util.jobTimeout.limit', { d: fmtDuration(timeoutSec) }) : t('util.jobTimeout.unlimited')
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
  const effectiveText = effectiveSec > 0 ? fmtDuration(effectiveSec) : t('util.jobTimeout.unlimited')
  return t('util.jobTimeout.clamped', { req: fmtDuration(requestedSec), eff: effectiveText })
}
