import type { PageDict } from './types'

export const docReader: PageDict = {
  zh: {
    'docReader.loading': '加载文档…',
    'docReader.copy': '复制阅读页地址',
    'docReader.copied': '已复制',
    'docReader.line': '引用第 {line} 行',
    'docReader.truncated': '文档较大，仅显示前 256KB',
    'docReader.toc': '目录',
    'docReader.imagePlaceholder': '图片',
    'docReader.error.status': '请求失败（{status}）',
    'docReader.error.missing': '文件不存在，或尚未在 server 可读的项目目录中。',
    'docReader.error.missingWorktree': '文件不存在，或只在该 job 的 worktree（分支「{branch}」）里；合并后可读。',
    'docReader.error.remote': '该项目在 worker 上运行，server 读不到文件。',
    'docReader.error.forbidden': '不允许读取这类文件。',
    'docReader.error.binary': '这不是文本文件。',
  },
  en: {
    'docReader.loading': 'Loading document…',
    'docReader.copy': 'Copy reader URL',
    'docReader.copied': 'Copied',
    'docReader.line': 'Reference line {line}',
    'docReader.truncated': 'The document is large; only the first 256KB is shown.',
    'docReader.toc': 'Contents',
    'docReader.imagePlaceholder': 'Image',
    'docReader.error.status': 'Request failed ({status})',
    'docReader.error.missing': 'The file does not exist or is not readable by this server project.',
    'docReader.error.missingWorktree': 'The file does not exist here, or only exists in the job worktree (branch “{branch}”); it will be readable after merge.',
    'docReader.error.remote': 'This project runs on a worker; the server cannot read its files.',
    'docReader.error.forbidden': 'This kind of file cannot be read.',
    'docReader.error.binary': 'This is not a text file.',
  },
}
