/**
 * L15 R22 交互/文案缺陷守卫（issue #103 / #104 / #107 / #108）。
 *
 * 运行：
 *   node test/l15_app_ux.mjs                   # 源码级断言 + 变异自证（CI 默认路径，无需容器）
 *   node test/l15_app_ux.mjs --with-browser     # 额外用真 Chromium 复现四条缺陷
 *
 * ---------------------------------------------------------------------------
 * 四条 issue（均由自动化真人点击测试产出）
 * ---------------------------------------------------------------------------
 * #103 方向生成的上游未就绪错误以**英文原文**直接 toast 给用户。实测原文：
 *      `cannot enqueue directions: dataset 134 has no domains, run domains/generate first`
 *      —— 同时泄漏内部接口路径（domains/generate）、英文句式，且不是用户能执行的指令。
 * #104 「数据资产」页（/console/results）自我声明包含「交付文件」，却**整页没有下载入口**。
 *      实测（任务 #50，确有 1 个交付文件）：「含『下载』字样的按钮数: 0」。
 * #107 登录页空表单提交**没有本地必填校验**，把后端英文
 *      `email and password are required` 原样展示在告警卡与 toast 两处。
 * #108 「新增策略」弹窗的「领域数」**默认预填 1000**，只填名称就保存会得到一条
 *      按 1000 个领域规划的策略 —— 这正是 issue #63 里 domainCount=1000 脏数据的来源。
 *
 * ---------------------------------------------------------------------------
 * 为什么分两层（CI 可执行性是硬要求）
 * ---------------------------------------------------------------------------
 * CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，**都不起容器**。
 * 因此默认路径只做**源码级断言**（对源码文本求值），无网络、无容器、无浏览器，
 * 由 exit code 决定成败；真浏览器断言收进 `--with-browser`，未启用时 [SKIP] 且不影响 exit code
 * （避免把「环境不可达」误报为「断言失败」）。
 *
 * 变异自证：断言抽成**谓词函数**（problemsWithX(src)），把变异后的源码喂进谓词，
 * 断言返回的问题列表**非空**。这样「断言非空转」是被证明的，而不是被声称的。
 *
 *   反例（不要这样写）：
 *     record('变异：删掉标签 -> 断言失败', !mutated.includes(label))
 *   若源码里本来就没有该标签，replaceAll 是 no-op，`!false = true` 直接 PASS，什么也没证明。
 */

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const APP_SOURCE = path.join(WEB_ROOT, 'src', 'App.tsx')
const API_SOURCE = path.join(WEB_ROOT, 'src', 'lib', 'api.ts')

const BASE_URL = process.env.L15_R22_BASE ?? 'http://127.0.0.1:3210'
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

const source = readFileSync(APP_SOURCE, 'utf8')
const apiSource = readFileSync(API_SOURCE, 'utf8')

/** 取从 `from` 到 `to`（不含）之间的源码片段；找不到返回 null。 */
function sliceBetween(text, from, to) {
  const start = text.indexOf(from)
  if (start < 0) return null
  const end = text.indexOf(to, start + from.length)
  return end < 0 ? text.slice(start) : text.slice(start, end)
}

// ---------------------------------------------------------------------------
// 谓词函数（变异用例会把「变异后的源码」喂进这些函数，断言返回的列表非空）
// ---------------------------------------------------------------------------

/** #107：登录页必须在**提交前**做本地必填/格式校验。 */
function problemsWithLoginValidation(appSrc) {
  const problems = []
  const view = sliceBetween(appSrc, 'function LoginPage(', '\nexport default function App()')
  if (view === null) { problems.push('未找到 LoginPage 组件'); return problems }

  if (!/请输入邮箱/.test(view)) problems.push('缺少本地必填提示「请输入邮箱」')
  if (!/请输入密码/.test(view)) problems.push('缺少本地必填提示「请输入密码」')
  if (!/邮箱格式不正确|格式不正确/.test(view)) problems.push('缺少提交前的邮箱格式校验（issue #107 第 3 步）')

  // 校验必须在提交前短路：提交处理器里必须有 `if (!validate()) return` 之类的守卫。
  const submit = sliceBetween(view, 'const handleSubmit', '\n  return (')
  if (submit === null) {
    problems.push('未找到 LoginPage 的提交处理器 handleSubmit')
  } else if (!/if\s*\(!validate\(\)\)\s*return/.test(submit)) {
    problems.push('提交处理器没有「校验未过即返回」的守卫，本地校验无法阻止请求发出')
  }

  // 表单提交与回车都必须走带校验的处理函数，而不是直接 onSubmit。
  // 只检查 form 片段，避免把 handleSubmit 内部的合法调用误判为绕过校验。
  const form = sliceBetween(view, '<form', '</form>') ?? ''
  if (/onClick=\{\(\) => void onSubmit\(email, password\)\}/.test(view) || /onSubmit\(email, password\)/.test(form)) {
    problems.push('登录按钮直接调用 onSubmit，绕过了本地校验')
  }
  return problems
}

/** #103：后端英文句式必须在**统一入口**被本地化，而不是各调用点各自处理。 */
function problemsWithLocalizedApiErrors(appSrc, apiSrc) {
  const problems = []
  const cap = sliceBetween(appSrc, 'const capabilityActions', '\n  const renderTaskDetail')
  if (cap === null) { problems.push('未找到 capabilityActions'); return problems }

  // 后端那句英文必须有一条翻译规则。
  if (!/cannot enqueue directions/i.test(apiSrc)) {
    problems.push('lib/api.ts 缺少 `cannot enqueue directions` 的本地化规则（英文原文会直达 toast）')
  }
  // 翻译必须**在响应拦截器内部**被调用（否则只修了某一个调用点，其余仍漏出英文）。
  // 这里取拦截器块再检查，而不是全文搜索 —— 全文搜索在「删掉接线但函数仍在」时会漏判。
  const interceptorBlock = sliceBetween(apiSrc, 'client.interceptors.response.use', '\nfunction localizeApiMessage')
  if (interceptorBlock === null) {
    problems.push('lib/api.ts 里没有 axios 响应拦截器（或结构与预期不同）')
  } else if (!/localizeApiMessage\(rawMessage/.test(interceptorBlock)) {
    problems.push('拦截器内部没有调用 localizeApiMessage（后端英文会直达各调用点）')
  }
  // 兜底：无汉字的消息也必须有中文落地，防止后端新增文案时再次露英文。
  if (!/hasNoChinese/.test(apiSrc)) {
    problems.push('缺少「无汉字」兜底（后端新增英文文案时界面会再次露英文）')
  }
  // 能力入口必须仍然把失败交给统一错误处理（不自己吞错、不直接透出英文）。
  if (!/handleRequestError\(error\)/.test(cap)) {
    problems.push('能力入口没有走统一的 handleRequestError')
  }
  return problems
}

/** #104：数据资产页必须有下载入口，且**复用**既有下载实现。 */
function problemsWithResultsHubDownload(appSrc) {
  const problems = []
  const hub = sliceBetween(appSrc, 'const renderResultsHub = () =>', '\n  const renderOperations')
  if (hub === null) { problems.push('未找到 renderResultsHub'); return problems }

  if (!/下载/.test(hub)) {
    problems.push('数据资产页没有下载入口（issue #104）')
  }
  if (/下载/.test(hub) && !/downloadArtifact\(/.test(hub)) {
    problems.push('下载入口没有复用既有 downloadArtifact 实现')
  }
  if (/createObjectURL/.test(hub)) {
    problems.push('数据资产页自行实现了 blob 下载（应复用 downloadArtifact）')
  }
  return problems
}

/** #108：新增策略的「领域数」默认值不得是 1000。 */
function problemsWithStrategyDomainDefault(appSrc) {
  const problems = []
  const draft = sliceBetween(appSrc, 'const makeEmptyStrategyDraft', '\n  }), []')
  if (draft === null) {
    problems.push('未找到 makeEmptyStrategyDraft')
  } else if (/domainCount:\s*1000/.test(draft)) {
    problems.push('makeEmptyStrategyDraft 的 domainCount 默认仍是 1000')
  }

  const init = sliceBetween(appSrc, 'const [strategyDraft, setStrategyDraft] = useState<Partial<Strategy>>({', '\n  })')
  if (init !== null && /domainCount:\s*1000/.test(init)) {
    problems.push('strategyDraft 的初始 domainCount 仍是 1000（用户一打开弹窗就看到的值）')
  }

  if (/strategyDraft\.domainCount\s*\?\?\s*1000/.test(appSrc)) {
    problems.push('策略弹窗输入框仍有 `?? 1000` 兜底（issue #108 的隐藏来源）')
  }
  return problems
}

// ---------------------------------------------------------------------------
// 应用谓词
// ---------------------------------------------------------------------------

record('#107 登录页有本地必填/格式校验，且提交前短路',
  problemsWithLoginValidation(source).length === 0,
  problemsWithLoginValidation(source).join('; ') || '本地校验齐备且提交前短路')

record('#103 后端英文错误在拦截器统一本地化，能力入口不再直达英文',
  problemsWithLocalizedApiErrors(source, apiSource).length === 0,
  problemsWithLocalizedApiErrors(source, apiSource).join('; ') || '本地化已接在拦截器上且有兜底')

record('#104 数据资产页提供下载入口且复用既有下载实现',
  problemsWithResultsHubDownload(source).length === 0,
  problemsWithResultsHubDownload(source).join('; ') || '有下载入口且复用 downloadArtifact')

record('#108 新增策略的「领域数」默认值不再是 1000',
  problemsWithStrategyDomainDefault(source).length === 0,
  problemsWithStrategyDomainDefault(source).join('; ') || '无 1000 默认值残留')

// ---------------------------------------------------------------------------
// 变异自证：把修复改回原状，谓词必须报出问题
// ---------------------------------------------------------------------------

/** 变异 1（#108）：把默认领域数改回 1000。 */
const mut108 = source.replace(
  /domainCount:\s*DEFAULT_STRATEGY_DOMAIN_COUNT,\n    questionsPerDomain/,
  'domainCount: 1000,\n    questionsPerDomain',
)
record('#108 变异：把默认领域数改回 1000 -> 谓词必须报错',
  /domainCount:\s*1000,/.test(mut108) && problemsWithStrategyDomainDefault(mut108).length > 0,
  problemsWithStrategyDomainDefault(mut108).join('; ') || '未被捕获（断言空转）')

/** 变异 2（#104）：删掉数据资产页的下载按钮。 */
const mut104 = source.replace(
  /<Button size="small" theme="solid" type="primary" onClick=\{\(\) => void downloadArtifact\(artifact\)\}>[\s\S]*?<\/Button>/,
  '',
)
record('#104 变异：删掉数据资产页的下载按钮 -> 谓词必须报错',
  problemsWithResultsHubDownload(mut104).length > 0,
  problemsWithResultsHubDownload(mut104).join('; ') || '未被捕获（断言空转）')

/** 变异 3（#103）：删掉拦截器里的本地化接线。 */
const mutApi103 = apiSource.replace(
  'localizeApiMessage(rawMessage, statusCode, fallbackMessage)',
  'rawMessage',
)
record('#103 变异：删掉拦截器里的本地化接线 -> 谓词必须报错',
  mutApi103 !== apiSource && problemsWithLocalizedApiErrors(source, mutApi103).length > 0,
  problemsWithLocalizedApiErrors(source, mutApi103).join('; ') || '未被捕获（断言空转）')

/** 变异 4（#107）：删掉登录页的本地必填提示。 */
const mut107 = source.replaceAll('请输入邮箱', '邮箱').replaceAll('请输入密码', '密码')
record('#107 变异：删掉本地必填提示 -> 谓词必须报错',
  problemsWithLoginValidation(mut107).length > 0,
  problemsWithLoginValidation(mut107).join('; ') || '未被捕获（断言空转）')

/** 变异 5（#107）：让登录按钮绕过校验直接提交。 */
const mut107b = source.replace(
  'event.preventDefault()\n              handleSubmit()',
  'event.preventDefault()\n              void onSubmit(email, password)',
)
record('#107 变异：让登录按钮绕过校验 -> 谓词必须报错',
  problemsWithLoginValidation(mut107b).length > 0,
  problemsWithLoginValidation(mut107b).join('; ') || '未被捕获（断言空转）')

// ---------------------------------------------------------------------------
// 可选：真浏览器复现（修复前应「坏」，修复后应「好」）
// ---------------------------------------------------------------------------

async function browserChecks() {
  const require = createRequire(path.join(WEB_ROOT, 'package.json'))
  let chromium
  try {
    ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
  } catch (error) {
    record('加载 playwright', false, String(error?.message ?? error))
    return
  }

  const browser = await chromium.launch({ headless: true })
  try {
    // ---- #107：空表单提交不得出现后端英文原文，且要有本地提示 ----
    {
      const page = await browser.newPage()
      await page.goto(`${BASE_URL}/login`)
      const emailInput = page.locator('input').first()
      await emailInput.click()
      await emailInput.fill('')
      const pwd = page.locator('input[type=password]').first()
      await pwd.click()
      await pwd.fill('')
      await page.getByRole('button', { name: /进入我的任务/ }).first().click()
      await page.waitForTimeout(1500)
      const body = await page.locator('body').innerText()
      const hasEnglish = /email and password are required/.test(body)
      const hasLocalHint = /请输入邮箱/.test(body) && /请输入密码/.test(body)
      record('#107 真浏览器：空提交出现本地必填提示', hasLocalHint,
        hasLocalHint ? '页面出现「请输入邮箱」与「请输入密码」' : `未出现本地提示（页面含：${body.slice(-200)}）`)
      record('#107 真浏览器：空提交不出现后端英文原文', !hasEnglish,
        hasEnglish ? '仍出现 `email and password are required`' : '未出现英文原文')
      await page.close()
    }

    // ---- 登录（后续用例需要登录态） ----
    const page = await browser.newPage()
    await page.goto(`${BASE_URL}/login`)
    await page.locator('input').first().fill('admin@company.com')
    await page.locator('input[type=password]').first().fill('admin123456')
    await page.getByRole('button', { name: /进入我的任务/ }).first().click()
    await page.waitForURL(/console/, { timeout: 20_000 })
    await page.waitForTimeout(2000)

    // ---- #104：数据资产页必须有下载入口（选一个确有交付文件的任务） ----
    {
      const target = await page.evaluate(async () => {
        const res = await fetch('/api/v1/datasets', { credentials: 'include' })
        const list = await res.json()
        for (const d of list) {
          const a = await fetch(`/api/v1/datasets/${d.id}/export`, { credentials: 'include' })
          if (!a.ok) continue
          const arts = await a.json()
          if (Array.isArray(arts) && arts.length > 0) return { datasetId: d.id, artifacts: arts.length }
        }
        return null
      })
      if (!target) {
        recordSkip('#104 真浏览器：数据资产页有下载入口', '库里没有已产出交付文件的任务（输入缺失）')
      } else {
        // 必须走**真实用户路径**：先进任务详情选中任务，再用侧边栏点进「数据资产」。
        // 不能直接 goto('/console/results') —— 那是整页重载，会丢掉 activeDatasetId
        //（阶段路由不携带数据集上下文，属另一个已知问题），
        // 届时页面本来就无数据，测不出「有交付文件却没有下载入口」这条缺陷。
        await page.goto(`${BASE_URL}/console/tasks/${target.datasetId}`)
        await page.waitForTimeout(2500)
        await page.locator('.app-layout__sidebar').getByText('数据资产', { exact: true }).first().click()
        await page.waitForTimeout(3000)
        const buttons = await page.locator('button').evaluateAll((els) => els.map((e) => (e.textContent || '').trim()))
        const downloadButtons = buttons.filter((t) => t.includes('下载'))
        record(`#104 真浏览器：数据资产页有下载入口（任务 #${target.datasetId}，${target.artifacts} 个交付文件）`,
          downloadButtons.length > 0,
          downloadButtons.length > 0 ? `下载按钮：${JSON.stringify(downloadButtons)}` : '含「下载」字样的按钮数 = 0')
      }
    }

    // ---- #103：方向生成的上游未就绪错误必须中文化 ----
    {
      const draft = await page.evaluate(async () => {
        const res = await fetch('/api/v1/datasets', { credentials: 'include' })
        const list = await res.json()
        for (const d of list) {
          if (d.status !== 'draft') continue
          const g = await fetch(`/api/v1/datasets/${d.id}`, { credentials: 'include' })
          if (!g.ok) continue
          const graph = await g.json()
          if ((graph.domains ?? []).length === 0) return { datasetId: d.id }
        }
        return null
      })
      if (!draft) {
        recordSkip('#103 真浏览器：方向生成错误中文化', '没有「draft 且无领域」的任务（输入缺失）')
      } else {
        await page.goto(`${BASE_URL}/console/tasks/${draft.datasetId}`)
        await page.waitForTimeout(2500)
        const btn = page.getByRole('button', { name: '生成方向', exact: true })
        if (await btn.count() === 0) {
          recordSkip('#103 真浏览器：方向生成错误中文化', '未找到「生成方向」按钮')
        } else {
          await btn.first().click()
          await page.waitForTimeout(2500)
          const body = await page.locator('body').innerText()
          const hasEnglish = /cannot enqueue directions/.test(body)
          const hasChinese = /请先完成并确认主题结构/.test(body)
          record(`#103 真浏览器：不出现英文原文（任务 #${draft.datasetId}）`, !hasEnglish,
            hasEnglish ? '仍出现 `cannot enqueue directions: ...`' : '未出现英文原文')
          record(`#103 真浏览器：出现中文可执行提示（任务 #${draft.datasetId}）`, hasChinese,
            hasChinese ? '出现「请先完成并确认主题结构，再生成方向。」' : '未出现中文可执行提示')
        }
      }
    }

    // ---- #108：新增策略弹窗的「领域数」默认值 ----
    {
      await page.goto(`${BASE_URL}/console/admin/strategies`)
      await page.waitForTimeout(2500)
      const addBtn = page.getByRole('button', { name: /新增策略/ })
      if (await addBtn.count() === 0) {
        recordSkip('#108 真浏览器：领域数默认值', '未找到「新增策略」按钮')
      } else {
        await addBtn.first().click()
        await page.waitForTimeout(1200)
        const values = await page
          .locator('.semi-modal input, .semi-modal .semi-input-number input')
          .evaluateAll((els) => els.map((e) => e.value))
        const has1000 = values.includes('1000')
        record('#108 真浏览器：新增策略弹窗不预填 1000', !has1000,
          has1000 ? `仍预填 1000（弹窗输入值：${JSON.stringify(values)}）` : `弹窗输入值：${JSON.stringify(values)}`)
        const cancel = page.getByRole('button', { name: /取消/ })
        if (await cancel.count() > 0) await cancel.first().click()
      }
    }

    await page.close()
  } catch (error) {
    record('真浏览器用例执行', false, String(error?.message ?? error))
  } finally {
    await browser.close()
  }
}

if (WITH_BROWSER) {
  await browserChecks()
} else {
  recordSkip('真浏览器四条缺陷复现', '未启用 --with-browser（默认路径不需要浏览器）')
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`APP UX FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`APP UX OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
