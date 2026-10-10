// 声明改动范围（gofer-3nxa.3）：job / todo 的 scope 是一组相对仓库根的路径 glob；验收时用
// job 的 diff 文件列表比对，越界文件在 Diff 页签标「范围外」（只提示，不阻塞 accept）。
// 规则与 Go 侧 internal/job/scope_check.go 一致（两边测试用同一组用例）：
//   `**` 跨任意层目录，`*` / `?` 只在一层内；不含通配符的 glob 也覆盖其下全部（当目录用）；
//   忽略开头的 `./` 与结尾的 `/`。

// diffFiles：unified diff（changes.diff，含 worktree 两段）涉及的文件，按首次出现去重。
// 改名取新路径，删除取原路径（`diff --git a/<old> b/<new>` 的 b 侧）。
export function diffFiles(diff: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  let inList = false // 截断的 diff 末尾附带 "=== changed files ===" 文件名清单（一行一个）
  for (const raw of diff.split('\n')) {
    const line = raw.replace(/\r$/, '')
    if (line.startsWith('=== ')) {
      inList = line === '=== changed files ==='
      continue
    }
    if (inList) {
      if (line !== '' && !seen.has(line)) {
        seen.add(line)
        out.push(line)
      }
      continue
    }
    if (!line.startsWith('diff --git ')) {
      continue
    }
    const rest = line.slice('diff --git '.length)
    const i = rest.lastIndexOf(' b/')
    const p = i >= 0 ? rest.slice(i + 3) : rest.replace(/^a\//, '')
    if (p !== '' && !seen.has(p)) {
      seen.add(p)
      out.push(p)
    }
  }
  return out
}

function globRegExp(glob: string): RegExp {
  let re = '^'
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i]
    if (c === '*' && glob[i + 1] === '*') {
      i++
      if (glob[i + 1] === '/') {
        i++
        re += '(?:.*/)?'
      } else {
        re += '.*'
      }
    } else if (c === '*') {
      re += '[^/]*'
    } else if (c === '?') {
      re += '[^/]'
    } else {
      re += c.replace(/[.+^${}()|[\]\\]/g, '\\$&')
    }
  }
  return new RegExp(re + '$')
}

export function scopeMatch(glob: string, file: string): boolean {
  const g = glob.trim().replace(/^\.\//, '').replace(/\/$/, '')
  const f = file.replace(/^\.\//, '')
  if (g === '') {
    return false
  }
  if (!/[*?]/.test(g)) {
    return f === g || f.startsWith(g + '/')
  }
  return globRegExp(g).test(f)
}

// outOfScope：不被任何 glob 覆盖的文件；scope 为空（没声明）时恒为空。
export function outOfScope(files: readonly string[], scope: readonly string[] | undefined | null): string[] {
  const globs = (scope ?? []).map((g) => g.trim()).filter((g) => g !== '')
  if (globs.length === 0) {
    return []
  }
  return files.filter((f) => !globs.some((g) => scopeMatch(g, f)))
}
