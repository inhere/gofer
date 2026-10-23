// extends 的三种写法：builtin（同名内置模板）、builtin:<模板>、<另一个已声明的 agent>。
// 文案在 i18n 的 util.agentExtends.*（P6-b 起走 t()）。
import { t } from '../i18n'

/** 徽标文字。 */
export function extendsLabel(ext: string | undefined): string {
  if (!ext) return ''
  if (ext === 'builtin') return t('util.agentExtends.builtin')
  if (ext.startsWith('builtin:')) return t('util.agentExtends.builtinNamed', { name: ext.slice('builtin:'.length).trim() })
  return t('util.agentExtends.named', { ext })
}

/** 悬停说明里「以 … 为底」的那一段。 */
export function extendsBase(ext: string | undefined): string {
  if (!ext || ext === 'builtin') return t('util.agentExtends.baseBuiltin')
  if (ext.startsWith('builtin:')) return `${t('util.agentExtends.baseBuiltinNamed', { name: ext.slice('builtin:'.length).trim() })} `
  return ` agent ${ext} `
}
