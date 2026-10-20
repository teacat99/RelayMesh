// 运行：pnpm --dir frontend run test:unit
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { SessionImage } from '../api/types'
import {
  hasDraftRefs,
  isDraftImageMissing,
  sendWithDraftImages,
  toInlineImages,
  toRefImages,
  type WireImages,
} from './draftImageWire'

function image(name: string, extra: Partial<SessionImage> = {}): SessionImage {
  return { name, format: 'png', data_type: 'base64', hash: 'h-' + name, width: 10, height: 20, data: name.toUpperCase().repeat(6), key: 'k-' + name, ...extra }
}

function refImage(name: string, wf = 'wf-1'): SessionImage {
  return image(name, { synced: true, draft_key: 'k-' + name, draft_workflow_id: wf })
}

function missingError(): Error {
  return Object.assign(new Error('draft_image_missing'), {
    response: { status: 400, data: { code: 'draft_image_missing', keys: ['k-a'] } },
  })
}

describe('toRefImages', () => {
  it('带 draft_key 的图片只留元数据与引用（没有正文），其余图片原样；工作流提到顶层', () => {
    const wire = toRefImages([refImage('a'), image('b'), refImage('c')])
    assert.equal(wire.draftWorkflowId, 'wf-1')
    const [a, b, c] = wire.images
    assert.equal(a.draft_key, 'k-a')
    assert.equal('data' in a, false)
    assert.equal(a.name, 'a')
    assert.equal(a.hash, 'h-a')
    assert.equal(a.width, 10)
    assert.equal(b.data, 'BBBBBB')
    assert.equal('draft_key' in b, false)
    assert.equal(c.draft_key, 'k-c')
    for (const img of wire.images) {
      assert.equal('draft_workflow_id' in img, false, '工作流只在请求顶层，不在每张图里')
      assert.equal('synced' in img, false)
      assert.equal('lost' in img, false)
    }
  })

  it('没有引用型图片：不带 draftWorkflowId；输入为空也安全', () => {
    assert.deepEqual(toRefImages([image('a')]).draftWorkflowId, undefined)
    assert.deepEqual(toRefImages(undefined), { images: [] })
    assert.deepEqual(toRefImages([]), { images: [] })
  })

  it('不修改传入的图片（失败重试还要用它们的正文）', () => {
    const a = refImage('a')
    const input = [a]
    toRefImages(input)
    assert.equal(a.data, 'AAAAAA')
    assert.equal(a.draft_key, 'k-a')
    assert.equal(a.synced, true)
    assert.equal(input.length, 1)
  })
})

describe('toInlineImages', () => {
  it('所有图片都带自己的正文，去掉内存字段与传输字段', () => {
    const wire = toInlineImages([refImage('a'), image('b', { lost: true })])
    assert.deepEqual(wire.draftWorkflowId, undefined)
    assert.equal(wire.images[0].data, 'AAAAAA')
    assert.equal('draft_key' in wire.images[0], false)
    assert.equal('synced' in wire.images[0], false)
    assert.equal('lost' in wire.images[1], false)
    assert.equal(wire.images[0].name, 'a')
  })

  it('hasDraftRefs', () => {
    assert.equal(hasDraftRefs([image('a')]), false)
    assert.equal(hasDraftRefs([image('a'), refImage('b')]), true)
    assert.equal(hasDraftRefs(undefined), false)
  })
})

describe('isDraftImageMissing', () => {
  it('只认「HTTP 400 + code=draft_image_missing」', () => {
    assert.equal(isDraftImageMissing(missingError()), true)
    assert.equal(isDraftImageMissing({ response: { status: 400, data: { code: 'other' } } }), false)
    assert.equal(isDraftImageMissing({ response: { status: 500, data: { code: 'draft_image_missing' } } }), false)
    assert.equal(isDraftImageMissing(new Error('network')), false)
    assert.equal(isDraftImageMissing(null), false)
    assert.equal(isDraftImageMissing(undefined), false)
  })
})

describe('sendWithDraftImages', () => {
  it('没有引用型图片：发一次，内联格式', async () => {
    const sent: WireImages[] = []
    const out = await sendWithDraftImages([image('a')], async wire => {
      sent.push(wire)
      return 'ok'
    })
    assert.equal(out, 'ok')
    assert.equal(sent.length, 1)
    assert.equal(sent[0].images[0].data, 'AAAAAA')
  })

  it('有引用型图片：先发引用格式（请求里没有正文）', async () => {
    const sent: WireImages[] = []
    await sendWithDraftImages([refImage('a')], async wire => {
      sent.push(wire)
      return 1
    })
    assert.equal(sent.length, 1)
    assert.equal(sent[0].images[0].draft_key, 'k-a')
    assert.equal('data' in sent[0].images[0], false)
    assert.equal(sent[0].draftWorkflowId, 'wf-1')
  })

  it('服务端说引用的图片不在了：用内存里的正文改发内联格式再试一次，用户无感', async () => {
    const sent: WireImages[] = []
    const out = await sendWithDraftImages([refImage('a'), image('b')], async wire => {
      sent.push(wire)
      if (sent.length === 1) throw missingError()
      return 'second'
    })
    assert.equal(out, 'second')
    assert.equal(sent.length, 2)
    assert.equal(sent[1].images[0].data, 'AAAAAA', '重发时带上正文')
    assert.equal('draft_key' in sent[1].images[0], false)
    assert.equal(sent[1].draftWorkflowId, undefined)
  })

  it('引用的图片不在、而这张图又没有内存正文：无法重发，原样抛出（不静默丢图）', async () => {
    const bare = refImage('a')
    delete bare.data
    let calls = 0
    await assert.rejects(
      sendWithDraftImages([bare], async () => {
        calls++
        throw missingError()
      }),
      /draft_image_missing/,
    )
    assert.equal(calls, 1)
  })

  it('其它错误不重试，直接抛出', async () => {
    let calls = 0
    await assert.rejects(
      sendWithDraftImages([refImage('a')], async () => {
        calls++
        throw new Error('network down')
      }),
      /network down/,
    )
    assert.equal(calls, 1)
  })

  it('重发后仍然失败：把第二次的错误抛给调用方，最多只发两次', async () => {
    let calls = 0
    await assert.rejects(
      sendWithDraftImages([refImage('a')], async () => {
        calls++
        throw calls === 1 ? missingError() : new Error('second failure')
      }),
      /second failure/,
    )
    assert.equal(calls, 2)
  })
})
