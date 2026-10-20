// 运行：pnpm --dir frontend run test:unit
// 手机会话页头的源码级防线（#168）：页头右侧只剩 终端 / 任务 / ⋯ 三个同样大小（36×36）的按钮，图标统一 16px，
// 「项目」收进「⋯」菜单，任务的数字改成右上角的角标；PC 页头不变（项目、工作表仍是带文字的独立按钮）。
// 行为（真实的宽度、图标尺寸、菜单点击）由端到端套件 scripts/e2e-terminal/e2e/header-nav.mjs 的 A7 步骤量；
// 这里钉住「靠它们成立」的写法，免得有人整理样式时顺手改回去、而 e2e 又没跑到：
//   1. 四个页头按钮（终端 / 任务 / 项目 / 工作表）都用同一个盒子常量 HEADER_BTN：手机是 size-9 的方块（不收缩），PC 是原来的小按钮；
//      图标都用 HEADER_ICON（手机 16px、PC 14px），文字标签在 md 以上才显示（切换点与 isPhone / max-md 同一条线 768px）；
//   2. 「项目」按钮与「工作表」按钮只在 PC 渲染（!isPhone），手机上它们在「⋯」菜单里；
//   3. 任务的数字角标在手机上是绝对定位的右上角角标（按钮只剩图标，数字不占宽度）；
//   4. 「⋯」菜单：触发器是同一个 size-9 方块、带 data-project-id；菜单最上面是「项目」条目（带文档篇数角标），点它发 open-project。
import assert from 'node:assert/strict'
import { existsSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'sessionHeaderPhone.test.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}
const read = (rel: string) => readFileSync(join(srcDir(), rel), 'utf8')
const squash = (s: string) => s.replace(/\s+/g, ' ').trim()

const HEADER = read('components/feedback/SessionHeader.vue')
const MORE = read('components/feedback/SessionHeaderMore.vue')
const tplOf = (src: string) => src.slice(src.indexOf('\n<template>'))

/** 模板里带某个 data-testid 的 <button …> 开标签（属性压成一行）。 */
function buttonWith(testid: string): string {
  const tpl = tplOf(HEADER)
  const at = tpl.indexOf(`data-testid="${testid}"`)
  assert.ok(at > 0, `模板里找不到 ${testid}`)
  const start = tpl.lastIndexOf('<button', at)
  const end = tpl.indexOf('>', at)
  return squash(tpl.slice(start, end + 1))
}

/** 脚本里 const NAME = '…'（单引号字符串，可能换行）。 */
function constOf(name: string): string {
  const m = HEADER.match(new RegExp(`const ${name} =\\s*'([^']*)'`))
  assert.ok(m, `找不到常量 ${name}`)
  return m[1]
}

describe('手机会话页头：四个按钮共用同一个盒子', () => {
  const box = constOf('HEADER_BTN')
  const icon = constOf('HEADER_ICON')

  it('HEADER_BTN：手机是 36×36 的方块（居中、没有内边距与间隙），不收缩，贴角标要 relative；PC 保持原来的小按钮', () => {
    for (const cls of ['relative', 'shrink-0', 'max-md:size-9', 'max-md:justify-center', 'max-md:gap-0', 'max-md:p-0', 'px-2.5', 'py-1']) {
      assert.ok(box.split(/\s+/).includes(cls), `HEADER_BTN 缺 ${cls}`)
    }
    // 旧写法（手机上只收窄内边距，按钮宽度跟着文字 / 图标走）不能回来
    assert.ok(!/max-md:px-2\b/.test(box) && !/max-md:py-1\.5\b/.test(box), '手机内边距的旧写法不该留着')
  })

  it('HEADER_ICON：手机 16px、PC 14px（md 以上），与「⋯」的图标同大', () => {
    const cls = icon.split(/\s+/)
    assert.ok(cls.includes('size-4') && cls.includes('md:size-3.5') && cls.includes('text-primary'), icon)
    assert.match(MORE, /<MoreHorizontal class="size-4 text-primary" \/>/)
  })

  it('终端 / 任务 / 项目 / 工作表四个按钮都 :class="HEADER_BTN"，不再各写一份 class', () => {
    for (const testid of ['session-terminal-launcher', 'session-work-entry', 'session-project-entry', 'session-sheet-entry']) {
      const tag = buttonWith(testid)
      assert.match(tag, /:class="HEADER_BTN"/, `${testid} 应使用 HEADER_BTN`)
      assert.doesNotMatch(tag, /\sclass="/, `${testid} 不该再有手写的 class`)
    }
  })

  it('四个图标都用 HEADER_ICON；文字标签在 md 以上才显示（不再是 sm），否则 640~767px 的手机上文字会撑出 36px 的方块', () => {
    const tpl = squash(tplOf(HEADER))
    for (const name of ['SquareTerminal', 'ListChecks', 'FolderGit2', 'FileText']) {
      assert.ok(tpl.includes(`<${name} :class="HEADER_ICON" />`), `${name} 应使用 HEADER_ICON`)
    }
    for (const label of ['终端', '任务', '项目', '工作表']) {
      assert.ok(tpl.includes(`<span class="hidden md:inline font-medium">${label}</span>`), `「${label}」标签应是 hidden md:inline`)
    }
    assert.doesNotMatch(tpl, /hidden sm:inline font-medium/, '页头按钮的文字标签不该再用 sm 断点')
  })
})

describe('手机会话页头：项目与工作表只在 PC 渲染，手机在「⋯」菜单里', () => {
  it('「项目」按钮 v-if 带 !isPhone；「工作表」按钮同样', () => {
    assert.match(buttonWith('session-project-entry'), /v-if="sessionWorkflowId && !isPhone"/)
    assert.match(buttonWith('session-sheet-entry'), /v-if="!isPhone &&/)
  })

  it('终端与任务按钮手机也在（页头只剩 终端 / 任务 / ⋯）', () => {
    assert.doesNotMatch(buttonWith('session-terminal-launcher'), /isPhone/)
    assert.doesNotMatch(buttonWith('session-work-entry'), /isPhone/)
  })

  it('SessionHeaderMore 在手机渲染，并收到项目编号、悬停说明、文档篇数与 open-project / open-sheet 两个事件', () => {
    const tpl = tplOf(HEADER)
    const m = tpl.match(/<SessionHeaderMore\b([^>]*)>/)
    assert.ok(m, '模板里没有 SessionHeaderMore')
    const attrs = squash(m[1])
    for (const a of [
      'v-if="isPhone && sessionWorkflowId"',
      ':workflow-id="sessionWorkflowId"',
      ':project-id="ownerId"',
      ':project-title="projectEntryTitle"',
      ':docs-count="docsBadge"',
      '@open-project="openProject"',
      '@open-sheet="isSheetDrawerOpen = true"',
    ]) assert.ok(attrs.includes(a), `SessionHeaderMore 缺 ${a}`)
  })
})

describe('手机会话页头：任务数字是右上角的角标', () => {
  it('角标在手机上 absolute 贴在按钮右上角，带底色与描边环（盖住按钮边框）；PC 仍在按钮里排成一行', () => {
    const tpl = squash(tplOf(HEADER))
    const m = tpl.match(/<span v-if="workBadge" class="([^"]*)"/)
    assert.ok(m, '找不到任务角标')
    const cls = m[1].split(/\s+/)
    for (const c of ['max-md:absolute', 'max-md:-right-1.5', 'max-md:-top-1.5', 'max-md:bg-background', 'max-md:ring-2', 'max-md:ring-background']) {
      assert.ok(cls.includes(c), `任务角标缺 ${c}`)
    }
    assert.ok(!cls.includes('absolute'), '角标只在手机上绝对定位，PC 上不能无条件 absolute')
  })
})

describe('「⋯」菜单：项目收在最上面', () => {
  const tpl = squash(tplOf(MORE))

  it('触发器是 36×36 的方块（size-9，没有 min-w / 内边距），带 data-project-id（项目认出来后的就绪信号）', () => {
    const m = tpl.match(/<button type="button" class="([^"]*)"[^>]*data-testid="session-more-menu"/)
    assert.ok(m, '找不到「⋯」触发器')
    const cls = m[1].split(/\s+/)
    assert.ok(cls.includes('size-9') && cls.includes('shrink-0') && cls.includes('relative'), m[1])
    assert.ok(!cls.some(c => /^min-w-/.test(c) || /^p[xy]-/.test(c)), '方块里不该再有 min-w / 内边距')
    assert.match(tpl, /:data-project-id="props\.projectId \|\| undefined"/)
  })

  it('props 与事件：projectId / projectTitle / docsCount；open-project 与 open-sheet', () => {
    assert.match(MORE, /projectId\?: number/)
    assert.match(MORE, /projectTitle\?: string/)
    assert.match(MORE, /docsCount\?: number/)
    assert.match(MORE, /\(e: 'open-sheet'\): void; \(e: 'open-project'\): void/)
  })

  it('「项目」条目在提醒策略与「工作表」之前，点它发 open-project，文档篇数角标沿用 session-project-badge', () => {
    const project = tpl.indexOf('data-testid="session-project-entry"')
    const notify = tpl.indexOf('<ConversationNotifyItems')
    const sheet = tpl.indexOf('data-testid="session-sheet-entry"')
    assert.ok(project > 0 && project < notify && notify < sheet, '顺序应是 项目 → 提醒 → 工作表')
    assert.match(tpl, /data-testid="session-project-entry"[^>]*@select="emit\('open-project'\)"/)
    assert.match(tpl, /v-if="props\.docsCount"[^>]*data-testid="session-project-badge"/)
  })
})
