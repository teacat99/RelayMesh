// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import { buildSessionHashIndex, capsuleRefToken, confirmRefToken, insertQuoteRef, replyHash, replyRefToken } from './replyRef'

describe('replyHash / replyRefToken', () => {
  it('取会话编号 sess- 之后的十六进制，统一成小写', () => {
    assert.equal(replyHash('sess-9f02517c'), '9f02517c')
    assert.equal(replyHash('sess-9F02517C'), '9f02517c')
    assert.equal(replyHash('  sess-9f02517c '), '9f02517c')
    assert.equal(replyRefToken('sess-9f02517c'), '【回复 9f02517c】')
  })

  it('编号不是 sess-<十六进制> 的形状时没有哈希，也就不出引用标记', () => {
    for (const id of ['', 'abc', 'sess-', 'sess-zz', 'sess-12345', 'sess-9f02517c-extra', 'wf-9f02517c', 'sess-1234567890abc']) {
      assert.equal(replyHash(id), '', `「${id}」不该有哈希`)
      assert.equal(replyRefToken(id), '')
    }
  })

  it('标记写法能被服务端的识别规则接受（回复 6~12 位十六进制，胶囊 r + 正整数）', () => {
    assert.match(replyRefToken('sess-9f02517c'), /^【回复 [0-9a-f]{6,12}】$/)
    assert.match(capsuleRefToken(8), /^【恢复 r\d{1,6}】$/)
  })
})

describe('capsuleRefToken', () => {
  it('修订号必须是正整数', () => {
    assert.equal(capsuleRefToken(8), '【恢复 r8】')
    assert.equal(capsuleRefToken(1), '【恢复 r1】')
    for (const bad of [0, -1, 1.5, Number.NaN]) assert.equal(capsuleRefToken(bad), '')
  })
})

describe('confirmRefToken（#132）', () => {
  it('Q5 / q5 / 5 / 数字 5 都写成「【确认 Q5】」', () => {
    for (const ref of ['Q5', 'q5', '5', ' Q5 ', 5]) assert.equal(confirmRefToken(ref), '【确认 Q5】')
    assert.equal(confirmRefToken('Q123456'), '【确认 Q123456】')
  })
  it('不是 1~6 位正整数时返回空串（界面据此不显示引用）', () => {
    for (const bad of ['', 'Q', 'Q0', '0', -1, 1.5, 'Q1234567', 'Qx', '确认', Number.NaN]) assert.equal(confirmRefToken(bad as string | number), '')
  })
  it('能插进输入框，和别的标记并存', () => {
    const first = insertQuoteRef('', 0, 0, confirmRefToken('Q5'))
    assert.equal(first.text, '【确认 Q5】 ')
    const second = insertQuoteRef(first.text, first.caret, first.caret, capsuleRefToken(8))
    assert.equal(second.text, '【确认 Q5】 【恢复 r8】 ')
  })
})

describe('insertQuoteRef', () => {
  const T = '【回复 9f02517c】'

  it('空输入框：插入标记并补一个空格，光标在空格之后', () => {
    const r = insertQuoteRef('', 0, 0, T)
    assert.equal(r.text, `${T} `)
    assert.equal(r.caret, r.text.length)
    assert.equal(r.inserted, true)
  })

  it('夹在文字中间：两侧各补一个空格，光标落在后面那个空格之后', () => {
    const r = insertQuoteRef('请看这里', 2, 2, T)
    assert.equal(r.text, `请看 ${T} 这里`)
    assert.equal(r.text.slice(r.caret), '这里')
  })

  it('两侧已经是空白时不再补空格，光标紧跟标记', () => {
    const r = insertQuoteRef('前  后', 2, 2, T)
    assert.equal(r.text, `前 ${T} 后`)
    assert.equal(r.text.slice(r.caret), ' 后')
    const nl = insertQuoteRef('第一行\n第二行', 4, 4, T)
    assert.equal(nl.text, `第一行\n${T} 第二行`)
  })

  it('有选区时替换选区', () => {
    const r = insertQuoteRef('aXXb', 1, 3, T)
    assert.equal(r.text, `a ${T} b`)
    const reversed = insertQuoteRef('aXXb', 3, 1, T)
    assert.equal(reversed.text, r.text)
  })

  it('光标位置超出范围时夹到文字两端', () => {
    assert.equal(insertQuoteRef('ab', 99, 99, T).text, `ab ${T} `)
    assert.equal(insertQuoteRef('ab', -5, -5, T).text, `${T} ab`)
  })

  it('同一个标记已经在文字里：不重复插入，光标挪到它后面', () => {
    const text = `先看${T} 再说`
    const r = insertQuoteRef(text, 0, 0, T)
    assert.equal(r.inserted, false)
    assert.equal(r.text, text)
    assert.equal(r.text.slice(r.caret), '再说')
    // 标记后面不是空格时，光标就停在标记之后
    const tight = insertQuoteRef(`${T}好`, 0, 0, T)
    assert.equal(tight.caret, T.length)
  })

  it('不同的标记各自插入', () => {
    const first = insertQuoteRef('', 0, 0, T)
    const second = insertQuoteRef(first.text, first.caret, first.caret, '【恢复 r8】')
    assert.equal(second.inserted, true)
    assert.equal(second.text, `${T} 【恢复 r8】 `)
  })
})

describe('buildSessionHashIndex（#186 C2a）', () => {
  it('按短哈希建索引：哈希统一小写，后出现的同哈希行覆盖前面的，没有哈希的行不进索引', () => {
    const rows = [
      { session_id: 'sess-9F02517C', n: 1 },
      { session_id: 'legacy-id', n: 2 },
      { session_id: 'sess-aaaaaa', n: 3 },
      { session_id: 'sess-9f02517c', n: 4 },
    ]
    const idx = buildSessionHashIndex(rows)
    assert.deepEqual([...idx.keys()].sort(), ['9f02517c', 'aaaaaa'])
    assert.equal(idx.get('9f02517c')?.n, 4)
    assert.equal(idx.get('aaaaaa')?.n, 3)
    assert.equal(buildSessionHashIndex([]).size, 0)
  })

  it('与逐行 replyHash 的写法结果一致（共用 store 之前每个实例的写法）', () => {
    const ids = ['sess-0a1b2c3d', 'sess-ffffff', 'x', '', 'sess-12345', 'sess-0a1b2c3d4e5f', 'SESS-ABCDEF01']
    const rows = ids.map((session_id, n) => ({ session_id, n }))
    const want = new Map<string, (typeof rows)[number]>()
    for (const r of rows) {
      const h = replyHash(r.session_id)
      if (h) want.set(h, r)
    }
    assert.deepEqual([...buildSessionHashIndex(rows)], [...want])
  })
})
