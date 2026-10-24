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

/**
 * The visible one-line clamp note next to the effective timeout (gofer-ea2i): what was
 * requested and which ceiling cut it. Mirrors `gofer job show`'s
 * "(timeout 14400s clamped to 5400s by server.max_job_timeout_sec)" — the config key is
 * the part a caller needs to raise the limit. Rows from before the source was recorded
 * name only the level (project / server) or, failing that, just the ceiling.
 * Returns undefined when nothing was clamped.
 */
export function jobTimeoutClampNote(
  requestedSec: number | null | undefined,
  effectiveSec: number | null | undefined,
  limitKey?: string | null,
  limitSource?: string | null,
): string | undefined {
  if (
    requestedSec == null ||
    requestedSec <= 0 ||
    effectiveSec == null ||
    requestedSec === effectiveSec
  ) {
    return undefined
  }
  const req = fmtDuration(requestedSec)
  if (limitKey) {
    return t('util.jobTimeout.clampByKey', { req, key: limitKey })
  }
  switch (limitSource) {
    case 'server':
      return t('util.jobTimeout.clampBySource', { req, src: t('util.jobTimeout.limitServer') })
    case 'project':
      return t('util.jobTimeout.clampBySource', { req, src: t('util.jobTimeout.limitProject') })
    default:
      return t('util.jobTimeout.clamp', { req })
  }
}
