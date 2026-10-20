// 运行：pnpm --dir frontend run test:unit
// 会话 / 追加里的图片小工具（#120 · D-273 阶段 C，预览视图用）：稳定 key、骨架占位比例、可用地址、「已加载」记录。
import assert from 'node:assert/strict'
import { describe, it } from 'node:test'
import type { SessionImage } from '../api/types'
import { getImageKey, getImageStyle, getImageUrl, isImageLoaded, markImageLoaded } from './sessionImages'

const img = (over: Partial<SessionImage>): SessionImage => ({ ...over }) as SessionImage

describe('图片：稳定 key', () => {
  it('优先内容哈希，其次直链，最后退回序号', () => {
    assert.equal(getImageKey(img({ hash: 'h1', url: '/a.png' }), 3, 's1'), 's1_h1')
    assert.equal(getImageKey(img({ url: '/a.png' }), 3, 's1'), 's1_/a.png')
    assert.equal(getImageKey(img({}), 3, 's1'), 's1_3')
  })

  it('没有图片对象时也有 key；前缀把不同来源隔开', () => {
    assert.equal(getImageKey(null, 0, 'inbox-9'), 'inbox-9_0')
    assert.equal(getImageKey(undefined, 2, 'inbox-9'), 'inbox-9_2')
    assert.notEqual(getImageKey(img({ hash: 'h' }), 0, 'a'), getImageKey(img({ hash: 'h' }), 0, 'b'))
  })
})

describe('图片：骨架占位比例', () => {
  it('图片自带宽高就按它（加载前后高度不变）', () => {
    assert.deepEqual(getImageStyle(img({ width: 400, height: 300 })), { aspectRatio: '400 / 300' })
    assert.deepEqual(getImageStyle(img({ width: 300, height: 900 })), { aspectRatio: '300 / 900' })
  })

  it('宽高缺失、为 0 或没有图片时用 16 / 10', () => {
    for (const x of [img({}), img({ width: 0, height: 100 }), img({ width: 100, height: 0 }), img({ width: -1, height: 5 }), null, undefined]) {
      assert.deepEqual(getImageStyle(x), { aspectRatio: '16 / 10' })
    }
  })
})

describe('图片：可用地址', () => {
  it('直链优先于内联数据', () => {
    assert.equal(getImageUrl(img({ url: '/api/v1/img/1', data: 'AAAA' })), '/api/v1/img/1')
  })

  it('内联数据：data: 与 http(s) 原样；裸 base64 补上 data URL（默认 png，可指定格式）', () => {
    assert.equal(getImageUrl(img({ data: 'data:image/jpeg;base64,QQ==' })), 'data:image/jpeg;base64,QQ==')
    assert.equal(getImageUrl(img({ data: 'https://x.test/a.png' })), 'https://x.test/a.png')
    assert.equal(getImageUrl(img({ data: 'http://x.test/a.png' })), 'http://x.test/a.png')
    assert.equal(getImageUrl(img({ data: 'QUJD' })), 'data:image/png;base64,QUJD')
    assert.equal(getImageUrl(img({ data: 'QUJD', format: 'webp' })), 'data:image/webp;base64,QUJD')
  })

  it('什么都没有时是空串', () => {
    assert.equal(getImageUrl(img({})), '')
    assert.equal(getImageUrl(null), '')
    assert.equal(getImageUrl(undefined), '')
  })
})

describe('图片：已加载记录（模块级，跨组件实例共享）', () => {
  it('标记之后才算已加载；不同 key 互不影响', () => {
    const key = `test_${Math.random().toString(36).slice(2)}`
    assert.equal(isImageLoaded(key), false)
    markImageLoaded(key)
    assert.equal(isImageLoaded(key), true)
    assert.equal(isImageLoaded(`${key}-other`), false)
  })
})
