import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { computed, effectScope, nextTick, ref, watchEffect, type Ref } from 'vue'
import { deepEqual, stableComputed } from './stable'

describe('deepEqual：JSON 形状的值逐项比较', () => {
  it('原始值按 Object.is：NaN 等于自己，0 与 -0 不同，null 与 undefined 不同', () => {
    assert.equal(deepEqual(1, 1), true)
    assert.equal(deepEqual('a', 'a'), true)
    assert.equal(deepEqual(NaN, NaN), true)
    assert.equal(deepEqual(0, -0), false)
    assert.equal(deepEqual(null, undefined), false)
    assert.equal(deepEqual(1, '1'), false)
  })

  it('数组：长度与每一项都要相同，顺序有关', () => {
    assert.equal(deepEqual([1, 2, 3], [1, 2, 3]), true)
    assert.equal(deepEqual([1, 2, 3], [3, 2, 1]), false)
    assert.equal(deepEqual([1, 2], [1, 2, 3]), false)
    assert.equal(deepEqual([], []), true)
  })

  it('对象：键的顺序不计，值递归比较；多一个键、少一个键、键值不同都不相等', () => {
    assert.equal(deepEqual({ a: 1, b: { c: [1, { d: 2 }] } }, { b: { c: [1, { d: 2 }] }, a: 1 }), true)
    assert.equal(deepEqual({ a: 1 }, { a: 1, b: undefined }), false)
    assert.equal(deepEqual({ a: undefined }, { b: undefined }), false)
    assert.equal(deepEqual({ a: 1 }, { a: 2 }), false)
    assert.equal(deepEqual({ a: { b: 1 } }, { a: { b: 2 } }), false)
  })

  it('数组与对象、null 与对象不会被混为一谈；类实例按引用比较', () => {
    assert.equal(deepEqual([], {}), false)
    assert.equal(deepEqual(null, {}), false)
    class P {
      x = 1
    }
    assert.equal(deepEqual(new P(), new P()), false)
    const same = new P()
    assert.equal(deepEqual(same, same), true)
    assert.equal(deepEqual(Object.create(null), {}), false)
  })
})

describe('stableComputed：没变就不唤醒依赖它的渲染', () => {
  /** 一个依赖 c 的「渲染」：返回它跑了几次。 */
  async function renders(make: (tick: Ref<number>) => { value: unknown }, steps: number[]) {
    const tick = ref(0)
    const c = make(tick)
    let runs = 0
    const scope = effectScope()
    scope.run(() =>
      watchEffect(() => {
        void c.value
        runs++
      }),
    )
    await nextTick()
    const first = runs
    for (const s of steps) {
      tick.value = s
      await nextTick()
    }
    scope.stop()
    return { first, total: runs }
  }

  it('对照：普通 computed 每次重算出新对象，依赖它的渲染每一秒都会重跑', async () => {
    const r = await renders(t => computed(() => ({ label: (t.value ?? 0) >= 100 ? '晚' : '早', parts: [1, 2] })), [1, 2, 3, 4, 5])
    assert.equal(r.first, 1)
    assert.equal(r.total, 6)
  })

  it('stableComputed：内容没变就返回旧对象，渲染一次都不重跑；真的变了才跑', async () => {
    const r = await renders(t => stableComputed(() => ({ label: (t.value ?? 0) >= 100 ? '晚' : '早', parts: [1, 2] })), [1, 2, 3, 4, 5])
    assert.equal(r.first, 1)
    assert.equal(r.total, 1, '时钟走了五下、显示没变，不该重画')
    const r2 = await renders(t => stableComputed(() => ({ label: (t.value ?? 0) >= 100 ? '晚' : '早', parts: [1, 2] })), [1, 2, 100, 101])
    assert.equal(r2.total, 2, '只有越过 100 那一下改变了显示，只重画一次')
  })

  it('没变时拿到的就是上一次的那个对象（身份不变）', async () => {
    const tick = ref(0)
    const c = stableComputed(() => ({ n: Math.floor(tick.value / 10) }))
    const a = c.value
    tick.value = 3
    assert.equal(c.value, a)
    tick.value = 12
    assert.notEqual(c.value, a)
    assert.deepEqual(c.value, { n: 1 })
  })

  it('原始值与 null 也可用：null 之间互相相同；从 null 变成对象是变化', async () => {
    const tick = ref(0)
    const c = stableComputed<{ k: number } | null>(() => (tick.value < 5 ? null : { k: 1 }))
    assert.equal(c.value, null)
    tick.value = 1
    assert.equal(c.value, null)
    tick.value = 6
    assert.deepEqual(c.value, { k: 1 })
  })

  it('可以传自己的比较函数（只比关心的字段）', async () => {
    const tick = ref(0)
    const c = stableComputed(
      () => ({ key: tick.value < 3 ? 'a' : 'b', noise: tick.value }),
      (x, y) => x.key === y.key,
    )
    const a = c.value
    tick.value = 2
    assert.equal(c.value, a, 'noise 变了但 key 没变：沿用旧对象')
    tick.value = 4
    assert.equal(c.value.key, 'b')
  })
})
