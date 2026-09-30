/**
 * 仅开发模式生效的性能度量探针（D-171 / P0）。
 *
 * 约束：所有调用点必须用 `if (import.meta.env.DEV)` 包裹，生产构建会把整段分支连同本模块一并树摇移除，
 * 因此不会给发布产物增加任何体积或运行时开销。
 *
 * 用法（浏览器控制台或自动化脚本）：
 *   window.__rmPerf.reset()          // 清空采样
 *   ...执行待测操作...
 *   window.__rmPerf.snapshot()       // 汇总：请求数/字节/分位耗时、命名测量、长任务、DOM 变更量、JS 堆
 */

export interface PerfRequest {
  method: string
  url: string
  status: number
  ms: number
  bytes: number
  startedAt: number
}

export interface PerfMeasure {
  name: string
  ms: number
  at: number
}

const marks = new Map<string, number>()
const requests: PerfRequest[] = []
const measures: PerfMeasure[] = []
const longTasks: { ms: number; at: number }[] = []
const dom = { mutations: 0, addedNodes: 0, removedNodes: 0 }

export function perfMark(name: string): void {
  marks.set(name, performance.now())
}

/** 计算自 startMark 起的耗时并记录为命名测量，返回毫秒数（起点不存在则返回 null）。 */
export function perfMeasure(name: string, startMark: string): number | null {
  const start = marks.get(startMark)
  if (start === undefined) return null
  const ms = performance.now() - start
  measures.push({ name, ms, at: performance.now() })
  return ms
}

export function perfRecordRequest(record: PerfRequest): void {
  requests.push(record)
}

function normalizeRoute(method: string, url: string): string {
  const path = url.split('?')[0]
  const normalized = path
    .replace(/\/(sess|wf)-[^/]+/g, '/:id')
    .replace(/\/queued\/\d+/g, '/queued/:n')
    .replace(/\/images\/\d+/g, '/images/:n')
  return `${method} ${normalized}`
}

function percentile(sortedAsc: number[], p: number): number {
  if (sortedAsc.length === 0) return 0
  const idx = Math.min(sortedAsc.length - 1, Math.ceil((p / 100) * sortedAsc.length) - 1)
  return sortedAsc[Math.max(0, idx)]
}

function round1(n: number): number {
  return Math.round(n * 10) / 10
}

function snapshot() {
  const byRoute: Record<string, { count: number; bytes: number; p50: number; p95: number }> = {}
  const grouped = new Map<string, PerfRequest[]>()
  for (const r of requests) {
    const key = normalizeRoute(r.method, r.url)
    const list = grouped.get(key)
    if (list) list.push(r)
    else grouped.set(key, [r])
  }
  for (const [key, list] of grouped) {
    const times = list.map(r => r.ms).sort((a, b) => a - b)
    byRoute[key] = {
      count: list.length,
      bytes: list.reduce((sum, r) => sum + r.bytes, 0),
      p50: round1(percentile(times, 50)),
      p95: round1(percentile(times, 95)),
    }
  }

  const byMeasure: Record<string, { count: number; p50: number; p95: number; max: number }> = {}
  const measureGroups = new Map<string, number[]>()
  for (const m of measures) {
    const list = measureGroups.get(m.name)
    if (list) list.push(m.ms)
    else measureGroups.set(m.name, [m.ms])
  }
  for (const [name, list] of measureGroups) {
    const sorted = [...list].sort((a, b) => a - b)
    byMeasure[name] = {
      count: sorted.length,
      p50: round1(percentile(sorted, 50)),
      p95: round1(percentile(sorted, 95)),
      max: round1(sorted[sorted.length - 1]),
    }
  }

  const mem = (performance as any).memory
  return {
    requestTotal: requests.length,
    requestBytes: requests.reduce((sum, r) => sum + r.bytes, 0),
    byRoute,
    byMeasure,
    longTasks: {
      count: longTasks.length,
      totalMs: round1(longTasks.reduce((sum, t) => sum + t.ms, 0)),
      maxMs: round1(longTasks.reduce((max, t) => Math.max(max, t.ms), 0)),
    },
    dom: { ...dom, nodes: document.getElementsByTagName('*').length },
    usedJSHeapMB: mem ? round1(mem.usedJSHeapSize / 1048576) : null,
  }
}

function reset(): void {
  marks.clear()
  requests.length = 0
  measures.length = 0
  longTasks.length = 0
  dom.mutations = 0
  dom.addedNodes = 0
  dom.removedNodes = 0
}

let installed = false

/** 挂载 window.__rmPerf，并开始采集长任务与 DOM 变更量。 */
export function installPerfProbe(extra: Record<string, unknown> = {}): void {
  if (installed || typeof window === 'undefined') return
  installed = true

  ;(window as any).__rmPerf = {
    reset,
    snapshot,
    mark: perfMark,
    measure: perfMeasure,
    requests,
    measures,
    longTasks,
    ...extra,
  }

  try {
    const observer = new PerformanceObserver(list => {
      for (const entry of list.getEntries()) {
        longTasks.push({ ms: entry.duration, at: entry.startTime })
      }
    })
    observer.observe({ entryTypes: ['longtask'] })
  } catch (_) {
    // 部分环境不支持 longtask，忽略即可
  }

  const start = () => {
    const mutationObserver = new MutationObserver(records => {
      for (const record of records) {
        dom.mutations++
        dom.addedNodes += record.addedNodes.length
        dom.removedNodes += record.removedNodes.length
      }
    })
    mutationObserver.observe(document.body, { childList: true, subtree: true, attributes: true, characterData: true })
  }
  if (document.body) start()
  else window.addEventListener('DOMContentLoaded', start, { once: true })
}
