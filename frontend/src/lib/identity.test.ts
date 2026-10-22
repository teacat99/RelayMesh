import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import golden from './identityLabels.golden.json'
import {
  IDENTITY_NAME_MAX,
  baseLabel,
  computeLabels,
  directoryName,
  identityLabel,
  normalizeIdentityName,
  type LabelInput,
} from './identity'

interface GoldenCase {
  name: string
  items: LabelInput[]
  want: Record<string, { text: string; degraded: boolean }>
}

describe('computeLabels（与 Go 共用的对照表）', () => {
  // JSON 里各用例的 want 键不同（"1" / "7" / "9" …），推断出的联合类型与索引签名不兼容，先转 unknown
  const cases = golden.cases as unknown as GoldenCase[]
  it('对照表没有被截断', () => {
    assert.ok(cases.length >= 10)
  })
  for (const c of cases) {
    it(c.name, () => {
      const got = computeLabels(c.items)
      assert.equal(got.size, Object.keys(c.want).length)
      for (const item of c.items) {
        const want = c.want[String(item.id)]
        assert.ok(want, `对照表缺少 id ${item.id}`)
        assert.deepEqual(got.get(item.id), want)
      }
    })
  }
})

describe('directoryName', () => {
  const table: Array<[string, string]> = [
    ['/home/user/RelayMesh', 'RelayMesh'],
    ['/home/user/RelayMesh/', 'RelayMesh'],
    ['C:\\Users\\me\\code\\RelayMesh', 'RelayMesh'],
    ['C:\\Users\\me\\code\\RelayMesh\\', 'RelayMesh'],
    ['  /a/b  ', 'b'],
    ['relative', 'relative'],
    ['/', ''],
    ['', ''],
    ['   ', ''],
  ]
  for (const [input, want] of table) {
    it(JSON.stringify(input), () => assert.equal(directoryName(input), want))
  }
  it('undefined 当作空串', () => assert.equal(directoryName(undefined), ''))
})

describe('baseLabel / identityLabel', () => {
  it('改名 > 目录名 > 未知项目', () => {
    assert.equal(baseLabel({ id: 1, name: ' 后端组 ', project_directory: '/a/b' }), '后端组')
    assert.equal(baseLabel({ id: 1, project_directory: '/a/b' }), 'b')
    assert.equal(baseLabel({ id: 1 }), '未知项目')
  })

  it('identityLabel 在整批里比较重名', () => {
    const all: LabelInput[] = [
      { id: 1, host_name: 'h', project_directory: '/a/app' },
      { id: 2, host_name: 'h', project_directory: '/b/app' },
    ]
    assert.deepEqual(identityLabel(1, all), { text: 'h:/a/app', degraded: true })
    // 改名之后不再重名，另一个身份立刻恢复成目录名
    const renamed = all.map(x => (x.id === 2 ? { ...x, name: '后端' } : x))
    assert.deepEqual(identityLabel(1, renamed), { text: 'app', degraded: false })
    assert.deepEqual(identityLabel(2, renamed), { text: '后端', degraded: false })
  })

  it('不在这一批里的身份不会抛错', () => {
    assert.deepEqual(identityLabel(99, []), { text: '未知项目', degraded: false })
  })
})

describe('normalizeIdentityName', () => {
  it('去掉首尾空白与控制字符', () => {
    assert.equal(normalizeIdentityName('  前端\t助手\n '), '前端助手')
  })
  it('空串合法（表示恢复用目录名）', () => {
    assert.equal(normalizeIdentityName('   '), '')
  })
  it('按字符数（不是字节数）限长', () => {
    assert.equal(normalizeIdentityName('你'.repeat(IDENTITY_NAME_MAX))?.length, IDENTITY_NAME_MAX)
    assert.equal(normalizeIdentityName('你'.repeat(IDENTITY_NAME_MAX + 1)), null)
    // 代理对（emoji）按一个字符算，与 Go 的 []rune 计数一致
    assert.notEqual(normalizeIdentityName('😀'.repeat(IDENTITY_NAME_MAX)), null)
    assert.equal(normalizeIdentityName('😀'.repeat(IDENTITY_NAME_MAX + 1)), null)
  })
})
