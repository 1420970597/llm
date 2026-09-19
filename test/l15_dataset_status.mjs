/**
 * L15 数据集状态渲染守卫（issue #98）。
 *
 * 运行：
 *   node test/l15_dataset_status.mjs              # 默认路径（无需容器，CI 可执行）
 *   node test/l15_dataset_status.mjs --with-browser  # 额外用真 Chromium 验证页面
 *
 * ---------------------------------------------------------------------------
 * 与 test/dataset_status_coverage_test.go 的分工（两层，不重复）
 * ---------------------------------------------------------------------------
 * Go 守卫是**源码级**的：它从后端 Go 源码提取状态值，断言前端有对应处理，
 * 并断言 statusLabel 不再 `default: return status`。
 *
 * 本脚本是**运行时**的：它真的把 `lib/datasetStatus.ts` 打包执行，
 * 调用 `describeDatasetStatus()` 并断言返回值。两者不可互相替代：
 *   - 源码级能证明「有人写了这个分支」，证明不了「这个分支跑起来是对的」；
 *   - 运行时能证明「翻译结果正确」，但它只覆盖前端声明的那些值，
 *     覆盖不到「后端还有哪些值没被前端声明」—— 那是 Go 守卫的职责。
 *
 * ---------------------------------------------------------------------------
 * 为什么默认路径不依赖容器（硬要求）
 * ---------------------------------------------------------------------------
 * CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，
 * **都不起容器**。因此本脚本默认路径必须无网络无容器即可跑绿并决定 exit code；
 * 需要真浏览器的断言收进 `--with-browser`，未启用时输出 [SKIP] 且不影响 exit code。
 */

import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const STATUS_MODULE = path.join(WEB_ROOT, 'src', 'lib', 'datasetStatus.ts')
const APP_SOURCE = path.join(WEB_ROOT, 'src', 'App.tsx')

const BASE_URL = process.env.L15_STATUS_BASE_URL ?? 'http://127.0.0.1:3210'
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

// ---------------------------------------------------------------------------
// 打包并真实执行 datasetStatus.ts
// ---------------------------------------------------------------------------

const webRequire = createRequire(path.join(WEB_ROOT, 'package.json'))
const esbuild = webRequire('esbuild')

let statusModule = null
const workDir = mkdtempSync(path.join(tmpdir(), 'l15-status-'))
try {
  const outfile = path.join(workDir, 'datasetStatus.cjs')
  await esbuild.build({
    entryPoints: [STATUS_MODULE],
    bundle: true,
    outfile,
    format: 'cjs',
    platform: 'node',
    absWorkingDir: WEB_ROOT,
    nodePaths: [path.join(WEB_ROOT, 'node_modules'), path.join(REPO_ROOT, 'node_modules')],
    mainFields: ['module', 'main'],
    logLevel: 'silent',
    define: { 'process.env.NODE_ENV': '"production"' },
  })
  statusModule = createRequire(import.meta.url)(outfile)
  record('datasetStatus.ts 可被打包并加载', true, `导出：${Object.keys(statusModule).join(', ')}`)
} catch (error) {
  record('datasetStatus.ts 可被打包并加载', false, String(error?.message ?? error))
}

if (statusModule) {
  const { DATASET_STATUS_LABELS, DATASET_STATUS_PROGRESS, DATASET_STATUS_ROUTE, describeDatasetStatus } = statusModule

  // ---- 1. 运行时断言：每个已知状态的文案是中文，且不等于原始状态串 ----
  const han = /[\p{Script=Han}]/u
  for (const [status, label] of Object.entries(DATASET_STATUS_LABELS)) {
    const ok = typeof label === 'string' && label !== status && han.test(label)
    record(`状态 ${status} 的运行时文案是中文且不等于原始串`, ok, `「${label}」`)
  }

  // ---- 2. 运行时断言：三张表覆盖同一组 key（只补文案会显示 0% / 跳错页）----
  const labelsKeys = Object.keys(DATASET_STATUS_LABELS).sort()
  const progressKeys = Object.keys(DATASET_STATUS_PROGRESS).sort()
  const routeKeys = Object.keys(DATASET_STATUS_ROUTE).sort()
  record('LABELS / PROGRESS 的 key 集合一致（否则进度会显示 0%）',
    labelsKeys.join(',') === progressKeys.join(','),
    `labels=${labelsKeys.length} progress=${progressKeys.length}`)
  record('LABELS / ROUTE 的 key 集合一致（否则主按钮会跳错页）',
    labelsKeys.join(',') === routeKeys.join(','),
    `labels=${labelsKeys.length} routes=${routeKeys.length}`)

  // ---- 3. 运行时断言：describeDatasetStatus 永不泄漏原始英文串 ----
  // 覆盖：已知值、动态拼接值（真实库里出现过的）、完全未知值、空串。
  const neverLeakSamples = [
    ...labelsKeys,
    // 真实库里已观察到的动态失败状态
    'chain-standards.generate_failed',
    // 其余注册表里可能出现的动态形式
    'grpo.generate_failed',
    'sft.generate_failed',
    'cleaning.run_failed',
    'eval.run_failed',
    'directions.generate_failed',
    'export.generate_failed',
    'questions.generate_failed',
    // 完全未知
    'some_future_status_v9',
    '',
  ]
  const leaked = []
  for (const sample of neverLeakSamples) {
    const out = describeDatasetStatus(sample)
    if (typeof out?.label !== 'string' || out.label.length === 0) {
      leaked.push(`${JSON.stringify(sample)} -> 空文案`)
      continue
    }
    // 空串是特例：兜底文案不可能是空串，但也不能是空串本身
    if (sample !== '' && out.label === sample) leaked.push(`${JSON.stringify(sample)} -> 原样返回`)
    if (!han.test(out.label)) leaked.push(`${JSON.stringify(sample)} -> 非中文「${out.label}」`)
  }
  record(`describeDatasetStatus 在 ${neverLeakSamples.length} 个样本上都不泄漏原始状态串`,
    leaked.length === 0, leaked.length ? leaked.join('; ') : '全部返回中文文案')

  // ---- 4. 运行时断言：issue #98 的具体症状已消除 ----
  const directions = describeDatasetStatus('directions_completed')
  record('directions_completed 不再原样显示（issue #98 的核心症状）',
    directions.label !== 'directions_completed' && han.test(directions.label),
    `「${directions.label}」 known=${directions.known}`)
  record('directions_completed 被标为已知状态', directions.known === true,
    `known=${directions.known}`)

  record('directions_completed 的进度非零（issue #98 的第二个症状）',
    Number(DATASET_STATUS_PROGRESS.directions_completed) > 0,
    `progress=${DATASET_STATUS_PROGRESS.directions_completed}%`)

  record('directions_completed 的下一步是「问题生成」而非回「主题结构」（issue #98 的第四个症状）',
    DATASET_STATUS_ROUTE.directions_completed === '/console/questions',
    `route=${DATASET_STATUS_ROUTE.directions_completed}`)

  const partial = describeDatasetStatus('directions_partial_failed')
  record('directions_partial_failed 有独立中文文案', partial.label !== 'directions_partial_failed' && han.test(partial.label),
    `「${partial.label}」`)

  // ---- 5. 运行时断言：动态失败状态走后缀规则，文案里带上阶段名 ----
  const dyn = describeDatasetStatus('chain-standards.generate_failed')
  record('chain-standards.generate_failed 走后缀规则且文案可读',
    dyn.label !== 'chain-standards.generate_failed' && han.test(dyn.label),
    `「${dyn.label}」 known=${dyn.known}`)

  // ---- 6. 变异自证：把兜底文案改成原样返回，上面的断言必须失败 ----
  // 这里不修改产品代码，而是在测试内构造一个「坏的」翻译函数，
  // 证明本脚本的断言逻辑确实能识别泄漏（而非恒真）。
  const badTranslate = (raw) => ({ label: raw, known: false })
  const badLeaks = neverLeakSamples.some((s) => {
    const out = badTranslate(s)
    return s !== '' && (out.label === s || !han.test(out.label))
  })
  record('变异自证：原样返回原始串的实现会被本脚本判定为泄漏',
    badLeaks === true,
    badLeaks ? '坏实现被识别为泄漏（断言非空转）' : '坏实现未被识别，断言可能是空转')
}

// ---------------------------------------------------------------------------
// 7. 源码级：App.tsx 仍从单一来源取文案（防止有人绕过模块手写 switch）
// ---------------------------------------------------------------------------
const appSource = readFileSync(APP_SOURCE, 'utf8')
record('App.tsx 的 statusLabel 使用单一事实来源 describeDatasetStatus',
  /function statusLabel\(status: string\)\s*\{\s*return describeDatasetStatus\(status\)\.label/.test(appSource.replace(/\s+/g, ' ')),
  /describeDatasetStatus/.test(appSource) ? '已接线' : '未接线')

record('App.tsx 不再有 `default: return status` 形态的状态泄漏',
  !/case '[a-z_]+':\s*return '[^']*'\s*default:\s*return status/.test(appSource.replace(/\s+/g, ' ')),
  'statusLabel 已改为委托单一来源')

// ---------------------------------------------------------------------------
// 8. 可选：真浏览器验证（--with-browser）
// ---------------------------------------------------------------------------
if (WITH_BROWSER) {
  try {
    const require = createRequire(path.join(REPO_ROOT, 'package.json'))
    let chromium
    try {
      ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
    } catch {
      ;({ chromium } = require('playwright'))
    }
    const browser = await chromium.launch({ headless: true })
    const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
    const page = await context.newPage()
    try {
      await page.goto(`${BASE_URL}/login`, { waitUntil: 'load' })
      await page.locator('input').first().fill('admin@company.com')
      await page.locator('input[type=password]').fill('admin123456')
      await page.locator('button[type=submit], .semi-button-primary').first().click()
      await page.waitForURL(/console/, { timeout: 20_000 })
      await page.waitForTimeout(2000)

      // 找出一个处于 directions_completed 的数据集（若不存在则 SKIP，不伪造）
      const target = await page.evaluate(async () => {
        const res = await fetch('/api/v1/datasets', { credentials: 'include' })
        const list = await res.json()
        return (list ?? []).find((item) => item.status === 'directions_completed') ?? null
      })

      if (!target) {
        recordSkip('真浏览器：directions_completed 数据集的详情页不显示原始状态串',
          '当前库中没有处于 directions_completed 的数据集，无法验证（不伪造通过）')
      } else {
        await page.goto(`${BASE_URL}/console/tasks/${target.id}`, { waitUntil: 'load' })
        await page.waitForTimeout(2500)
        const body = (await page.locator('body').innerText()).replace(/\s+/g, ' ')
        record(`真浏览器：数据集 #${target.id}（directions_completed）页面不显示原始状态串`,
          !body.includes('directions_completed'),
          body.includes('directions_completed') ? '页面仍出现原始状态串' : '未出现原始状态串')
        record(`真浏览器：数据集 #${target.id} 页面显示中文状态文案`,
          body.includes('方向已生成'),
          body.includes('方向已生成') ? '显示「方向已生成，待生成问题」' : '未找到中文状态文案')
      }
    } finally {
      await browser.close()
    }
  } catch (error) {
    record('真浏览器断言', false, String(error?.message ?? error))
  }
} else {
  recordSkip('真浏览器：页面不显示原始状态串',
    '未启用 --with-browser（默认路径不需要浏览器）')
}

rmSync(workDir, { recursive: true, force: true })

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------
console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`DATASET STATUS FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`DATASET STATUS OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
