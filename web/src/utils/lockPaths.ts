// 锁定目录输入解析（gofer-jkd2）：CLI 的 `--lock` 可重复传入项目根相对的目录，Web 表单里
// 用一个多行/逗号分隔的输入框表达同一组值。空 = 不声明锁（服务端仍按老行为锁整个 CWD）。
//
// 与 internal/job/model.go 的 JobRequest.lock_paths 对齐：值是**项目根相对**路径，由服务端
// 经 SafeJoin 解析、拒绝逃出项目根的写法，所以这里只做拆行/去空白，不校验也不改写路径。
export function parseLockPaths(raw: string): string[] {
  return raw
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter((s) => s !== '')
}

// formatLockPaths 把服务端回填的锁定目录写成输入框文本（一行一个）。rebuild / 继续会话表单
// 用它预填：未改动就不回传该字段，服务端继承源 job 的声明。
export function formatLockPaths(paths: readonly string[] | undefined): string {
  return (paths ?? []).join('\n')
}

// DIR_LOCK_HINT 是锁定目录字段的共用提示：新建 job、新会话、继续会话三处口径一致。
export const DIR_LOCK_HINT =
  '项目根相对，可多个（换行或逗号分隔）；留空 = 锁整个 CWD。只动某个子目录就写它，别的子目录的 job 才不用排队。'
