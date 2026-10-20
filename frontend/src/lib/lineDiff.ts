export type DiffLine = { type: 'same' | 'add' | 'del'; text: string }

/** 行级 LCS 差异（无第三方依赖）。 */
export function lineDiffLcs(a: string, b: string): DiffLine[] {
  const A = a.split('\n')
  const B = b.split('\n')
  const n = A.length
  const m = B.length
  const dp: number[][] = Array.from({ length: n + 1 }, () => Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[i][j] = A[i] === B[j] ? dp[i + 1][j + 1] + 1 : Math.max(dp[i + 1][j], dp[i][j + 1])
    }
  }
  const out: DiffLine[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (A[i] === B[j]) {
      out.push({ type: 'same', text: A[i] })
      i++
      j++
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ type: 'del', text: A[i] })
      i++
    } else {
      out.push({ type: 'add', text: B[j] })
      j++
    }
  }
  while (i < n) {
    out.push({ type: 'del', text: A[i++] })
  }
  while (j < m) {
    out.push({ type: 'add', text: B[j++] })
  }
  return out
}
