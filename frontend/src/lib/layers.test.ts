// 运行：pnpm --dir frontend run test:unit
// 层级表的约束 + 源码层面的防回退检查：
// 「验证身份」弹窗被「接入主机」弹窗盖住（密码框点不到）就是因为两者层级相同、先后取决于 DOM 挂载顺序；
// 同样的问题在「确认删除主机 → 再要求输密码」上又出现过一次：确认框与验证框同为 top 层，确认框的遮罩盖住了验证框，
// 所以验证框单独占一档（verify），高于确认框。
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it } from 'node:test'
import { LAYER, LAYER_CLASS, dialogLayerClass, type LayerName } from './layers'

function srcDir(): string {
  for (const base of [join(process.cwd(), 'src'), join(process.cwd(), 'frontend', 'src')]) {
    if (existsSync(join(base, 'lib', 'layers.ts'))) return base
  }
  throw new Error('找不到 frontend/src，请在仓库根目录或 frontend 目录下运行测试')
}

function read(rel: string): string {
  return readFileSync(join(srcDir(), rel), 'utf8')
}

function listVue(dir: string): string[] {
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) out.push(...listVue(full))
    else if (name.endsWith('.vue')) out.push(full)
  }
  return out
}

describe('层级表', () => {
  it('数值严格递增：抽屉 < 弹窗 < 确认框 < 验证框 < 气泡 < 掉线登录 < 图片预览', () => {
    const order: LayerName[] = ['sheet', 'dialog', 'dialogTop', 'dialogVerify', 'popper', 'session', 'lightbox']
    for (let i = 1; i < order.length; i++) {
      assert.ok(LAYER[order[i - 1]] < LAYER[order[i]], `${order[i - 1]} 应低于 ${order[i]}`)
    }
  })

  it('类名与数值一致（Tailwind 需要字面量，这里防止改了一边忘了另一边）', () => {
    for (const name of Object.keys(LAYER) as LayerName[]) {
      const m = /^z-(?:\[(\d+)\]|(\d+))$/.exec(LAYER_CLASS[name])
      assert.ok(m, `${name} 的类名格式不对：${LAYER_CLASS[name]}`)
      assert.equal(Number(m[1] ?? m[2]), LAYER[name], name)
    }
  })

  it('弹窗默认走普通层，top 走叠加层，verify 走验证层', () => {
    assert.equal(dialogLayerClass(undefined), LAYER_CLASS.dialog)
    assert.equal(dialogLayerClass('base'), LAYER_CLASS.dialog)
    assert.equal(dialogLayerClass('top'), LAYER_CLASS.dialogTop)
    assert.equal(dialogLayerClass('verify'), LAYER_CLASS.dialogVerify)
  })
})

describe('源码约束', () => {
  it('气泡类原语全部取 popper 层，且不再手写 z-index', () => {
    for (const rel of [
      'components/ui/tooltip/TooltipContent.vue',
      'components/ui/popover/PopoverContent.vue',
      'components/ui/dropdown-menu/DropdownMenuContent.vue',
      'components/ui/select/SelectContent.vue',
    ]) {
      const text = read(rel)
      assert.match(text, /LAYER_CLASS\.popper/, `${rel} 应使用 LAYER_CLASS.popper`)
      assert.doesNotMatch(text, /['\s]z-(\[\d+\]|\d+)\b/, `${rel} 不应手写 z-index`)
    }
  })

  it('弹窗与抽屉原语取层级表', () => {
    assert.match(read('components/ui/dialog/DialogContent.vue'), /dialogLayerClass/)
    assert.doesNotMatch(read('components/ui/dialog/DialogContent.vue'), /\bz-\[\d+\]/)
    assert.match(read('components/ui/sheet/SheetContent.vue'), /LAYER_CLASS\.sheet/)
  })

  it('确认框使用 top 层；验证身份框使用比它更高的 verify 层（确认之后才弹出验证，不能被确认框的遮罩盖住）', () => {
    assert.match(read('components/terminal/ConfirmDialog.vue'), /<DialogContent[^>]*\blayer="top"/)
    assert.match(read('components/terminal/UnlockDialog.vue'), /<DialogContent[^>]*\blayer="verify"/)
    assert.ok(LAYER.dialogVerify > LAYER.dialogTop)
  })

  it('掉线登录框使用 session 层', () => {
    assert.match(read('components/auth/LoginDialog.vue'), /LAYER_CLASS\.session/)
  })

  it('没有任何地方给气泡类内容再手写 z-index 覆盖（否则会被压回弹窗下面）', () => {
    const bad: string[] = []
    const re = /<(DropdownMenuContent|PopoverContent|TooltipContent|SelectContent)\b[^>]*\bclass="[^"]*\bz-(\[\d+\]|\d+)\b/
    for (const file of listVue(srcDir())) {
      if (re.test(readFileSync(file, 'utf8'))) bad.push(file.slice(srcDir().length + 1))
    }
    assert.deepEqual(bad, [])
  })
})
