/**
 * 导出工件下载路径守卫（自包含，CI 可执行，无需容器）。
 *
 * 运行：
 *   node test/l15_artifact_download.mjs               # 源码级断言（CI 默认路径）
 *   node test/l15_artifact_download.mjs --with-browser # 真浏览器点真实下载
 *
 * ---------------------------------------------------------------------------
 * 背景
 * ---------------------------------------------------------------------------
 * 此前的 downloadArtifact 用**裸 `fetch`** 拉二进制工件，而本模块其余 74 个接口
 * 全部走共享的 axios `client`。后果：
 *   1. 绕过 `withCredentials` 与**响应拦截器** —— 会话过期时用户看到裸 HTTP 错误，
 *      而不是拦截器给出的中文提示「登录状态已失效，请重新登录。」；
 *   2. 错误文案与全站不一致（fetch 路径自己拼 message）；
 *   3. 静态扫描对裸 fetch 持续报 "Potential SSRF"。该报告本身是**误报**
 *      （SSRF 要求服务端发起 + host 受控，而这里两个入参都是 number、URL 是
 *      相对同源路径，见 docs/plans/round2-security-finding-triage.md），
 *      但裸 fetch 确实是本仓库的唯一例外，统一到 client 后例外消失。
 *
 * 本测试锁定：下载必须走 client，且不再存在裸 fetch。
 */

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const APP_SOURCE = path.join(REPO_ROOT, 'apps', 'web-user', 'src', 'App.tsx')
const API_SOURCE = path.join(REPO_ROOT, 'apps', 'web-user', 'src', 'lib', 'api.ts')

const WITH_BROWSER = process.argv.includes('--with-browser')

const failures = []
const results = []
function record(name, ok, detail) {
  results.push({ name, ok, detail })
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(name)
}
function recordSkip(name, detail) {
  results.push({ name, ok: true, skipped: true, detail })
  console.log(`[SKIP] ${name}: ${detail}`)
}

const app = readFileSync(APP_SOURCE, 'utf8')
const api = readFileSync(API_SOURCE, 'utf8')

// ---- 1. 前端不再有裸 fetch ----
const bareFetch = app.match(/\bfetch\s*\(/g) ?? []
record('App.tsx 里不再有裸 fetch（已统一到共享 axios client）',
  bareFetch.length === 0,
  bareFetch.length === 0 ? '0 处' : `仍有 ${bareFetch.length} 处`)

// ---- 2. 下载走 client，且带 withCredentials / blob ----
record('lib/api.ts 提供 downloadArtifactBlob 且走 client',
  /downloadArtifactBlob\s*:/.test(api) && /client\s*\n?\s*\.get<Blob>/.test(api),
  '找到 downloadArtifactBlob 且使用 client.get<Blob>')

record('下载请求使用 responseType: blob（二进制工件不能按 JSON 解析）',
  /downloadArtifactBlob[\s\S]{0,400}responseType:\s*'blob'/.test(api),
  'responseType: blob 存在')

record('client 配置了 withCredentials（同源 Cookie 才能带上）',
  /withCredentials:\s*true/.test(api), 'withCredentials: true 存在')

record('响应拦截器把 401 转成可理解的中文提示',
  /statusCode\s*===\s*401[\s\S]{0,120}登录状态已失效/.test(api),
  '401 → 「登录状态已失效，请重新登录。」')

record('downloadArtifact 调用 consoleApi.downloadArtifactBlob',
  /consoleApi\.downloadArtifactBlob\s*\(/.test(app),
  'App.tsx 里找到该调用')

record('downloadArtifact 对空文件给出可操作提示（而不是静默下载 0 字节）',
  /blob\.size\s*===\s*0[\s\S]{0,200}throw new Error/.test(app),
  '找到 size === 0 的判定与抛错')

// ---- 3. 文件名解析：Content-Disposition 优先，回退对象键 ----
record('文件名解析优先用 Content-Disposition，缺失时回退对象键末段',
  /artifactFileName\s*:/.test(api) && /content-disposition/.test(app),
  'api.ts 提供 artifactFileName，App.tsx 传入 content-disposition 响应头')

function parseFileName(disposition, objectKey) {
  const matched = (disposition ?? '').match(/filename="?([^";]+)"?/)
  if (matched?.[1]) return matched[1]
  return objectKey.split('/').pop() || 'dataset-export.jsonl'
}
const fileCases = [
  ['attachment; filename="dataset-alpaca.jsonl"', 's3://b/k/other.jsonl', 'dataset-alpaca.jsonl'],
  ['attachment; filename=plain.jsonl', 's3://b/k/other.jsonl', 'plain.jsonl'],
  ['', 's3://llm-factory-dev/datasets/5/exports/dataset-alpaca.jsonl', 'dataset-alpaca.jsonl'],
  ['', '', 'dataset-export.jsonl'],
]
for (const [disp, key, want] of fileCases) {
  const got = parseFileName(disp, key)
  record(`文件名解析：disposition=${disp || '(空)'} key=${key || '(空)'} -> ${want}`,
    got === want, `得到 ${got}`)
}

// ---- 4. 变异自证：改回裸 fetch，断言必须失败 ----
const mutated = app.replace(
  /const response = await consoleApi\.downloadArtifactBlob\(/,
  'const response = await fetch(',
)
record('变异：把下载改回裸 fetch -> 「不再有裸 fetch」断言会失败',
  /\bfetch\s*\(/.test(mutated), '变异后源码里重新出现裸 fetch')

// ---- 5. 可选：真浏览器点一次真实下载 ----
if (WITH_BROWSER) {
  const BASE = process.env.L15_DL_BASE ?? 'http://127.0.0.1:3210'
  const require = createRequire(path.join(REPO_ROOT, 'package.json'))
  let chromium
  try {
    ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
  } catch (error) {
    record('加载 playwright', false, String(error?.message ?? error))
  }
  if (chromium) {
    const browser = await chromium.launch({ headless: true })
    try {
      const page = await browser.newPage({ acceptDownloads: true })
      await page.goto(`${BASE}/login`)
      await page.locator('input').first().fill(process.env.L15_ADMIN_EMAIL ?? 'admin@company.com')
      await page.locator('input[type=password]').fill(process.env.L15_ADMIN_PASSWORD ?? 'admin123456')
      await page.locator('button[type=submit], .semi-button-primary').first().click()
      await page.waitForURL(/console/, { timeout: 20_000 })

      // 找一个已经产出工件的任务
      const target = await page.evaluate(async () => {
        const res = await fetch('/api/v1/datasets', { credentials: 'include' })
        const list = await res.json()
        for (const d of list) {
          const a = await fetch(`/api/v1/datasets/${d.id}/export`, { credentials: 'include' })
          if (!a.ok) continue
          const arts = await a.json()
          if (Array.isArray(arts) && arts.length > 0) return { datasetId: d.id, artifactId: arts[0].id }
        }
        return null
      })

      if (!target) {
        recordSkip('真浏览器下载', '库里没有已产出工件的任务（输入缺失，不伪造通过）')
      } else {
        // 下载按钮在**任务详情页**（阶段页 /console/exports 只展示列表，没有下载按钮）。
        // 实测任务详情页的按钮文案是「下载结果后进入下一步」，用子串匹配。
        await page.goto(`${BASE}/console/tasks/${target.datasetId}`)
        await page.waitForTimeout(3000)
        const download = page.waitForEvent('download', { timeout: 30_000 })
        // 必须精确匹配：页头的导航按钮文案是「下载结果后进入下一步」（含「下载结果」子串），
        // 用 has-text 会先命中它 —— 那是个导航动作，不触发下载。
        // 真正的下载按钮文案恰好是「下载结果」。
        await page.getByRole('button', { name: '下载结果', exact: true }).first().click()
        const dl = await download
        const name = dl.suggestedFilename()
        const p = await dl.path()
        const size = p ? (await import('node:fs')).statSync(p).size : 0
        record('真浏览器点击下载产出真实文件',
          size > 0, `dataset=${target.datasetId} artifact=${target.artifactId} 文件=${name} 大小=${size}B`)
      }
    } catch (error) {
      record('真浏览器下载', false, String(error?.message ?? error))
    } finally {
      await browser.close()
    }
  }
} else {
  recordSkip('真浏览器下载', '未启用 --with-browser（默认路径不需要浏览器）')
}

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`ARTIFACT DOWNLOAD FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`ARTIFACT DOWNLOAD OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
