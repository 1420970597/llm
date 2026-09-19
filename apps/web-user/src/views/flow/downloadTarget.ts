/**
 * 结果文件下载目标校验。
 *
 * 抽成纯函数的原因：原实现内联在 App.tsx 的 downloadArtifact 闭包里，
 * 无法单独测试。这里把「什么算合法下载目标」的规则独立出来，
 * 既便于测试，也避免日后在组件里各处重复写校验。
 */

/** 只允许同源相对路径，防止重构意外产出绝对 URL（如直接使用后端返回的 downloadUrl）。 */
const ALLOWED_PREFIX = '/api/'

/**
 * 把输入严格化为正整数。
 *
 * 不能只用 `Number(v)` 后判 `Number.isInteger`：`Number(true) === 1`、
 * `Number([5]) === 5`、`Number('') === 0`、`Number('0x10') === 16`、
 * `Number('1e3') === 1000` —— 这些意外转换会让非数字输入通过校验，
 * 与「严格校验」的语义不符。故先做类型与字面检查。
 *
 * @returns 合法时返回该正整数，否则 `null`。
 */
function toPositiveInt(value: unknown): number | null {
  if (typeof value === 'number') {
    return Number.isInteger(value) && value > 0 ? value : null
  }
  if (typeof value === 'string') {
    const trimmed = value.trim()
    // 只接受纯十进制数字串，排除空串、十六进制、科学计数法、前导 +/- 等
    if (!/^\d+$/.test(trimmed)) return null
    const parsed = Number(trimmed)
    return Number.isInteger(parsed) && parsed > 0 ? parsed : null
  }
  return null
}

/**
 * 校验并规范化下载目标。
 *
 * @returns 合法时返回规范化后的 `{ datasetId, artifactId }`，否则返回 `null`。
 */
export function resolveDownloadTarget(
  datasetId: unknown,
  artifactId: unknown,
): { datasetId: number; artifactId: number } | null {
  const ds = toPositiveInt(datasetId)
  const art = toPositiveInt(artifactId)
  if (ds === null || art === null) return null
  return { datasetId: ds, artifactId: art }
}

/**
 * 校验同源下载路径。传入由 resolveDownloadTarget 结果拼出的相对路径。
 *
 * @returns 合法时返回该路径，否则返回 `null`。
 */
export function resolveDownloadPath(path: string): string | null {
  return path.startsWith(ALLOWED_PREFIX) ? path : null
}

/** 供测试与调用方复用的白名单前缀。 */
export const DOWNLOAD_PATH_PREFIX = ALLOWED_PREFIX
