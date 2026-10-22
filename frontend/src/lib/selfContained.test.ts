// 运行：pnpm --dir frontend run test:unit
// 页面自包含的防回退检查：应用外壳与样式表不得引用任何第三方站点（字体、样式表、脚本、预连接）。
// 起因：index.html 里曾有 Google Fonts 外链。它是渲染阻塞的样式表（实测约 345 KB、324 个 @font-face，其中 303 个属于
// 没有任何样式用到的 Noto Sans SC），访问 Google 不稳定的网络（国内、内网）会拖慢首屏；另一个 Noto Sans Mono 只在三处行内
// code 里作为兜底。外链已去掉，Noto Sans Mono 改为随应用自带的拉丁子集（同源，约 32 KB，OFL 许可）；
// 这里和服务端的 BuildCSP 测试一起防止外链再被引回来，并把自带字体的体积与许可证钉住。
import assert from 'node:assert/strict'
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { describe, it } from 'node:test'

function frontendDir(): string {
  for (const base of [process.cwd(), join(process.cwd(), 'frontend')]) {
    if (existsSync(join(base, 'index.html')) && existsSync(join(base, 'src'))) return base
  }
  throw new Error('找不到 frontend 目录，请在仓库根目录或 frontend 目录下运行测试')
}

function listFiles(dir: string, ext: string): string[] {
  if (!existsSync(dir)) return []
  const out: string[] = []
  for (const name of readdirSync(dir)) {
    const full = join(dir, name)
    if (statSync(full).isDirectory()) out.push(...listFiles(full, ext))
    else if (name.endsWith(ext)) out.push(full)
  }
  return out
}

// 先去掉 HTML / CSS 注释：注释里解释历史原因时可以提到站点名，不应触发误报
const stripComments = (s: string) => s.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '')

// 带主机名的绝对地址（含协议相对地址 //host）；本机地址不算第三方
const EXTERNAL = /(?:https?:)?\/\/(?!localhost\b|127\.0\.0\.1\b)[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}/i

describe('页面自包含', () => {
  it('index.html 的 link / script / img / source / iframe 不引用第三方站点，也没有 preconnect', () => {
    const html = stripComments(readFileSync(join(frontendDir(), 'index.html'), 'utf8'))
    const refs = [...html.matchAll(/<(?:link|script|img|source|iframe)\b[^>]*?\b(?:href|src)\s*=\s*["']([^"']+)["']/gi)].map(m => m[1])
    assert.ok(refs.length >= 3, `应能解析出 index.html 里的资源引用（图标、主题脚本、入口脚本），实际只有 ${refs.length} 条`)
    for (const ref of refs) {
      assert.ok(!EXTERNAL.test(ref), `index.html 引用了第三方资源：${ref}（字体、样式与脚本都应随前端构建产物一起提供）`)
    }
    assert.ok(!/rel\s*=\s*["'](?:preconnect|dns-prefetch)["']/i.test(html), 'index.html 不应预连接第三方站点')
  })

  it('样式表里没有 @import 或 url() 指向第三方站点', () => {
    const root = frontendDir()
    const files = [...listFiles(join(root, 'src'), '.css'), ...listFiles(join(root, 'public'), '.css')]
    assert.ok(files.length > 0, '应能找到样式表')
    for (const file of files) {
      const css = stripComments(readFileSync(file, 'utf8'))
      for (const m of css.matchAll(/(?:@import\s+(?:url\()?|url\()\s*["']?([^"')\s]+)/gi)) {
        if (m[1].startsWith('data:')) continue // 内联资源里的 xmlns 之类不是外部请求
        assert.ok(!EXTERNAL.test(m[1]), `${file} 引用了第三方资源：${m[1]}`)
      }
    }
  })

  it('@font-face 只引用随应用打包的本地文件（相对路径），体积受控，且随包提供字体许可证', () => {
    const MAX_FONT_BYTES = 80 * 1024 // 自带的是拉丁子集（约 32 KB）；超过这个量级多半是把整套字体（含 CJK）塞进来了
    const root = frontendDir()
    let blocks = 0
    for (const file of listFiles(join(root, 'src'), '.css')) {
      const css = stripComments(readFileSync(file, 'utf8'))
      for (const block of css.matchAll(/@font-face\s*\{[^}]*\}/gi)) {
        blocks++
        assert.ok(!EXTERNAL.test(block[0]), `${file} 的 @font-face 引用了第三方字体：${block[0].slice(0, 120)}`)
        const urls = [...block[0].matchAll(/url\(\s*["']?([^"')\s]+)/gi)].map(m => m[1])
        assert.ok(urls.length > 0, `${file} 的 @font-face 没有 src：${block[0].slice(0, 120)}`)
        for (const ref of urls) {
          assert.ok(/^\.\.?\//.test(ref), `${file} 的 @font-face 应引用相对路径的本地文件，而不是 ${ref}`)
          const target = join(dirname(file), ref)
          assert.ok(existsSync(target), `${file} 的 @font-face 引用的文件不存在：${ref}`)
          assert.ok(statSync(target).size <= MAX_FONT_BYTES, `${ref} 超过 ${MAX_FONT_BYTES / 1024} KB：应只带用得上的字符子集`)
        }
      }
    }
    assert.ok(blocks >= 1, '应有自带的 Noto Sans Mono 拉丁子集（@font-face）')
    // SIL OFL 要求随字体一起分发版权声明与许可证；public/ 下的文件会原样进入构建产物（/licenses/…）
    const license = join(root, 'public', 'licenses', 'NotoSansMono-OFL.txt')
    assert.ok(existsSync(license), '缺少 public/licenses/NotoSansMono-OFL.txt（自带字体的许可证）')
    assert.match(readFileSync(license, 'utf8'), /SIL OPEN FONT LICENSE Version 1\.1/)
  })
})
