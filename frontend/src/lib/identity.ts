// Agent 身份的显示名（设计稿 04 §1，需求 I-2）。
//
// 规则：显示名 = 改名 ?? 目录名；同一批身份里出现重名（不分大小写，改名也参与比较）时，
// 所有重名项都降级成「主机:完整路径」；降级后仍然撞车（同一主机、同一路径、不同凭据），再补上「 #编号」。
//
// 这是 Go 实现（internal/agentstate/label.go 的 Labels）的逐行镜像：
// 服务端在 GET /identities 里已经给出算好的 label，前端只有在「本地改动了身份」（比如刚改完名、还没重新取列表）
// 时才需要自己再算一遍，所以两边必须一模一样——src/lib/identityLabels.golden.json 是共用的对照表，
// Go 的 TestLabelsGolden 与 identity.test.ts 读同一个文件，谁改了规则谁就会被另一边的测试拦下。
// 全站只用这一处计算，避免状态卡、成员面板、群消息各算各的。

export interface LabelInput {
  id: number
  host_name?: string
  project_directory?: string
  /** 用户改的名字；空 = 用目录名 */
  name?: string
}

export interface IdentityLabel {
  text: string
  /** true = 因为重名降级成了「主机:完整路径」 */
  degraded: boolean
}

export const UNKNOWN_PROJECT = '未知项目'

/** 改名的长度上限（与服务端 store.AgentNameMaxRunes 一致，按字符数计）。 */
export const IDENTITY_NAME_MAX = 80

function trimSlashEnd(s: string): string {
  return s.replace(/[/\\]+$/, '')
}

/** 取路径的最后一段，Unix 与 Windows 风格的分隔符都认；取不到返回空串。 */
export function directoryName(dir: string | undefined): string {
  let d = trimSlashEnd((dir ?? '').trim())
  if (d === '') return ''
  const i = Math.max(d.lastIndexOf('/'), d.lastIndexOf('\\'))
  if (i >= 0) d = d.slice(i + 1)
  return d
}

/** 不考虑重名时的显示名：改名，否则目录名，再否则「未知项目」。 */
export function baseLabel(it: LabelInput): string {
  const n = (it.name ?? '').trim()
  if (n !== '') return n
  const d = directoryName(it.project_directory)
  if (d !== '') return d
  return UNKNOWN_PROJECT
}

function degradedLabel(it: LabelInput): string {
  const dir = trimSlashEnd((it.project_directory ?? '').trim())
  const host = (it.host_name ?? '').trim()
  if (host !== '' && dir !== '') return `${host}:${dir}`
  if (dir !== '') return dir
  if (host !== '') return `${host}:${baseLabel(it)}`
  return baseLabel(it)
}

/** 计算一批身份的显示名（同一批里才会比较重名，调用方传入同一个用户的全部身份）。 */
export function computeLabels(items: LabelInput[]): Map<number, IdentityLabel> {
  const out = new Map<number, IdentityLabel>()
  const groups = new Map<string, LabelInput[]>()
  for (const it of items) {
    const k = baseLabel(it).toLowerCase()
    const g = groups.get(k)
    if (g) g.push(it)
    else groups.set(k, [it])
  }
  for (const g of groups.values()) {
    if (g.length === 1) {
      out.set(g[0].id, { text: baseLabel(g[0]), degraded: false })
      continue
    }
    const seen = new Map<string, number>()
    for (const it of g) {
      const k = degradedLabel(it).toLowerCase()
      seen.set(k, (seen.get(k) ?? 0) + 1)
    }
    for (const it of g) {
      let text = degradedLabel(it)
      if ((seen.get(text.toLowerCase()) ?? 0) > 1) text = `${text} #${it.id}` // 同主机同路径的两个凭据：再补上编号才分得开
      out.set(it.id, { text, degraded: true })
    }
  }
  return out
}

/** 取某个身份在这一批里的显示名；这一批里没有它时按「单独一个」算。 */
export function identityLabel(id: number, items: LabelInput[]): IdentityLabel {
  const hit = computeLabels(items).get(id)
  if (hit) return hit
  return { text: UNKNOWN_PROJECT, degraded: false }
}

/** 改名输入的清洗：与服务端一致——去掉换行 / 制表符 / 控制字符并裁掉首尾空白；超长返回 null。 */
export function normalizeIdentityName(raw: string): string | null {
  // eslint-disable-next-line no-control-regex
  const cleaned = raw.replace(/[\u0000-\u001f]/g, '').trim()
  if ([...cleaned].length > IDENTITY_NAME_MAX) return null
  return cleaned
}
