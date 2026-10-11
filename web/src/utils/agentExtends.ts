// extends 的三种写法：builtin（同名内置模板）、builtin:<模板>、<另一个已声明的 agent>。

/** 徽标文字。 */
export function extendsLabel(ext: string | undefined): string {
  if (!ext) return ''
  if (ext === 'builtin') return '继承内置'
  if (ext.startsWith('builtin:')) return `继承内置 ${ext.slice('builtin:'.length).trim()}`
  return `继承 ${ext}`
}

/** 悬停说明里「以 … 为底」的那一段。 */
export function extendsBase(ext: string | undefined): string {
  if (!ext || ext === 'builtin') return '同名内置模板'
  if (ext.startsWith('builtin:')) return `内置模板 ${ext.slice('builtin:'.length).trim()} `
  return ` agent ${ext} `
}
