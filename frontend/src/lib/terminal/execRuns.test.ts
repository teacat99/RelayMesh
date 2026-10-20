// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { RunView, ThreadView } from '../../api/terminal'
import {
  APPROVAL_TTL_MS,
  LIVE_CAP_BYTES,
  SseParser,
  appendLive,
  approvalRemaining,
  cleanDisplay,
  collapsedPreview,
  decodeBase64,
  denyReasonLabel,
  distinctCallers,
  emptyLive,
  filterRuns,
  formatBytes,
  formatDuration,
  isActiveRun,
  isFinalRun,
  liveText,
  mergeRun,
  mergeRuns,
  modeLabel,
  previewLines,
  runElapsed,
  runStateInfo,
  runTitle,
  sortRuns,
  threadSubtitle,
  threadTitle,
  toolList,
} from './execRuns'

function run(id: string, over: Partial<RunView> = {}): RunView {
  return {
    run_id: id.padEnd(32, '0'),
    thread_key: 'tk',
    actor_kind: 'model',
    credential_id: 7,
    credential_name: 'cursor-main',
    target: 'ABCD',
    command: 'echo hi',
    state: 'succeeded',
    duration_ms: 300,
    timeout_sec: 120,
    stdout_bytes: 3,
    stderr_bytes: 0,
    truncated: false,
    middle_dropped_bytes: 0,
    created_at: '2026-10-02T10:00:00Z',
    updated_at: '2026-10-02T10:00:01Z',
    ...over,
  }
}

describe('状态', () => {
  it('终态与进行中互斥且覆盖全部状态', () => {
    for (const s of ['succeeded', 'failed', 'timeout', 'cancelled', 'rejected', 'denied', 'lost']) {
      assert.equal(isFinalRun(s), true, s)
      assert.equal(isActiveRun(s), false, s)
    }
    for (const s of ['awaiting_approval', 'running']) {
      assert.equal(isFinalRun(s), false, s)
      assert.equal(isActiveRun(s), true, s)
    }
    assert.equal(isFinalRun('weird'), false)
  })

  it('状态位文案', () => {
    assert.deepEqual(runStateInfo({ state: 'succeeded', exit_code: 0 }), { label: '成功', tone: 'ok', detail: '退出码 0' })
    assert.deepEqual(runStateInfo({ state: 'failed', exit_code: 2 }), { label: '失败', tone: 'bad', detail: '退出码 2' })
    assert.deepEqual(runStateInfo({ state: 'failed', signal: 'SIGKILL' }), { label: '失败', tone: 'bad', detail: '被信号 SIGKILL 终止' })
    assert.equal(runStateInfo({ state: 'awaiting_approval' }).tone, 'warn')
    assert.equal(runStateInfo({ state: 'running' }).tone, 'run')
    assert.equal(runStateInfo({ state: 'timeout' }).label, '超时')
    // 到点结束：说清「达到运行时长上限」以及上限（#141）；旧记录没有超时值就不写括号。
    assert.equal(runStateInfo({ state: 'timeout', timeout_sec: 1800 }).detail, '达到运行时长上限（30 分钟）')
    assert.equal(runStateInfo({ state: 'timeout', timeout_sec: 604800 }).detail, '达到运行时长上限（7 天）')
    assert.equal(runStateInfo({ state: 'timeout' }).detail, '达到运行时长上限')
    assert.equal(runStateInfo({ state: 'rejected', deny_reason: 'rejected_by_user' }).detail, '被你拒绝')
    assert.equal(runStateInfo({ state: 'denied', deny_reason: 'host_denied' }).tone, 'bad')
    assert.equal(runStateInfo({ state: 'lost', deny_reason: 'not_on_host' }).detail, '主机上找不到这次运行')
    // 没有退出码的失败把原因说出来（含 SSH 的连接 / 认证失败）；有退出码时仍以退出码为准。
    assert.equal(runStateInfo({ state: 'failed', deny_reason: 'ssh_auth_failed' }).detail, 'SSH 认证失败，请检查用户名与凭据')
    assert.equal(runStateInfo({ state: 'failed', deny_reason: 'bad_cwd' }).detail, '工作目录不存在或不是目录')
    assert.equal(runStateInfo({ state: 'failed', exit_code: 3, deny_reason: 'ssh_unreachable' }).detail, '退出码 3')
    assert.equal(runStateInfo({ state: 'failed', signal: 'SIGKILL', deny_reason: 'output_limit' }).detail, '被信号 SIGKILL 终止（输出超过上限，进程已被终止）')
    assert.equal(runStateInfo({ state: 'lost', deny_reason: 'proc_gone' }).detail, '远端进程已经不存在，没有留下结果')
    assert.equal(runStateInfo({ state: 'mystery' }).label, 'mystery')
    assert.equal(runStateInfo({ state: '' }).label, '未知')
  })

  it('未知原因码原样返回', () => {
    assert.equal(denyReasonLabel('approval_timeout'), '等待批准超时')
    assert.equal(denyReasonLabel('something_new'), 'something_new')
    assert.equal(denyReasonLabel(undefined), '')
  })

  it('已取消：写明是谁、为什么取消的（总闸关闭 / 主机停用 / 令牌变更 / 不认识的运行），旧记录说清当时没记下具体原因', () => {
    const detail = (reason?: string) => runStateInfo({ state: 'cancelled', deny_reason: reason }).detail
    assert.match(detail('gate_closed'), /总闸关闭/)
    assert.match(detail('host_removed'), /停用或删除/)
    assert.match(detail('token_changed'), /令牌/)
    assert.match(detail('unknown_run'), /不认识/)
    assert.match(detail('revoked'), /授权被收回/)
    assert.match(detail('pause'), /暂停/)
    assert.match(detail('user'), /你取消了/)
    assert.match(detail('model'), /模型取消了/)
    assert.match(detail('system'), /旧版本.*没有记下具体是哪一种/)
    assert.equal(detail(undefined), '', '没有原因码时不编造原因')
    assert.equal(runStateInfo({ state: 'cancelled', deny_reason: 'gate_closed' }).label, '已取消')
    assert.equal(runStateInfo({ state: 'cancelled', deny_reason: 'gate_closed' }).tone, 'muted')
  })
})

describe('标题与时长', () => {
  it('标题优先取意图，其次命令第一行，并限长', () => {
    assert.equal(runTitle({ description: '查看磁盘', command: 'df -h' }), '查看磁盘')
    assert.equal(runTitle({ description: '  ', command: 'cd /tmp\nls' }), 'cd /tmp')
    assert.equal(runTitle({ description: '', command: '' }), '（空命令）')
    const long = runTitle({ description: 'x'.repeat(200), command: '' }, 20)
    assert.equal(long.length, 20)
    assert.ok(long.endsWith('…'))
  })

  it('命令名清单', () => {
    assert.deepEqual(toolList({ tools: 'cd, git ,head' }), ['cd', 'git', 'head'])
    assert.deepEqual(toolList({ tools: '' }), [])
    assert.deepEqual(toolList({}), [])
  })

  it('时长写法', () => {
    assert.equal(formatDuration(40), '<0.1s')
    assert.equal(formatDuration(300), '0.3s')
    assert.equal(formatDuration(9_940), '9.9s')
    assert.equal(formatDuration(9_950), '10s')
    assert.equal(formatDuration(12_400), '12s')
    assert.equal(formatDuration(125_000), '2m05s')
    assert.equal(formatDuration(3_780_000), '1h03m')
    assert.equal(formatDuration(-1), '')
    assert.equal(formatDuration(Number.NaN), '')
  })

  it('字节写法', () => {
    assert.equal(formatBytes(512), '512 B')
    assert.equal(formatBytes(1536), '1.5 KiB')
    assert.equal(formatBytes(20 * 1024), '20 KiB')
    assert.equal(formatBytes(3 * 1024 * 1024), '3.0 MiB')
  })

  it('已用时间：待批准没有，运行中实时，结束后用记录值，从未开始的没有', () => {
    const now = Date.parse('2026-10-02T10:00:10Z')
    assert.equal(runElapsed({ state: 'awaiting_approval', started_at: undefined, duration_ms: 0 }, now), null)
    assert.equal(runElapsed({ state: 'running', started_at: '2026-10-02T10:00:04Z', duration_ms: 0 }, now), 6000)
    assert.equal(runElapsed({ state: 'running', started_at: undefined, duration_ms: 0 }, now), null)
    assert.equal(runElapsed({ state: 'succeeded', started_at: '2026-10-02T10:00:00Z', duration_ms: 300 }, now), 300)
    assert.equal(runElapsed({ state: 'rejected', started_at: undefined, duration_ms: 0 }, now), null)
  })
})

describe('待批准与模式', () => {
  it('待批准剩余时间：只有待批准的有；超时为 0', () => {
    const created = '2026-10-02T10:00:00Z'
    const t0 = Date.parse(created)
    assert.equal(approvalRemaining({ state: 'awaiting_approval', created_at: created }, t0 + 60_000), APPROVAL_TTL_MS - 60_000)
    assert.equal(approvalRemaining({ state: 'awaiting_approval', created_at: created }, t0 + APPROVAL_TTL_MS + 5), 0)
    assert.equal(approvalRemaining({ state: 'running', created_at: created }, t0), null)
    assert.equal(approvalRemaining({ state: 'awaiting_approval', created_at: 'bad' }, t0), null)
  })

  it('执行模式名称', () => {
    assert.equal(modeLabel('approve'), '需批准')
    assert.equal(modeLabel('readonly'), '只读')
    assert.equal(modeLabel('auto'), '自动')
    assert.equal(modeLabel(''), '自动')
    assert.equal(modeLabel(undefined), '自动')
    assert.equal(modeLabel('x'), 'x')
  })
})

describe('输出展示', () => {
  it('去掉转义序列、控制字符与双向控制符', () => {
    assert.equal(cleanDisplay('\u001b[31mred\u001b[0m ok'), 'red ok')
    assert.equal(cleanDisplay('a\u0007b\u0000c'), 'abc')
    assert.equal(cleanDisplay('\u001b]0;title\u0007text'), 'text')
    assert.equal(cleanDisplay('safe\u202eevil'), 'safeevil')
    assert.equal(cleanDisplay('tab\tkept\nnewline kept'), 'tab\tkept\nnewline kept')
  })

  it('同一行里的回车只保留最后一次刷新，CRLF 不受影响', () => {
    assert.equal(cleanDisplay('10%\r50%\r100%\ndone'), '100%\ndone')
    assert.equal(cleanDisplay('line1\r\nline2\r\n'), 'line1\nline2\n')
    assert.equal(cleanDisplay(''), '')
  })

  it('前 N 行与总行数', () => {
    const p = previewLines('a\nb\nc\nd\ne\n', 3)
    assert.deepEqual(p, { lines: ['a', 'b', 'c'], total: 5, more: true, exact: true })
    assert.deepEqual(previewLines('a\nb', 4), { lines: ['a', 'b'], total: 2, more: false, exact: true })
    assert.deepEqual(previewLines('', 4), { lines: [], total: 0, more: false, exact: true })
    assert.deepEqual(previewLines(undefined, 4), { lines: [], total: 0, more: false, exact: true })
    assert.equal(previewLines('a', 4, true).more, true)
    assert.equal(previewLines('a', 4, true).exact, false)
    assert.deepEqual(previewLines('a\nb', 0), { lines: [], total: 2, more: true, exact: true })
  })

  it('折叠预览：输出比预览多时标记还有更多', () => {
    const full = collapsedPreview({ preview_head: 'a\nb\n', stdout_bytes: 4, stderr_bytes: 0 }, 4)
    assert.equal(full.more, false)
    const cut = collapsedPreview({ preview_head: 'a\nb\n', stdout_bytes: 9000, stderr_bytes: 0 }, 4)
    assert.equal(cut.more, true)
    const none = collapsedPreview({ preview_head: '', stdout_bytes: 0, stderr_bytes: 0 }, 4)
    assert.deepEqual(none, { lines: [], total: 0, more: false, exact: true })
    const multibyte = collapsedPreview({ preview_head: '你好\n', stdout_bytes: 7, stderr_bytes: 0 }, 4)
    assert.equal(multibyte.more, false, '按字节而不是字符数比较')
  })
})

describe('列表', () => {
  it('按创建时间排序，相同时按运行 ID 稳定', () => {
    const a = run('a', { created_at: '2026-10-02T10:00:03Z' })
    const b = run('b', { created_at: '2026-10-02T10:00:01Z' })
    const c = run('c', { created_at: '2026-10-02T10:00:01Z' })
    assert.deepEqual(sortRuns([a, c, b]).map(r => r.run_id[0]), ['b', 'c', 'a'])
    assert.deepEqual(sortRuns([a, c, b], 'desc').map(r => r.run_id[0]), ['a', 'c', 'b'])
  })

  it('合并：新增、更新、终态不被旧状态覆盖', () => {
    const r1 = run('a', { state: 'running', updated_at: '2026-10-02T10:00:01Z' })
    let list = mergeRun([], r1)
    assert.equal(list.length, 1)
    const done = run('a', { state: 'succeeded', updated_at: '2026-10-02T10:00:05Z' })
    list = mergeRun(list, done)
    assert.equal(list[0].state, 'succeeded')
    // 迟到的“运行中”事件不能把终态改回去。
    list = mergeRun(list, r1)
    assert.equal(list[0].state, 'succeeded')
    // 迟到的、更旧的终态也不覆盖。
    const older = run('a', { state: 'failed', updated_at: '2026-10-02T10:00:02Z' })
    list = mergeRun(list, older)
    assert.equal(list[0].state, 'succeeded')
    // 合并一批。
    const merged = mergeRuns(list, [run('b', { created_at: '2026-10-02T09:00:00Z' }), run('c', { created_at: '2026-10-02T11:00:00Z' })])
    assert.deepEqual(merged.map(r => r.run_id[0]), ['b', 'a', 'c'])
  })

  it('合并不改变入参', () => {
    const base = [run('a')]
    const out = mergeRun(base, run('b', { created_at: '2026-10-02T11:00:00Z' }))
    assert.equal(base.length, 1)
    assert.equal(out.length, 2)
  })

  it('筛选与关键词', () => {
    const list = [
      run('a', { state: 'running', description: '重启服务' }),
      run('b', { state: 'failed', command: 'false' }),
      run('c', { state: 'timeout', tools: 'sleep' }),
      run('d', { state: 'rejected' }),
      run('e', { state: 'denied', credential_name: 'chatgpt-ops' }),
      run('f', { state: 'awaiting_approval' }),
      run('g', { state: 'succeeded' }),
    ]
    const ids = (l: RunView[]) => l.map(r => r.run_id[0]).join('')
    assert.equal(ids(filterRuns(list, 'all', '')), 'abcdefg')
    assert.equal(ids(filterRuns(list, 'active', '')), 'af')
    assert.equal(ids(filterRuns(list, 'failed', '')), 'bc')
    assert.equal(ids(filterRuns(list, 'denied', '')), 'de')
    assert.equal(ids(filterRuns(list, 'all', '  重启 ')), 'a')
    assert.equal(ids(filterRuns(list, 'all', 'SLEEP')), 'c')
    assert.equal(ids(filterRuns(list, 'all', 'chatgpt')), 'e')
    assert.equal(ids(filterRuns(list, 'active', 'chatgpt')), '')
  })

  it('调用方去重', () => {
    assert.deepEqual(distinctCallers([run('a'), run('b'), run('c', { credential_name: 'x' }), run('d', { credential_name: '' })]), ['cursor-main', 'x'])
  })
})

describe('线程', () => {
  const t = (over: Partial<ThreadView> = {}): ThreadView => ({
    key: 'k',
    target: 'ABCD',
    credential_id: 7,
    credential_name: 'chatgpt-ops',
    total: 3,
    active: 0,
    awaiting: 0,
    first_at: '',
    last_at: '',
    last_run_id: '',
    last_state: 'succeeded',
    last_title: '查看磁盘',
    ...over,
  })

  it('线程名', () => {
    assert.equal(threadTitle(t(), 'prod-host'), 'chatgpt-ops · prod-host')
    assert.equal(threadTitle(t({ workflow_id: 'wf-1' }), 'prod-host'), 'wf-1 · prod-host')
    assert.equal(threadTitle(t({ credential_name: '' }), ''), '未命名调用方')
  })

  it('第二行', () => {
    assert.equal(threadSubtitle(t()), '最近：查看磁盘')
    assert.equal(threadSubtitle(t({ thread: 'deploy', active: 2 })), '#deploy · 2 条运行中 · 最近：查看磁盘')
    assert.equal(threadSubtitle(t({ active: 3, awaiting: 1 })), '1 条待批准 · 最近：查看磁盘')
    assert.equal(threadSubtitle(t({ last_title: '' })), '')
  })
})

describe('事件流解析', () => {
  it('完整事件、多行 data、注释与默认事件名', () => {
    const p = new SseParser()
    const out = p.push('event: pause\ndata: {"paused":false}\n\n: ping\n\ndata: a\ndata: b\n\n')
    assert.deepEqual(out, [
      { event: 'pause', data: '{"paused":false}' },
      { event: 'message', data: 'a\nb' },
    ])
  })

  it('任意切分都能还原', () => {
    const whole = 'event: run\ndata: {"a":1}\n\nevent: out\ndata: {"b":2}\n\n'
    for (let cut = 1; cut < whole.length; cut++) {
      const p = new SseParser()
      const got = [...p.push(whole.slice(0, cut)), ...p.push(whole.slice(cut))]
      assert.deepEqual(got, [
        { event: 'run', data: '{"a":1}' },
        { event: 'out', data: '{"b":2}' },
      ], `在 ${cut} 处切开`)
    }
  })

  it('兼容 CRLF，并忽略 data 值前的单个空格', () => {
    const p = new SseParser()
    assert.deepEqual(p.push('event: x\r\ndata: y\r\n\r\n'), [{ event: 'x', data: 'y' }])
    assert.deepEqual(p.push('data:  two spaces\n\n'), [{ event: 'message', data: ' two spaces' }])
  })

  it('没有结束空行的事件不会提前吐出', () => {
    const p = new SseParser()
    assert.deepEqual(p.push('event: run\ndata: 1\n'), [])
    assert.deepEqual(p.push('\n'), [{ event: 'run', data: '1' }])
  })
})

describe('实时输出', () => {
  it('追加、去重与偏移', () => {
    let l = emptyLive()
    l = appendLive(l, 'stdout', 0, 'hello ', 6)
    l = appendLive(l, 'stdout', 6, 'world\n', 6)
    assert.equal(liveText(l), 'hello world\n')
    assert.deepEqual(l.off, { stdout: 12, stderr: 0 })
    const dup = appendLive(l, 'stdout', 0, 'hello ', 6)
    assert.equal(dup, l, '完全重复的片段直接忽略')
    l = appendLive(l, 'stderr', 0, 'warn\n', 5)
    assert.equal(liveText(l), 'hello world\nwarn\n')
    assert.deepEqual(l.off, { stdout: 12, stderr: 5 })
    assert.equal(l.partial, false)
  })

  it('偏移跳跃说明丢包', () => {
    let l = appendLive(emptyLive(), 'stdout', 0, 'a', 1)
    l = appendLive(l, 'stdout', 100, 'b', 1)
    assert.equal(l.partial, true)
    assert.equal(liveText(l), 'ab')
  })

  it('超过上限丢弃最早的片段并标记不完整', () => {
    let l = emptyLive()
    const piece = 'x'.repeat(1024)
    for (let i = 0; i < 20; i++) l = appendLive(l, 'stdout', i * 1024, piece, 1024, 8 * 1024)
    assert.ok(l.size <= 8 * 1024)
    assert.ok(l.chunks.length <= 8)
    assert.equal(l.partial, true)
    assert.equal(LIVE_CAP_BYTES, 256 * 1024)
  })

  it('入参不被修改', () => {
    const a = appendLive(emptyLive(), 'stdout', 0, 'a', 1)
    const b = appendLive(a, 'stdout', 1, 'b', 1)
    assert.equal(liveText(a), 'a')
    assert.equal(liveText(b), 'ab')
  })

  it('base64 解码', () => {
    assert.equal(new TextDecoder().decode(decodeBase64('aGVsbG8=')), 'hello')
    assert.equal(decodeBase64('###').length, 0)
    assert.equal(decodeBase64('').length, 0)
  })
})
