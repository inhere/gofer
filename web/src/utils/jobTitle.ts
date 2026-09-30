export const JOB_TITLE_MAX_LENGTH = 32

export function normalizeJobTitle(value: string): string {
  return Array.from(value.trim()).slice(0, JOB_TITLE_MAX_LENGTH).join('')
}
