// 与 internal/store/launcher_exec.go 一致；用 internal/store/testdata/launcher_exec_cases.json 表驱动测试（夹具放在 internal/ 下，公开库同步才带得走）。

const BUILTINS = new Set([
  '.', ':', 'alias', 'bg', 'bind', 'break', 'builtin', 'caller', 'cd', 'command', 'compgen', 'complete',
  'compopt', 'continue', 'declare', 'dirs', 'disown', 'echo', 'enable', 'eval', 'exec', 'exit', 'export',
  'false', 'fc', 'fg', 'getopts', 'hash', 'help', 'history', 'jobs', 'kill', 'let', 'local', 'logout',
  'mapfile', 'popd', 'printf', 'pushd', 'pwd', 'read', 'readarray', 'readonly', 'return', 'set', 'shift',
  'shopt', 'source', 'suspend', 'test', 'times', 'trap', 'true', 'type', 'typeset', 'ulimit', 'umask',
  'unalias', 'unset', 'wait',
])

export function classifyLauncherExec(command: string): { ok: true; exec: string } | { ok: false; message: string } {
  const cmd = command.trim()
  if (!cmd) return { ok: true, exec: '' }
  if (/[|&;<>(){}`$]/.test(cmd)) {
    return { ok: false, message: '命令里含有 shell 运算符、重定向或命令替换，不能开启「命令退出即结束会话」' }
  }
  if (cmd.includes('$(') || cmd.includes('${')) {
    return { ok: false, message: '命令里含有命令替换，不能开启「命令退出即结束会话」' }
  }
  for (const op of ['&&', '||', ';;']) {
    if (cmd.includes(op)) return { ok: false, message: '命令是复合语句，不能开启「命令退出即结束会话」' }
  }
  const words = splitShellWords(cmd)
  if (!words.ok) return { ok: false, message: words.message }
  if (words.words.length === 0) return { ok: true, exec: '' }
  let i = 0
  const env: string[] = []
  while (i < words.words.length && isEnvAssignment(words.words[i])) {
    env.push(words.words[i])
    i++
  }
  if (i >= words.words.length) {
    return { ok: false, message: '只有环境变量赋值、没有实际命令，不能开启「命令退出即结束会话」' }
  }
  if (BUILTINS.has(words.words[i].toLowerCase())) {
    return { ok: false, message: `「${words.words[i]}」是 shell 内建命令，不能开启「命令退出即结束会话」` }
  }
  const rest = words.words.slice(i).join(' ')
  const exec = env.length === 0 ? `exec ${rest}` : `exec env ${env.join(' ')} ${rest}`
  return { ok: true, exec }
}

function isEnvAssignment(w: string): boolean {
  const eq = w.indexOf('=')
  if (eq <= 0) return false
  const name = w.slice(0, eq)
  if (/^\d/.test(name)) return false
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(name)
}

function splitShellWords(s: string): { ok: true; words: string[] } | { ok: false; message: string } {
  const out: string[] = []
  let b = ''
  let inSingle = false
  let escaped = false
  for (const r of s) {
    if (escaped) {
      b += r
      escaped = false
      continue
    }
    if (r === '\\' && !inSingle) {
      escaped = true
      continue
    }
    if (r === "'") {
      b += r
      inSingle = !inSingle
      continue
    }
    if (!inSingle && /\s/.test(r)) {
      if (b) {
        out.push(b)
        b = ''
      }
      continue
    }
    b += r
  }
  if (escaped) return { ok: false, message: '命令末尾的反斜杠不完整，不能开启「命令退出即结束会话」' }
  if (inSingle) return { ok: false, message: '命令里的单引号没有配对，不能开启「命令退出即结束会话」' }
  if (b) out.push(b)
  return { ok: true, words: out }
}
