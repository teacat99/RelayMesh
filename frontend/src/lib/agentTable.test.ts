import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { computed, effectScope, nextTick, ref, watchEffect } from 'vue'
import { applyStateEvent, groupByWorkflow, pickSession, type AgentIdentity, type AgentSession, type AgentStateEvent } from './agentState'
import { createAgentTable, sameRecord, type AgentTable } from './agentTable'

const T0 = Date.UTC(2026, 9, 21, 2, 0, 0)
const iso = (offsetSec: number) => new Date(T0 + offsetSec * 1000).toISOString()

function sess(id: number, wf: string, extra: Partial<AgentSession> = {}): AgentSession {
  return {
    id,
    identity_id: 1,
    workflow_id: wf,
    conversation_id: `conv-${id}`,
    identity_label: 'app',
    host_name: 'h',
    project_directory: '/p/app',
    last_event_at: iso(id),
    state: 'idle',
    base_state: 'idle',
    since: iso(0),
    quiet_seconds: 0,
    waiting_you: false,
    stalled: false,
    stalled_soft: false,
    approval_suspected: false,
    approval_overdue: false,
    ...extra,
  }
}

function ident(id: number, label = 'app', extra: Partial<AgentIdentity> = {}): AgentIdentity {
  return {
    id,
    credential_id: 1,
    host_name: 'h',
    project_directory: `/p/${label}`,
    name: '',
    directory_name: label,
    label,
    label_degraded: false,
    first_seen_at: iso(0),
    last_seen_at: iso(1),
    ...extra,
  }
}

const ev = (wf: string, sessionId: number, state: string, sub?: string): AgentStateEvent => ({ workflow_id: wf, identity_id: 1, session_id: sessionId, state, sub })

/** 把表里的分组整理成 {工作流: [会话编号…]}，便于比较。 */
const idsOf = (t: AgentTable) => Object.fromEntries([...t.groups].map(([wf, g]) => [wf, g.map(s => s.id)]))

describe('sameRecord：扁平记录逐字段比较', () => {
  it('同引用、同内容相同；多键 / 少键 / 值不同 / 一个键缺失一个为 undefined 都不同', () => {
    const a = { x: 1, y: 'a', z: undefined as string | undefined }
    assert.equal(sameRecord(a, a), true)
    assert.equal(sameRecord(a, { x: 1, y: 'a', z: undefined }), true)
    assert.equal(sameRecord(a, { x: 2, y: 'a', z: undefined }), false)
    assert.equal(sameRecord({ x: 1 } as Record<string, unknown>, { x: 1, y: 2 }), false)
    assert.equal(sameRecord({ x: undefined } as Record<string, unknown>, { y: undefined }), false)
  })
})

describe('createAgentTable：内容与原来的整表分组一致', () => {
  it('mergeOverview：按工作流分组、组内最近动静在前，没有工作流号的会话不入表', () => {
    const t = createAgentTable()
    const list = [sess(1, 'a'), sess(3, 'a'), sess(2, 'b'), sess(4, 'a'), sess(5, '')]
    t.mergeOverview(list)
    assert.deepEqual(idsOf(t), { a: [4, 3, 1], b: [2] })
    // 与旧实现（扁平数组 + groupByWorkflow）逐项一致
    const old = groupByWorkflow(list)
    for (const [wf, g] of old) assert.deepEqual(t.groups.get(wf)?.map(s => s.id), g.map(s => s.id))
  })

  it('mergeOverview：只替换清单里出现的工作流，其余工作流原样保留（概览有上限，单独取过的不能被冲掉）', () => {
    const t = createAgentTable()
    t.mergeOverview([sess(1, 'a'), sess(2, 'b')])
    t.mergeOverview([sess(3, 'a')])
    assert.deepEqual(idsOf(t), { a: [3], b: [2] })
  })

  it('replaceWorkflow：换掉这个工作流的会话；传空数组就是这个工作流没有会话了', () => {
    const t = createAgentTable()
    t.mergeOverview([sess(1, 'a'), sess(2, 'b')])
    t.replaceWorkflow('a', [sess(7, 'a'), sess(8, 'a')])
    assert.deepEqual(idsOf(t), { a: [8, 7], b: [2] })
    t.replaceWorkflow('a', [])
    assert.deepEqual(idsOf(t), { b: [2] })
    assert.equal(t.groups.has('a'), false)
  })

  it('patchEvent：与 applyStateEvent 同一个结果，并让这个会话排到最前', () => {
    const t = createAgentTable()
    const list = [sess(1, 'a', { last_event_at: iso(10) }), sess(2, 'a', { last_event_at: iso(5) })]
    t.mergeOverview(list)
    const nowIso = iso(20)
    assert.equal(t.patchEvent(ev('a', 2, 'working', 'thinking'), nowIso), true)
    const expected = groupByWorkflow(applyStateEvent(list, ev('a', 2, 'working', 'thinking'), nowIso).list).get('a')!
    assert.deepEqual(t.groups.get('a'), expected)
    assert.equal(t.groups.get('a')?.[0].id, 2)
    assert.equal(t.groups.get('a')?.[0].state, 'working')
  })

  it('patchEvent：找不到这个会话返回 false 且不改任何东西；事件里的工作流号不对时按会话编号找', () => {
    const t = createAgentTable()
    t.mergeOverview([sess(1, 'a')])
    assert.equal(t.patchEvent(ev('a', 99, 'working'), iso(30)), false)
    assert.deepEqual(idsOf(t), { a: [1] })
    assert.equal(t.groups.get('a')?.[0].state, 'idle')
    assert.equal(t.patchEvent(ev('zzz', 1, 'working'), iso(30)), true)
    assert.equal(t.groups.get('a')?.[0].state, 'working')
  })

  it('setIdentities：清单里没有的删掉，内容没变的保留旧对象', () => {
    const t = createAgentTable()
    const a = ident(1, 'one')
    t.setIdentities([a, ident(2, 'two')])
    t.setIdentities([{ ...a }, ident(3, 'three')])
    assert.deepEqual([...t.identities.keys()].sort(), [1, 3])
    assert.equal(t.identities.get(1), a, '内容没变，仍是第一次的那个对象')
  })

  it('relabelSessions：只改标签变了的会话，其余原样（同一个对象）', () => {
    const t = createAgentTable()
    t.mergeOverview([sess(1, 'a', { identity_id: 1, identity_label: 'old' }), sess(2, 'b', { identity_id: 2, identity_label: 'keep' })])
    const b0 = t.groups.get('b')
    t.relabelSessions(new Map([[1, ident(1, 'new')], [2, ident(2, 'keep')]]))
    assert.equal(t.groups.get('a')?.[0].identity_label, 'new')
    assert.equal(t.groups.get('b'), b0)
  })
})

describe('createAgentTable：按键失效——一个事件只唤醒读这个工作流的人（#215）', () => {
  /** 给每个工作流装一个「卡片」：读 pickSession(groups.get(wf))，记录被重算 / 渲染的次数。 */
  function cards(t: AgentTable, wfs: string[]) {
    const evals: Record<string, number> = {}
    const renders: Record<string, number> = {}
    const scope = effectScope()
    scope.run(() => {
      for (const wf of wfs) {
        evals[wf] = 0
        renders[wf] = 0
        const entry = computed(() => {
          evals[wf]++
          const picked = pickSession(t.groups.get(wf))
          return { ...picked, identity: picked.primary ? (t.identities.get(picked.primary.identity_id) ?? null) : null }
        })
        watchEffect(() => {
          void entry.value
          renders[wf]++
        })
      }
    })
    return { evals, renders, stop: () => scope.stop() }
  }

  const WFS = Array.from({ length: 30 }, (_, i) => `wf-${i}`)
  function seeded() {
    const t = createAgentTable()
    t.setIdentities([ident(1, 'app')])
    t.mergeOverview(WFS.map((wf, i) => sess(i + 1, wf)))
    return t
  }
  const total = (m: Record<string, number>) => Object.values(m).reduce((a, b) => a + b, 0)

  it('对照：原来的整表写法（扁平数组 + 整体分组的 computed），一个事件把三十张卡全部唤醒', async () => {
    const sessions = ref<AgentSession[]>(WFS.map((wf, i) => sess(i + 1, wf)))
    const byWorkflow = computed(() => groupByWorkflow(sessions.value))
    const renders: Record<string, number> = {}
    const scope = effectScope()
    scope.run(() => {
      for (const wf of WFS) {
        renders[wf] = 0
        const entry = computed(() => ({ ...pickSession(byWorkflow.value.get(wf)) }))
        watchEffect(() => {
          void entry.value
          renders[wf]++
        })
      }
    })
    await nextTick()
    const before = total(renders)
    sessions.value = applyStateEvent(sessions.value, ev('wf-7', 8, 'working', 'thinking'), iso(100)).list
    await nextTick()
    assert.equal(total(renders) - before, WFS.length, '这就是 #215 要消掉的：一个工作流的事件让所有读 byWorkflow 的卡片都重画')
    scope.stop()
  })

  it('一个工作流来了十个事件：只有这一个工作流的卡片被重算 / 重画，另外二十九张一次都没有', async () => {
    const t = seeded()
    const c = cards(t, WFS)
    await nextTick()
    const base = { evals: { ...c.evals }, renders: { ...c.renders } }
    for (let i = 0; i < 10; i++) {
      t.patchEvent(ev('wf-7', 8, i % 2 === 0 ? 'working' : 'idle', i % 2 === 0 ? 'tool:Shell' : undefined), iso(100 + i))
      await nextTick()
    }
    for (const wf of WFS) {
      if (wf === 'wf-7') continue
      assert.equal(c.evals[wf], base.evals[wf], `${wf} 不该被重算`)
      assert.equal(c.renders[wf], base.renders[wf], `${wf} 不该被重画`)
    }
    assert.equal(c.renders['wf-7'] - base.renders['wf-7'], 10)
    assert.equal(total(c.renders) - total(base.renders), 10)
    c.stop()
  })

  it('整表校准 / 单个工作流重取拿回的内容与缓存相同：谁都不会被唤醒', async () => {
    const t = seeded()
    const c = cards(t, WFS)
    await nextTick()
    const before = total(c.evals)
    // 服务端返回的是新解析出来的对象（内容相同、引用不同）
    t.mergeOverview(WFS.map((wf, i) => sess(i + 1, wf)))
    t.replaceWorkflow('wf-3', [sess(4, 'wf-3')])
    t.setIdentities([ident(1, 'app')])
    await nextTick()
    assert.equal(total(c.evals), before)
    c.stop()
  })

  it('重取带回了新内容：只有这一个工作流被唤醒', async () => {
    const t = seeded()
    const c = cards(t, WFS)
    await nextTick()
    const before = { ...c.renders }
    t.replaceWorkflow('wf-3', [sess(4, 'wf-3', { stalled: true, reason: 'quiet' })])
    await nextTick()
    for (const wf of WFS) assert.equal(c.renders[wf] - before[wf], wf === 'wf-3' ? 1 : 0, wf)
    c.stop()
  })

  it('一个身份改了名：只有指向它的工作流被唤醒', async () => {
    const t = createAgentTable()
    t.setIdentities([ident(1, 'one'), ident(2, 'two')])
    t.mergeOverview([sess(1, 'wf-a', { identity_id: 1 }), sess(2, 'wf-b', { identity_id: 2 }), sess(3, 'wf-c', { identity_id: 2 })])
    const c = cards(t, ['wf-a', 'wf-b', 'wf-c'])
    await nextTick()
    const before = { ...c.renders }
    t.setIdentities([ident(1, 'one'), ident(2, 'two', { name: '改名', label: '改名' })])
    await nextTick()
    assert.deepEqual(
      ['wf-a', 'wf-b', 'wf-c'].map(wf => c.renders[wf] - before[wf]),
      [0, 1, 1],
    )
    c.stop()
  })

  it('新工作流第一次出现：只唤醒正在读它的那张卡（此前读到的是空）', async () => {
    const t = seeded()
    const c = cards(t, [...WFS, 'wf-new'])
    await nextTick()
    const before = { ...c.renders }
    t.replaceWorkflow('wf-new', [sess(500, 'wf-new', { state: 'working', base_state: 'working' })])
    await nextTick()
    for (const wf of [...WFS, 'wf-new']) assert.equal(c.renders[wf] - before[wf], wf === 'wf-new' ? 1 : 0, wf)
    c.stop()
  })
})
