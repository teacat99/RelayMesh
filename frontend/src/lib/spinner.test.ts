// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import {
  SPINNER_ANIMATION_NAME,
  SPINNER_CYCLE_MS,
  SPINNER_FRAMES,
  SPINNER_INTERVAL_MS,
  frameAt,
  frameIndexAt,
} from './spinner'
import { alignSpinnerPhase, type SpinnerAnimationLike, type SpinnerTargetLike } from './spinnerPhase'

/** 单测是编译到缓存目录里跑的，不能用 import.meta.url 找源码；从当前目录往 frontend/src 找。 */
function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'spinner.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')

describe('盲文旋转符的帧', () => {
  it('十帧，字形与顺序和需求一致', () => {
    assert.equal(SPINNER_FRAMES.join(' '), '⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏')
    assert.equal(SPINNER_FRAMES.length, 10)
    assert.equal(new Set(SPINNER_FRAMES).size, 10)
  })

  it('frameAt 循环取帧，负数与非数字不会越界', () => {
    assert.equal(frameAt(0), '⠋')
    assert.equal(frameAt(9), '⠏')
    assert.equal(frameAt(10), '⠋')
    assert.equal(frameAt(23), '⠸')
    assert.equal(frameAt(-1), '⠏')
    assert.equal(frameAt(-11), '⠏')
    assert.equal(frameAt(Number.NaN), '⠋')
    assert.equal(frameAt(2.9), '⠹')
  })

  it('每帧 80ms，一圈 800ms（= 帧数 × 每帧毫秒数）', () => {
    assert.equal(SPINNER_INTERVAL_MS, 80)
    assert.equal(SPINNER_CYCLE_MS, 800)
    assert.equal(SPINNER_CYCLE_MS, SPINNER_FRAMES.length * SPINNER_INTERVAL_MS)
  })

  it('frameIndexAt：时间线上的时刻 → 所有指示符此刻显示的帧', () => {
    assert.equal(frameIndexAt(0), 0)
    assert.equal(frameIndexAt(79.9), 0)
    assert.equal(frameIndexAt(80), 1)
    assert.equal(frameIndexAt(799.9), 9)
    assert.equal(frameIndexAt(800), 0, '一圈走完回到第一帧')
    assert.equal(frameIndexAt(1234), 5)
    // 页面开了几个小时：时间线的数字很大，取模后依然落在 0 ~ 9
    assert.equal(frameIndexAt(3_600_000 + 81), 1)
    assert.equal(frameIndexAt(86_400_000 * 3 + 799), 9)
    // 负数与非数字不会越界
    assert.equal(frameIndexAt(-1), 9)
    assert.equal(frameIndexAt(-800), 0)
    assert.equal(frameIndexAt(Number.NaN), 0)
    assert.equal(frameIndexAt(Number.POSITIVE_INFINITY), 0)
    // 与 frameAt 配合：第 i 帧的字形
    assert.equal(frameAt(frameIndexAt(240)), '⠸')
  })
})

describe('alignSpinnerPhase：动画起点对齐到时间线 0 点', () => {
  const anim = (name: string | undefined, startTime: unknown = 12345): SpinnerAnimationLike => ({ animationName: name, startTime })

  it('只改转圈动画的起点，别的动画不碰；返回改了几个', () => {
    const spin = anim(SPINNER_ANIMATION_NAME)
    const fade = anim('fade-in')
    const plain = anim(undefined) // 非 CSS 动画（没有 animationName）
    const target: SpinnerTargetLike = { getAnimations: () => [fade, spin, plain] }
    assert.equal(alignSpinnerPhase(target), 1)
    assert.equal(spin.startTime, 0)
    assert.equal(fade.startTime, 12345)
    assert.equal(plain.startTime, 12345)
  })

  it('同一个元素上有多个转圈动画时都对齐', () => {
    const a = anim(SPINNER_ANIMATION_NAME)
    const b = anim(SPINNER_ANIMATION_NAME)
    assert.equal(alignSpinnerPhase({ getAnimations: () => [a, b] }), 2)
    assert.deepEqual([a.startTime, b.startTime], [0, 0])
  })

  it('没有动画（例如系统要求减少动态效果）时什么也不做', () => {
    assert.equal(alignSpinnerPhase({ getAnimations: () => [] }), 0)
  })

  it('不支持 getAnimations 的环境、没有目标时不报错', () => {
    assert.equal(alignSpinnerPhase({}), 0)
    assert.equal(alignSpinnerPhase(null), 0)
    assert.equal(alignSpinnerPhase(undefined), 0)
  })
})

describe('盲文旋转符的 CSS 约定（BrailleSpinner.vue 与 spinner.ts 的常量一一对应）', () => {
  const vue = read('components/ui/spinner/BrailleSpinner.vue')
  // 样式块在文件末尾；从最后一个行首的 <style 开始取，脚本注释里提到 <style> 字样也不会取错
  const styleAt = vue.lastIndexOf('\n<style')
  const style = (/^<style[^>]*>([\s\S]*?)<\/style>/.exec(vue.slice(styleAt + 1))?.[1] ?? '').replace(/\/\*[\s\S]*?\*\//g, '')
  const script = (/<script[^>]*>([\s\S]*?)<\/script>/.exec(vue)?.[1] ?? '').replace(/\/\/.*$/gm, '')
  const template = /<template>([\s\S]*?)<\/template>/.exec(vue)?.[1] ?? ''
  const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

  /** 顶层规则 `selector { … }` 里的声明（不含嵌在 @media 里的同名规则）。 */
  function decls(selector: string): Record<string, string> {
    const m = new RegExp(`(?:^|\\})\\s*${escapeRe(selector)}\\s*\\{([^}]*)\\}`).exec(style)
    assert.ok(m, `找不到规则 ${selector}`)
    const out: Record<string, string> = {}
    for (const part of (m[1] ?? '').split(';')) {
      const i = part.indexOf(':')
      if (i > 0) out[part.slice(0, i).trim()] = part.slice(i + 1).trim()
    }
    return out
  }

  it('有三段样式可读', () => {
    assert.ok(style.includes('.braille-spinner'), '样式块读不到，守卫会全部失效')
    assert.ok(script.includes('alignSpinnerPhase'))
    assert.ok(template.includes('data-spinner'))
  })

  it('十行字形写在伪元素的 content 里，顺序与 SPINNER_FRAMES 一致，不进 DOM', () => {
    const d = decls('.braille-strip::before')
    assert.equal(d.content, `'${SPINNER_FRAMES.join('\\A')}'`)
    assert.equal(d['white-space'], 'pre', '\\A 要变成换行')
    assert.equal(d.display, 'block')
    // 模板里没有任何字形、没有插值：textContent 里不会混进这串符号，也没有逐帧改写的文字节点
    for (const g of SPINNER_FRAMES) assert.ok(!template.includes(g), `模板里不该直接写字形 ${g}`)
    assert.ok(!template.includes('{{'), '模板里不该有插值')
  })

  it('动画：一圈 800ms、十格阶梯、无限循环，且只在没有「减少动态效果」偏好时挂上', () => {
    const want = `${SPINNER_ANIMATION_NAME} ${SPINNER_CYCLE_MS}ms steps(${SPINNER_FRAMES.length}, end) infinite`
    const m = /@media \(prefers-reduced-motion: no-preference\)\s*\{\s*\.braille-strip\s*\{([^}]*)\}\s*\}/.exec(style)
    assert.ok(m, '动画必须放在 prefers-reduced-motion: no-preference 里（减少动态效果时停在第一帧）')
    assert.equal((m[1] ?? '').trim(), `animation: ${want};`)
    // 顶层的 .braille-strip 规则里不能直接挂动画（否则减少动态效果时也会转）
    assert.ok(!('animation' in decls('.braille-strip')))
  })

  it('关键帧只动 transform（合成器线程独自推进，主线程没有逐帧的样式重算 / 布局 / 绘制）', () => {
    const m = new RegExp(`@keyframes ${escapeRe(SPINNER_ANIMATION_NAME)}\\s*\\{\\s*to\\s*\\{([^}]*)\\}\\s*\\}`).exec(style)
    assert.ok(m, `找不到 @keyframes ${SPINNER_ANIMATION_NAME}（只有一个 to 帧）`)
    assert.equal((m[1] ?? '').trim(), 'transform: translateY(-100%);', '竖条整体上移 100%（= 十行），配合 steps(10) 每格一行')
  })

  it('盒子：边长 = 字号 = 行高，所以竖条每上移一行恰好换一个字形；超出的部分裁掉', () => {
    const d = decls('.braille-spinner')
    for (const k of ['width', 'height', 'font-size', 'line-height']) assert.equal(d[k], 'var(--bs-size)', k)
    assert.equal(d.overflow, 'hidden')
    assert.equal(d['flex-direction'], 'column', '竖条按自身高度（十行）排，不能被拉伸成盒子高度')
    assert.equal(d['flex-shrink'], '0')
    // 尺寸与旧版一致（16px 根字号下：md 14px = w-3.5，sm 12px = w-3）
    assert.equal(d['--bs-size'], '0.875rem')
    assert.equal(decls(".braille-spinner[data-size='sm']")['--bs-size'], '0.75rem')
    assert.equal(decls('.braille-strip').flex, 'none')
  })

  it('组件没有任何脚本驱动的动画：没有定时器、没有响应式帧号，只在动画开始时对齐一次相位', () => {
    assert.ok(!/setInterval|setTimeout|requestAnimationFrame|useSpinnerFrame|SpinnerClock/.test(script))
    assert.ok(!/\b(ref|computed|watch|reactive)\s*\(/.test(script), '不该有逐帧更新的响应式状态')
    assert.match(template, /@animationstart="onStart"/)
    assert.match(script, /alignSpinnerPhase\(e\.target as Element\)/)
    assert.match(template, /aria-hidden="true"/)
    assert.match(template, /data-spinner\b/)
    assert.match(template, /data-spinner-strip/)
  })
})

describe('旧的定时器方案已经彻底移除（#197）', () => {
  it('源码里没有任何地方还引用 SpinnerClock / useSpinnerFrame / SpinnerEnv', () => {
    const hits: string[] = []
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) {
          walk(p)
        } else if (/\.(ts|vue)$/.test(name) && name !== 'spinner.test.ts') {
          if (/SpinnerClock|useSpinnerFrame|SpinnerEnv/.test(readFileSync(p, 'utf8'))) hits.push(p.slice(srcDir().length + 1))
        }
      }
    }
    walk(srcDir())
    assert.deepEqual(hits, [])
    assert.ok(!existsSync(join(srcDir(), 'lib', 'useSpinner.ts')), 'useSpinner.ts 应已改名为 spinnerPhase.ts')
  })
})
