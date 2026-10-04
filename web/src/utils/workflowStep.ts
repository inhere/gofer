// 工作流步骤进度显示：done 之后后端的 current_step 会越过 total_steps（如 2/1），
// 显示层统一夹到 total，并在 done 时标注 (done)。只改显示，不改接口值。
export function workflowStepText(status: string, current: number, total: number): string {
  const cur = total > 0 && current > total ? total : current
  const base = `${cur}/${total}`
  return status === 'done' ? `${base} (done)` : base
}
