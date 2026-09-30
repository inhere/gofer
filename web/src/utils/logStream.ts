export type LogStreamName = 'stdout' | 'stderr'

export interface DefaultLogStreamInput {
  /** Job detail passes true only for exec jobs. */
  autoStderr: boolean
  stdout: string
  stderr: string
}

/**
 * Pick the first log tab without considering later appends or user actions.
 * Exec jobs show stderr first only when stdout is empty; agent jobs always start
 * on stdout even when their process diagnostics are already present.
 */
export function defaultLogStream({ autoStderr, stdout, stderr }: DefaultLogStreamInput): LogStreamName {
  return autoStderr && stdout.length === 0 && stderr.length > 0 ? 'stderr' : 'stdout'
}
