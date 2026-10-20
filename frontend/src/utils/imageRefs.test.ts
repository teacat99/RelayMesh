// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { imageRefToken, insertImageRef, listImageRefs, moveMapper, remapImageRefs, removalMapper } from './imageRefs'

describe('imageRefToken / listImageRefs', () => {
  it('标记写法与 MCP 回执里的「【图 N】」一致', () => {
    assert.equal(imageRefToken(1), '【图 1】')
    assert.equal(imageRefToken(12), '【图 12】')
  })

  it('按出现顺序列出所有标记，容忍空白差异，忽略普通文字', () => {
    assert.deepEqual(listImageRefs('看【图 1】和【图3】，还有【图  12 】；图 5 不算，【图】也不算'), [1, 3, 12])
    assert.deepEqual(listImageRefs(''), [])
  })
})

describe('insertImageRef', () => {
  it('在光标处插入，光标落在标记之后', () => {
    const r = insertImageRef('请看这里', 2, 2, 1)
    assert.equal(r.text, '请看【图 1】这里')
    assert.equal(r.caret, 2 + '【图 1】'.length)
    assert.equal(r.text.slice(r.caret), '这里')
  })

  it('有选区时替换选区', () => {
    const r = insertImageRef('abcdef', 1, 4, 2)
    assert.equal(r.text, 'a【图 2】ef')
  })

  it('选区端点颠倒、越界时被夹紧，不会抛错或丢字', () => {
    assert.equal(insertImageRef('abc', 3, 1, 1).text, 'a【图 1】')
    assert.equal(insertImageRef('abc', -5, 99, 1).text, '【图 1】')
    assert.equal(insertImageRef('', 0, 0, 3).text, '【图 3】')
  })

  it('连续插入多张图得到相邻的标记', () => {
    let text = ''
    let caret = 0
    for (const n of [1, 2, 3]) {
      const r = insertImageRef(text, caret, caret, n)
      text = r.text
      caret = r.caret
    }
    assert.equal(text, '【图 1】【图 2】【图 3】')
  })
})

describe('remapImageRefs', () => {
  it('恒等映射不改变任何文字', () => {
    const text = '第一段【图 1】\n第二段【图 2】，结尾'
    assert.equal(remapImageRefs(text, n => n), text)
  })

  it('只改标记本身，标记以外的文字（含类似写法的普通文字）原样保留', () => {
    const text = '图 2 不是标记，【图 2】才是；【图 3】也是'
    assert.equal(remapImageRefs(text, n => n + 10), '图 2 不是标记，【图 12】才是；【图 13】也是')
  })

  it('返回 null 的标记被删除；两侧各有空格时顺带吞掉一个，不留双空格', () => {
    assert.equal(remapImageRefs('看 【图 2】 这里', () => null), '看 这里')
    assert.equal(remapImageRefs('看【图 2】这里', () => null), '看这里')
    assert.equal(remapImageRefs('【图 2】', () => null), '')
    assert.equal(remapImageRefs('A 【图 2】', () => null), 'A ')
  })
})

describe('removalMapper', () => {
  it('删除第 2 张：指向它的标记删除，后面的整体前移，前面的不动', () => {
    const text = '【图 1】【图 2】【图 3】【图 4】'
    assert.equal(remapImageRefs(text, removalMapper(1)), '【图 1】【图 2】【图 3】')
  })

  it('删除最后一张 / 第一张', () => {
    assert.equal(remapImageRefs('【图 1】【图 2】', removalMapper(1)), '【图 1】')
    assert.equal(remapImageRefs('【图 1】【图 2】', removalMapper(0)), '【图 1】')
  })

  it('穷举：任意一张图被删除后，每个幸存标记都指向同一张图的新位置', () => {
    const labels = ['A', 'B', 'C', 'D', 'E']
    for (let removed = 0; removed < labels.length; removed++) {
      const after = labels.filter((_, i) => i !== removed)
      const mapper = removalMapper(removed)
      labels.forEach((label, oldIndex) => {
        const mapped = mapper(oldIndex + 1)
        if (oldIndex === removed) {
          assert.equal(mapped, null)
        } else {
          assert.equal(after[(mapped as number) - 1], label, `删除 ${removed} 后 ${label} 的标记应指向它的新位置`)
        }
      })
    }
  })
})

describe('moveMapper', () => {
  it('把第 1 张拖到第 3 个位置：A B C D → B C A D', () => {
    // 标记顺序：A=1 B=2 C=3 D=4，移动后 B=1 C=2 A=3 D=4
    assert.equal(remapImageRefs('【图 1】【图 2】【图 3】【图 4】', moveMapper(0, 2)), '【图 3】【图 1】【图 2】【图 4】')
  })

  it('把第 4 张拖到第 2 个位置：A B C D → A D B C', () => {
    assert.equal(remapImageRefs('【图 1】【图 2】【图 3】【图 4】', moveMapper(3, 1)), '【图 1】【图 3】【图 4】【图 2】')
  })

  it('原地不动是恒等映射', () => {
    assert.equal(remapImageRefs('【图 1】【图 2】', moveMapper(1, 1)), '【图 1】【图 2】')
  })

  it('穷举：任意 (from, to) 调序后，每个标记都跟着它的图片走，且映射是双射', () => {
    const labels = ['A', 'B', 'C', 'D', 'E']
    for (let from = 0; from < labels.length; from++) {
      for (let to = 0; to < labels.length; to++) {
        const after = labels.slice()
        const [item] = after.splice(from, 1)
        after.splice(to, 0, item)
        const mapper = moveMapper(from, to)
        const seen = new Set<number>()
        labels.forEach((label, oldIndex) => {
          const mapped = mapper(oldIndex + 1) as number
          assert.equal(after[mapped - 1], label, `(${from}→${to}) ${label} 的标记应跟着它走`)
          seen.add(mapped)
        })
        assert.equal(seen.size, labels.length)
      }
    }
  })

  it('超出当前图片数的手写标记不会被调序误伤', () => {
    assert.equal(remapImageRefs('【图 9】', moveMapper(0, 2)), '【图 9】')
  })
})
