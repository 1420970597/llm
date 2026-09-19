/**
 * L15 R20 数据清洗页交互守卫（自包含，一条命令）。
 *
 * 运行：
 *   node test/l15_cleaning_ux.mjs                    # 源码级断言（CI 默认路径，无需容器）
 *   node test/l15_cleaning_ux.mjs --with-browser     # 真浏览器复现修复前/修复后两条路径
 *
 * ---------------------------------------------------------------------------
 * 覆盖的两个缺陷（均由自动化真人点击测试发现）
 * ---------------------------------------------------------------------------
 *   #105 删除自定义清洗关键词**没有二次确认**，点击即真删、不可撤销。
 *        且界面上不区分「内置词静默失效」与「自定义词静默真删」两种删除语义。
 *   #106 数据清洗页**一打开就弹与当前面板无关的告警**「还没有规则…」。
 *
 * ---------------------------------------------------------------------------
 * 为什么 #106 值得单独修（父代理复核时的关键发现）
 * ---------------------------------------------------------------------------
 * 那条提示在源码里是 Semi 的 `<Banner>`（内联的），**不是** Toast。但父代理实测确认：
 *   Semi 的 Banner 会**无条件**渲染 `role="alert"`
 *   （见 node_modules/@douyinfe/semi-ui/lib/es/banner/index.js：
 *      React.createElement("div", { className: wrapper, style: style, role: "alert" }, ...)
 *    且 BannerProps 里**没有任何 prop** 可以覆盖它）。
 * 后果有三层：
 *   1. 屏幕阅读器在**页面加载时**就把这条静态空状态当实时告警播报；
 *   2. 自动化无障碍巡检（按 role=alert 收集通知）把它识别成「toast」，
 *      于是表现为 issue #106 描述的「打开页面就弹无关 toast」；
 *   3. 它内联在「发起一次清洗」面板里，却长得像全局告警，内容与用户当前所在面板无关。
 * 因此修法不是「换个 toast 时机」，而是**空状态就地展示（且带可点击的下一步）**，
 * 并把这条 role=alert 从页面上彻底移除。
 *
 * ---------------------------------------------------------------------------
 * 为什么默认只跑源码级断言（CI 可执行性是硬要求）
 * ---------------------------------------------------------------------------
 * CI 的 Backend job 只跑 `go test`，Frontend job 只跑 `tsc + vite build`，
 * **都不起容器**。因此默认路径必须在无容器下跑绿并决定 exit code；
 * 真浏览器断言收进 --with-browser，未启用时输出 [SKIP]，**不影响 exit code**。
 * 把「环境不可达」报成「断言失败」会让 CI 永久红/绿失真，也会淹没真正的回归。
 *
 * ---------------------------------------------------------------------------
 * 变异自证（避免恒真式断言）
 * ---------------------------------------------------------------------------
 * 本测试的每个源码级断言都抽成**谓词函数**，变异用例把「变异后的源码」喂进同一谓词，
 * 断言它返回的问题列表**非空**。这样才真的证明「该断言能捕获回归」。
 *
 * 反例（**不要这样写**，它永远为真、证明不了任何事）：
 *   record('变异：删掉标签 -> 断言失败', !mutatedSource.includes(label))
 * 若源码里本来就没有该 label，replaceAll 是 no-op，!false === true 直接 PASS。
 */

import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_ROOT = path.join(REPO_ROOT, 'apps', 'web-user')
const VIEW_DIR = path.join(WEB_ROOT, 'src', 'views')
const CLEANING_DIR = path.join(VIEW_DIR, 'cleaning')

const KEYWORD_PANEL = path.join(CLEANING_DIR, 'CleaningKeywordPanel.tsx')
const RUN_PANEL = path.join(CLEANING_DIR, 'CleaningRunPanel.tsx')
const RULE_PANEL = path.join(CLEANING_DIR, 'CleaningRulePanel.tsx')
const CLEANING_VIEW = path.join(VIEW_DIR, 'CleaningView.tsx')

const WITH_BROWSER = process.argv.includes('--with-browser')
const BASE_URL = process.env.R20_BASE ?? 'http://127.0.0.1:18195'

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

const keywordSource = readFileSync(KEYWORD_PANEL, 'utf8')
const runSource = readFileSync(RUN_PANEL, 'utf8')
const ruleSource = readFileSync(RULE_PANEL, 'utf8')
const viewSource = readFileSync(CLEANING_VIEW, 'utf8')

// ---------------------------------------------------------------------------
// 谓词函数（源码文本 -> 问题列表）。变异用例复用同一谓词，保证断言非空转。
// ---------------------------------------------------------------------------

/**
 * #105 谓词：删除自定义关键词前必须先经过确认。
 *
 * 判据（结构性，不依赖精确文案）：
 *   1. 源码里出现 `Modal.confirm`（仓库既有的二次确认惯例，
 *      见 views/eval/DimensionManager.tsx 的 confirmDelete）；
 *   2. 确认弹窗里提到「不可撤销」类后果说明；
 *   3. 真正的删除调用（deleteCleaningKeyword）**不在** removeKeyword 的
 *      直连路径上，而是被确认弹窗包住 —— 用「deleteCleaningKeyword 出现在
 *      Modal.confirm 之后的 400 字符内」近似判定（渲染顺序上它属于 onOk 回调）。
 */
function problemsWithDeleteConfirm(source) {
  const problems = []
  if (!/Modal\.confirm\s*\(/.test(source)) {
    problems.push('删除自定义关键词没有走 Modal.confirm（缺少二次确认）')
  }
  if (!/不可撤销|无法恢复/.test(source)) {
    problems.push('确认弹窗没有说明后果（不可撤销/无法恢复）')
  }
  // 删除调用必须位于 Modal.confirm 之后的回调里
  const confirmIdx = source.indexOf('Modal.confirm(')
  const deleteIdx = source.indexOf('deleteCleaningKeyword')
  if (confirmIdx < 0 || deleteIdx < 0) {
    problems.push('找不到 Modal.confirm 或 deleteCleaningKeyword，断言失效')
  } else if (deleteIdx < confirmIdx) {
    problems.push('deleteCleaningKeyword 出现在 Modal.confirm 之前，说明删除仍是直连的')
  } else if (deleteIdx - confirmIdx > 2000) {
    problems.push('deleteCleaningKeyword 距离 Modal.confirm 过远，无法确认它在 onOk 回调内')
  }
  // 内置词必须仍被拦住，且提示「改为停用」这条替代路径
  if (!/isBuiltin[\s\S]{0,200}不可删除|不可删除[\s\S]{0,80}停用/.test(source)) {
    problems.push('内置关键词的「不可删除 + 可改为停用」提示缺失')
  }
  return problems
}

/**
 * 去掉源码里的注释，避免把「注释里提到的 <Banner>」当成真实用法。
 *
 * 为什么必须做：本修复的说明注释里就写着「原先用 <Banner>」——
 * 若不先剥注释，谓词会把这条注释判为「仍在使用 Banner」，导致永远 FAIL。
 * 这里只做保守的剥除（行注释 `//` 与块注释），不处理字符串字面量里的
 * 伪注释（本文件没有这种写法，且字符串里的 `<Banner` 也不构成渲染）。
 */
function stripComments(source) {
  // 防御：谓词可能被馈入「变异失败」的返回值。测试工具不应因入参类型而崩溃 ——
  // 崩溃会把「断言连跑都没跑完」误报成一个未分类的异常，掩盖真正的失败原因。
  if (typeof source !== 'string') return ''
  let out = ''
  let i = 0
  while (i < source.length) {
    const two = source.slice(i, i + 2)
    if (two === '//') {
      const nl = source.indexOf('\n', i)
      i = nl < 0 ? source.length : nl
      continue
    }
    if (two === '/*') {
      const end = source.indexOf('*/', i + 2)
      i = end < 0 ? source.length : end + 2
      continue
    }
    out += source[i]
    i += 1
  }
  return out
}

/**
 * #106 谓词：清洗页初始加载不得产生任何 role=alert 的告警。
 *
 * 判据（这是本 issue 的**根因判据**，不是「文案是否出现」）：
 *   1. 清洗页相关文件里不得再用 `<Banner>` —— Semi 的 Banner 会无条件渲染
 *      role="alert"，无论语义上是不是告警。空状态应当用 Empty 就地展示。
 *   2. 「还没有规则」这类**空状态**文案必须出现在 Empty 里，而不是 Banner/Toast 里。
 *   3. 空状态必须带一个**可点击的下一步**（而不是用文字描述一个已在页面上的按钮）。
 */
function problemsWithNoStrayAlert(source) {
  const problems = []
  const code = stripComments(source)
  const bannerUses = code.match(/<Banner\b/g) ?? []
  if (bannerUses.length > 0) {
    problems.push(
      `仍在使用 <Banner> ×${bannerUses.length}：Semi 的 Banner 无条件渲染 role="alert"，` +
      '页面加载时会被当成实时告警（无障碍工具与自动化巡检都会这样识别）',
    )
  }
  if (!/还没有清洗规则/.test(code)) {
    problems.push('找不到「还没有清洗规则」空状态文案')
  }
  if (!/<Empty\b/.test(code)) {
    problems.push('空状态没有用 <Empty> 就地展示')
  }
  if (!/onRequestCreateRule/.test(code)) {
    problems.push('空状态没有提供可点击的下一步（onRequestCreateRule）')
  }
  return problems
}

/**
 * #106 关联谓词：跨面板触发「新建规则」必须用**递增计数器**而不是布尔 prop。
 *
 * 理由：用户可能在空状态里点两次「去新建清洗规则」，第二次也必须生效。
 * 布尔 prop 在 true→true 时不触发 effect，会静默失效。
 */
function problemsWithCreateRuleSignal(view, rule) {
  const problems = []
  if (!/createRuleSignal/.test(view)) {
    problems.push('CleaningView 没有把「新建规则」请求传给 CleaningRulePanel')
  }
  if (!/setCreateRuleSignal\s*\(\s*\(?\s*current\s*\)?\s*=>\s*current\s*\+\s*1/.test(view)) {
    problems.push('父组件没有用递增计数器触发（布尔 prop 无法反复触发同一动作）')
  }
  if (!/createRequestSignal\s*[:?]/.test(rule)) {
    problems.push('CleaningRulePanel 没有接收 createRequestSignal')
  }
  if (!/lastSignal/.test(rule) || !/useRef/.test(rule)) {
    problems.push('CleaningRulePanel 没有跳过首渲染，页面一加载就会弹出新建弹窗')
  }
  return problems
}

// ---------------------------------------------------------------------------
// 1. 断言（对真实源码求值）
// ---------------------------------------------------------------------------

const deleteProblems = problemsWithDeleteConfirm(keywordSource)
record('#105 删除自定义关键词走二次确认（Modal.confirm + 后果说明 + 替代做法）',
  deleteProblems.length === 0,
  deleteProblems.length === 0 ? '结构完整' : deleteProblems.join('; '))

const alertProblems = problemsWithNoStrayAlert(runSource)
record('#106 清洗页不再产生 role=alert 的意外告警（空状态改用 Empty 就地展示）',
  alertProblems.length === 0,
  alertProblems.length === 0 ? '无 Banner、空状态用 Empty 且带可点击下一步' : alertProblems.join('; '))

const signalProblems = problemsWithCreateRuleSignal(viewSource, ruleSource)
record('#106 跨面板「新建规则」用递增计数器触发且跳过首渲染',
  signalProblems.length === 0,
  signalProblems.length === 0 ? '父传递增信号、子用 useRef 跳过首渲染' : signalProblems.join('; '))

// 穷举检查：清洗模块其他文件不应残留同类「加载即告警」
const otherBannerFiles = [
  [path.join(CLEANING_DIR, 'CleaningReportPanel.tsx'), 'CleaningReportPanel'],
  [path.join(CLEANING_DIR, 'CleaningFlowSteps.tsx'), 'CleaningFlowSteps'],
  [CLEANING_VIEW, 'CleaningView'],
]
for (const [file, name] of otherBannerFiles) {
  const src = readFileSync(file, 'utf8')
  // 报告页与父页的 Banner 是**用户操作后**出现的（有 report / 有已完成 run 才渲染），
  // 不是加载即告警；这里只断言它们不是「无条件渲染」的形态。
  const unconditional = /^\s*<Banner\b/m.test(src) && !/(\?|\&\&)\s*\(?\s*<Banner/.test(src)
  record(`${name} 的 Banner（如有）不是无条件渲染`,
    !unconditional,
    unconditional ? '存在无条件渲染的 Banner（加载即告警）' : 'Banner 均在条件分支内或未使用')
}

// ---------------------------------------------------------------------------
// 2. 变异自证（谓词必须能捕获「退回原状」）
// ---------------------------------------------------------------------------

// 变异 1：把删除改回直连（删掉 Modal.confirm 包裹）
const mutatedDelete = keywordSource
  .replace(/Modal\.confirm\(\{[\s\S]*?\n\s*\}\)\n\s*\}/, '')
if (mutatedDelete === keywordSource) {
  record('变异 1 生效性检查', false, '未能构造「删掉 Modal.confirm」的变异（检查锚点）')
} else {
  const mp = problemsWithDeleteConfirm(mutatedDelete)
  record('变异 1：删掉 Modal.confirm -> #105 谓词必须报问题',
    mp.length > 0, mp.join('; ') || '未被捕获（断言空转）')
}

// 变异 2：把 Empty 空状态换回 Banner（复现修复前的形态）
// 锚点用「<Empty…description={…}/>」的完整多行块；用 indexOf 定位后按括号配平截取，
// 比写一条长正则更不容易随无关格式变更而静默失效。
function mutateEmptyToBanner(source) {
  const start = source.indexOf('                <Empty\n')
  // 锚点不存在时返回原文（而不是 null）：调用方靠 `mutated === source` 判定
  // 「变异未生效」并如实报 FAIL。返回 null 会让谓词拿到非字符串而崩溃。
  if (start < 0) return source
  const end = source.indexOf('/>', start)
  if (end < 0) return source
  const replacement =
    '                <Banner type="info" closeIcon={null} ' +
    'description="还没有清洗规则。可以先到上方「清洗规则」新建一条，再回来发起清洗。" />'
  return source.slice(0, start) + replacement + source.slice(end + 2)
}

const mutatedAlert = mutateEmptyToBanner(runSource)
if (mutatedAlert === runSource) {
  record('变异 2 生效性检查', false, '未能构造「换回 Banner」的变异（检查锚点）')
} else {
  const mp = problemsWithNoStrayAlert(mutatedAlert)
  record('变异 2：把空状态换回 Banner -> #106 谓词必须报问题',
    mp.length > 0, mp.join('; ') || '未被捕获（断言空转）')
}

// 变异 3：把递增计数器换回布尔 prop
const mutatedSignal = viewSource.replace(
  /onRequestCreateRule=\{\(\) => setCreateRuleSignal\(\(current\) => current \+ 1\)\}/,
  'onRequestCreateRule={() => undefined}',
)
if (mutatedSignal === viewSource) {
  record('变异 3 生效性检查', false, '未能构造「换掉递增计数器」的变异（检查锚点）')
} else {
  const mp = problemsWithCreateRuleSignal(mutatedSignal, ruleSource)
  record('变异 3：去掉递增计数器 -> 关联谓词必须报问题',
    mp.length > 0, mp.join('; ') || '未被捕获（断言空转）')
}

// ---------------------------------------------------------------------------
// 3. 真浏览器（--with-browser）：复现修复前/修复后两条路径
// ---------------------------------------------------------------------------

if (WITH_BROWSER) {
  const require = createRequire(path.join(WEB_ROOT, 'package.json'))
  let chromium
  try {
    ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
  } catch (error) {
    record('加载 playwright', false, `缺 playwright：${error?.message ?? error}`)
  }

  if (chromium) {
    const browser = await chromium.launch({ headless: true })
    try {
      const page = await browser.newPage()
      // 收集任何时刻出现的 toast 与 role=alert（含瞬时）
      await page.addInitScript(() => {
        window.__seenToast = []
        window.__seenAlert = []
        const scan = () => {
          document.querySelectorAll('.semi-toast-content, .semi-notification-content').forEach((el) => {
            const t = (el.textContent || '').trim()
            if (t && !window.__seenToast.includes(t)) window.__seenToast.push(t)
          })
          document.querySelectorAll('[role=alert]').forEach((el) => {
            const t = (el.textContent || '').trim()
            if (t && !window.__seenAlert.includes(t)) window.__seenAlert.push(t)
          })
        }
        new MutationObserver(scan).observe(document.documentElement, { childList: true, subtree: true })
        setInterval(scan, 100)
      })

      await page.goto(`${BASE_URL}/login`)
      await page.locator('input').first().fill('admin@company.com')
      await page.locator('input[type=password]').fill('admin123456')
      await page.locator('button[type=submit], .semi-button-primary').first().click()
      await page.waitForURL(/console/, { timeout: 20_000 })

      // ---- #106：打开清洗页不做任何点击 ----
      await page.goto(`${BASE_URL}/console/cleaning`)
      await page.waitForTimeout(5_000)
      const toasts = await page.evaluate(() => window.__seenToast)
      const alerts = await page.evaluate(() => window.__seenAlert)
      record('#106 打开清洗页不弹任何 toast', toasts.length === 0, JSON.stringify(toasts))
      record('#106 打开清洗页没有 role=alert 告警', alerts.length === 0, JSON.stringify(alerts))

      const runCard = page.locator('.console-panel').filter({ hasText: '发起一次清洗' }).first()
      record('#106 空状态就地展示在「发起一次清洗」面板内',
        (await runCard.locator('.console-empty').count()) > 0, '面板内存在 .console-empty')

      // 跨面板直达：点「去新建清洗规则」应打开弹窗，且可重复触发
      await runCard.getByRole('button', { name: '去新建清洗规则' }).click()
      await page.waitForTimeout(1_000)
      const modal1 = await page.locator('.semi-modal').count()
      await page.locator('.semi-modal button:visible', { hasText: '取消' }).last().click().catch(() => {})
      await page.waitForTimeout(600)
      await runCard.getByRole('button', { name: '去新建清洗规则' }).click()
      await page.waitForTimeout(1_000)
      const modal2 = await page.locator('.semi-modal').count()
      record('#106 「去新建清洗规则」可重复触发（递增计数器语义）',
        modal1 > 0 && modal2 > 0, `第一次 modal=${modal1}，第二次 modal=${modal2}`)
      await page.locator('.semi-modal button:visible', { hasText: '取消' }).last().click().catch(() => {})
      await page.waitForTimeout(500)

      // ---- #105：新建自定义词 -> 点删除必须先出确认 ----
      const uniq = `l15-r20-${process.pid}-${Date.now()}`
      await page.getByRole('button', { name: '新增关键词' }).click()
      await page.waitForTimeout(800)
      await page.locator('.semi-modal input:visible').first().fill(uniq)
      await page.locator('button:visible', { hasText: '保存' }).last().click()
      await page.waitForTimeout(2_500)

      const findRow = async () => {
        const rows = page.locator('.semi-table-tbody .semi-table-row')
        const n = await rows.count()
        for (let i = 0; i < n; i++) {
          const r = rows.nth(i)
          if ((await r.locator('td').first().innerText().catch(() => '')).includes(uniq)) return r
        }
        return null
      }

      const created = await findRow()
      record('#105 前置：自定义关键词创建成功', created !== null, `pattern=${uniq}`)

      if (created) {
        await created.getByRole('button', { name: '删除' }).click()
        await page.waitForTimeout(1_500)
        const confirmTitle = await page.locator('.semi-modal-title').innerText().catch(() => '')
        const confirmBody = await page.locator('.semi-modal-body').innerText().catch(() => '')
        record('#105 点删除先出确认弹窗', confirmTitle.includes('删除'), `标题=${confirmTitle}`)
        record('#105 确认弹窗说明后果与替代做法',
          /不可撤销|无法恢复/.test(confirmBody) && /停用/.test(confirmBody),
          confirmBody.replace(/\n/g, ' | ').slice(0, 120))
        record('#105 点「确定」之前该词仍在', (await findRow()) !== null, '尚未删除 ✓')

        // 取消不删除
        await page.locator('.semi-modal button:visible', { hasText: '取消' }).last().click()
        await page.waitForTimeout(1_200)
        record('#105 点「取消」不删除该词', (await findRow()) !== null, '取消后仍存在 ✓')

        // 确定才删除
        await (await findRow()).getByRole('button', { name: '删除' }).click()
        await page.waitForTimeout(1_200)
        await page.locator('.semi-modal button:visible', { hasText: '删除' }).last().click()
        await page.waitForTimeout(2_500)
        record('#105 点「确定」才真的删除', (await findRow()) === null, '确定后已删除 ✓')
      }
    } catch (error) {
      record('真浏览器断言', false, String(error?.message ?? error))
    } finally {
      await browser.close()
    }
  }
} else {
  recordSkip('真浏览器：#105 二次确认 / #106 无意外告警',
    '未启用 --with-browser（默认路径不需要浏览器）')
}

// ---------------------------------------------------------------------------
// 汇总
// ---------------------------------------------------------------------------

console.log('')
const skipped = results.filter((r) => r.skipped).length
if (failures.length > 0) {
  console.error(`CLEANING UX FAILED: ${failures.length}/${results.length} 项未通过 -> ${failures.join(', ')}`)
  process.exitCode = 1
} else {
  console.log(`CLEANING UX OK: ${results.length - skipped}/${results.length - skipped} 项通过` +
    (skipped ? `（另 ${skipped} 项因输入缺失跳过）` : ''))
}
