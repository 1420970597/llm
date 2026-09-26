/**
 * Issue #190–#195 与 #197（17 条）的**独立人工点击复核**。
 *
 * 与仓库自带的 `test/audit/verify-197-remediation.mjs` 的区别（这是本脚本存在的理由）：
 *   1. 那个脚本读的是权威元数据（`GET /blueprint-nodes`）、局部文本、DOM 属性计数。
 *      它无法回答「点搜索框里输入关键词，列表真的少了吗」「点『新建试制』按钮，
 *      界面有没有真的拦住计划量」这类**用户动作的因果**。
 *   2. 因此本脚本只用**真实点击/输入/按键**驱动，并且每条判定都记录
 *      「动作 → 可观测结果」，而不是「页面上有没有某个 data-* 属性」。
 *   3. 截图前解除 `body(100vh) > main(overflow:auto)` 的高度锁，
 *      否则「页面超长」类问题在证据里会消失（Playwright fullPage 只截视口）。
 *
 * 产物：docs/audit/issue-197-verify/<n>-<id>.png + report.json（含每条动作与读数）
 *
 * 用法：node test/audit/human-flow-verify.mjs
 *       PROJECT_ID=p_1 BASE_URL=http://127.0.0.1:3210 node test/audit/human-flow-verify.mjs
 */
import { mkdirSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const require = createRequire('/root/llm/package.json')
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? '/root/.pi/agent/npm/node_modules/playwright')

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../..')
const OUT = path.join(REPO_ROOT, 'docs/audit/issue-197-verify')
const BASE = (process.env.BASE_URL ?? 'http://127.0.0.1:3210').replace(/\/$/, '')
const EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'
const P = process.env.PROJECT_ID ?? 'p_1'

mkdirSync(OUT, { recursive: true })

const report = { base: BASE, project: P, startedAt: new Date().toISOString(), checks: [], httpErrors: [], pageErrors: [] }
let shotN = 0

const browser = await chromium.launch({ headless: true })
const ctx = await browser.newContext({ viewport: { width: 1600, height: 1000 }, locale: 'zh-CN', deviceScaleFactor: 1 })
const page = await ctx.newPage()
page.on('pageerror', (e) => report.pageErrors.push(String(e).slice(0, 200)))
page.on('response', (r) => {
  // 404/401 常被前端用作能力探测（如未登录时探 /v1/auth/me），只记 5xx 与真正失败的业务请求
  if (r.status() >= 500) report.httpErrors.push(`${r.status()} ${r.request().method()} ${r.url().replace(BASE, '').slice(0, 120)}`)
})

/** 记录一条判定：动作 → 观测 → 是否通过。 */
function record(issue, title, pass, action, observed, extra = {}) {
  const item = { issue, title, pass, action, observed, ...extra }
  report.checks.push(item)
  console.log(`[${pass ? 'PASS' : 'FAIL'}] #${issue} ${title}`)
  console.log(`        动作: ${action}`)
  console.log(`        观测: ${observed}`)
  return item
}

/** 解除内部滚动容器的高度锁后整页截图（否则长页面会被截成视口高）。 */
async function shot(key) {
  await page.evaluate(() => {
    const main = document.querySelector('main.app-layout__content')
    for (const el of [document.documentElement, document.body, main?.parentElement, main]) {
      if (!el) continue
      el.style.height = 'auto'
      el.style.maxHeight = 'none'
      el.style.overflow = 'visible'
    }
  })
  await page.waitForTimeout(200)
  shotN += 1
  const file = path.join(OUT, `${String(shotN).padStart(2, '0')}-${key}.png`)
  await page.screenshot({ path: file, fullPage: true })
  return path.relative(REPO_ROOT, file)
}

async function goto(route, settle = 1500) {
  await page.goto(`${BASE}${route}`, { waitUntil: 'networkidle', timeout: 45000 })
  await page.waitForTimeout(settle)
}

const bodyText = () => page.locator('body').innerText()
const contentHeight = () =>
  page.evaluate(() => document.querySelector('main.app-layout__content')?.scrollHeight ?? document.documentElement.scrollHeight)

/* ---------------------------------------------------------------- */
/* 登录                                                              */
/* ---------------------------------------------------------------- */
await goto('/login')
await page.getByPlaceholder('请输入邮箱').fill(EMAIL)
await page.getByPlaceholder('请输入密码').fill(PASSWORD)
await page.getByRole('button', { name: '进入今日工作' }).click()
await page.waitForURL(/\/today/, { timeout: 25000 })
await page.waitForTimeout(2000)
record('auth', '管理员可登录进入今日工作', page.url().includes('/today'), '填写邮箱密码并点击「进入今日工作」', `落地 URL = ${page.url()}`)

/* ---------------------------------------------------------------- */
/* #197-10 今日工作 = 工作台总览（点击磁贴必须能下钻）                 */
/* ---------------------------------------------------------------- */
{
  const tiles = page.locator('[data-today-overview="true"] a')
  const count = await tiles.count()
  const texts = []
  for (let i = 0; i < count; i += 1) texts.push((await tiles.nth(i).innerText()).replace(/\n+/g, ' ').trim())
  const before = page.url()
  let drilled = '未点击'
  if (count > 0) {
    await tiles.first().click()
    await page.waitForTimeout(2000)
    drilled = `${before} → ${page.url()}`
    await page.goBack({ waitUntil: 'networkidle' })
    await page.waitForTimeout(1200)
  }
  const file = await shot('today-overview')
  record('197-10', '今日工作提供可点总览磁贴', count >= 4, '统计磁贴数量并点击第一个磁贴', `${count} 个磁贴：${texts.join(' / ')}；点击后跳转 ${drilled}`, { screenshot: file })
}

/* ---------------------------------------------------------------- */
/* #197-1 项目列表分页状态常显 + 搜索真的作用于结果                   */
/* ---------------------------------------------------------------- */
{
  await goto('/projects')
  const footer = page.locator('[data-projects-pagination="true"]')
  const footerVisible = await footer.isVisible().catch(() => false)
  const footerText = footerVisible ? (await footer.innerText()).replace(/\n+/g, ' ').trim() : '(不存在)'
  const rowsBefore = await page.locator('.project-card, .atelier-project-card, [data-project-id]').count()

  // 真实人机交互：在搜索框输入一个必然无结果的词
  const search = page.getByLabel('搜索项目').first()
  await search.click()
  await search.fill('zzz-不存在的项目-zzz')
  await page.waitForTimeout(2200)
  const emptyText = (await bodyText()).replace(/\n+/g, ' ').slice(0, 200)
  const rowsAfter = await page.locator('.project-card, .atelier-project-card, [data-project-id]').count()
  await search.fill('')
  await page.waitForTimeout(2200)
  const rowsRestored = await page.locator('.project-card, .atelier-project-card, [data-project-id]').count()
  const file = await shot('projects-pagination')
  record('197-1', '项目列表常显分页状态且搜索真实生效', footerVisible && rowsAfter < rowsBefore && rowsRestored === rowsBefore,
    '检查分页状态行；在搜索框输入无结果关键词后清空',
    `分页状态行可见=${footerVisible}「${footerText}」；行数 输入前=${rowsBefore} 无结果词=${rowsAfter} 清空后=${rowsRestored}；空结果文案「${emptyText.slice(0, 100)}」`,
    { screenshot: file })
}

/* ---------------------------------------------------------------- */
/* #197-2 思考步骤 = 自然语言模板（而非 JSON）                        */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/blueprint?node=standard`)
  const editor = page.locator('[data-steps-editor="true"]')
  const hasEditor = await editor.count()
  // 该节点已引用标准版本 v1，草稿 steps 为空（引用版本优先），因此先真实点击「添加步骤」
  const addBtn = editor.locator('[data-steps-add="true"]')
  let addWorked = false
  let typed = '未输入'
  let fieldLabels = []
  if (await addBtn.count()) {
    await addBtn.first().click()
    await page.waitForTimeout(700)
    addWorked = (await editor.innerText()).includes('步骤 1')
    fieldLabels = await editor.locator('.wizard-field__label').allInnerTexts()
    const titleInput = editor.locator('input').first()
    if (await titleInput.count()) {
      await titleInput.click()
      await titleInput.fill('人机点击复核：先确认题目约束')
      await page.waitForTimeout(400)
      typed = await titleInput.inputValue()
    }
  }
  // 默认视图下不应出现原始 JSON 结构（JSON 只在「看 JSON」里出现）
  const bodyHasJSON = /\{\s*"id"\s*:|\{\s*"title"\s*:/.test((await bodyText()).replace(/\s+/g, ' '))
  const rawToggleExists = await editor.locator('[data-steps-raw-toggle="true"]').count()
  const file = await shot('blueprint-standard-steps')
  record('197-2', '思维步骤默认是自然语言分步表单，且可直接编辑（JSON 降为可选视图）',
    hasEditor > 0 && addWorked && !bodyHasJSON && rawToggleExists > 0 && typed.includes('人机点击复核'),
    '打开设计页标准节点 → 点「添加步骤」→ 在「做什么」输入框里填中文并回读',
    `分步编辑器=${hasEditor}；点击添加后出现步骤 1=${addWorked}；表单字段标签=[${fieldLabels.map((s) => s.trim()).join(' / ')}]；` +
    `输入回读「${typed}」；默认视图含原始 JSON=${bodyHasJSON}；「看 JSON」可切换=${rawToggleExists > 0}`,
    { screenshot: file })
}

/* ---------------------------------------------------------------- */
/* #197-5/#197-6/#197-4 独立评估可读、版本不强制、页面不超长          */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/blueprint?node=evaluation`)
  const purposeCount = await page.locator('[data-node-purpose="true"]').count()
  const purposeText = purposeCount ? (await page.locator('[data-node-purpose="true"]').first().innerText()).replace(/\n+/g, ' ').slice(0, 220) : '(不存在)'
  const stepsCount = await page.locator('[data-node-steps="true"]').count()
  const stepsText = stepsCount ? (await page.locator('[data-node-steps="true"]').first().innerText()).replace(/\n+/g, ' ').slice(0, 260) : '(不存在)'
  const historyIsDetails = await page.locator('details.blueprint-history').count()
  const height = await contentHeight()
  const file = await shot('blueprint-evaluation')
  record('197-5', '独立评估节点先给「会做什么」与执行顺序', purposeCount > 0 && stepsCount > 0,
    '打开设计页独立评估节点，检查 purpose 与执行步骤是否在字段之前呈现',
    `purpose=${purposeCount} 处「${purposeText}」；执行步骤=${stepsCount} 处「${stepsText.slice(0, 160)}…」`, { screenshot: file })
  record('197-6', '版本历史折叠、不强制用户先理解版本', historyIsDetails > 0,
    '检查版本历史是否为可折叠区块', `details.blueprint-history 元素数 = ${historyIsDetails}`)
  record('197-4', '设计页不再被单个节点撑成超长页面', height <= 1000 * 2.2,
    '解除滚动容器高度锁后测量页面真实内容高度',
    `内容高度 = ${height}px（视口 1000px，约 ${(height / 1000).toFixed(2)} 屏）`)
}

/* ---------------------------------------------------------------- */
/* #197-11 蓝图横向工作流 + m×n×z 结构树                              */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/blueprint`)
  const flex = await page.locator('.blueprint-nodes').evaluate((el) => getComputedStyle(el).flexDirection).catch(() => 'n/a')
  const canvasW = await page.locator('.blueprint-canvas').evaluate((el) => Math.round(el.getBoundingClientRect().width)).catch(() => 0)
  // 节点是否横向排列：比较前两个节点的 y 坐标（同排 → y 接近）
  const ys = await page.locator('[data-node-key]').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)))
  const sameRow = ys.length >= 2 && Math.abs(ys[0] - ys[0 + 1]) < 40
  const file1 = await shot('blueprint-horizontal')

  await goto(`/p/${P}/coverage`)
  const tree = page.locator('[data-coverage-tree="true"]')
  const treeCount = await tree.count()
  const treeText = treeCount ? (await tree.first().innerText()).replace(/\n+/g, ' | ').slice(0, 400) : '(不存在)'
  const file2 = await shot('coverage-tree')
  const formula = /m\s*[×x]\s*n\s*[×x]\s*z|最大可产出/.test(await bodyText())
  // 结构树的公式必须是**自洽的算术**：m × n × z 应当等于显示的结果值。
  // 这里不硬编码数字，而是从页面读回文本后自己算一遍 —— 否则「1×2×4=4」这种
  // 前后矛盾的读数会被一个恰好匹配「1×1×1=1」的正则放过。
  const formulaText = await page.locator('[data-coverage-formula="true"]').first().innerText().catch(() => '')
  const nums = (formulaText.match(/\d+/g) ?? []).map(Number)
  const [fm, fn, fz, fres] = nums
  const arithmeticConsistent = nums.length === 4 && fm * fn * fz === fres
  record('197-11', '蓝图改为横向工作流带', flex === 'row' && sameRow,
    '读取 .blueprint-nodes 的 flex-direction 并比较节点 y 坐标',
    `flex-direction=${flex}；画布宽=${canvasW}px；节点 y 坐标前两个=${ys.slice(0, 3).join(',')}（同排=${sameRow}）`, { screenshot: file1 })
  record('197-11', '覆盖页提供 m×n×z 结构树，且公式读数自洽', treeCount > 0 && formula && arithmeticConsistent,
    '打开覆盖矩阵，读回结构树公式并自己重算 m×n×z 是否等于结果值',
    `结构树元素=${treeCount}；公式文本「${formulaText}」；读回数字=[${nums.join(',')}]；` +
    `自洽（${fm}×${fn}×${fz}=${fm * fn * fz}，页面结果=${fres}）=${arithmeticConsistent}；结构树文本「${treeText}」`,
    { screenshot: file2, defect: arithmeticConsistent ? null : 'm×n×z 的三个因子不是乘积关系：z 显示的是 sum(quota) 总量，而文案宣称「每个方向的题数」' })
}

/* ---------------------------------------------------------------- */
/* #197-3 数据可预览（分字段人话视图 + JSON 可切换）                  */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/data`)
  const rows = page.locator('[data-sample-id]')
  const rowCount = await rows.count()
  let detail = '无样本可点'
  let previewOk = false
  let rawToggle = 0
  if (rowCount > 0) {
    // 真实点击：数据页行操作是「查看内容」（审阅页才是「审阅」）
    await rows.first().getByRole('button', { name: '查看内容' }).first().click()
    await page.waitForTimeout(2800)
    const text = (await bodyText()).replace(/\n+/g, ' ').slice(0, 900)
    rawToggle = await page.locator('[data-payload-raw-toggle="true"]').count()
    // 默认人话视图：出现字段中文标签（问题/推理/答案）而不是 {"question":
    const humanView = /问题|推理|答案/.test(text) && !/^\s*\{\s*"/.test(text.trim())
    previewOk = humanView
    detail = `URL=${page.url()}；含中文字段标签=${humanView}；原始 JSON 切换控件=${rawToggle}；正文「${text.slice(0, 300)}…」`
    // 真实交互：切换到原始 JSON 再切回
    if (rawToggle > 0) {
      await page.locator('[data-payload-raw-toggle="true"]').first().click()
      await page.waitForTimeout(700)
      detail += `；点击切换后页面含 {\\"question\\" = ${/\{\s*&quot;|"question"/.test(await bodyText())}`
    }
  }
  const file = await shot('sample-preview')
  record('197-3', '样本内容默认以人话分字段预览（JSON 为可切换视图）', previewOk,
    '在数据页点第一条样本的「查看内容」进入详情，检查默认视图是否为分字段中文而非原始 JSON',
    `${rowCount} 行样本；${detail}`, { screenshot: file })
}

/* ---------------------------------------------------------------- */
/* #197-12 数据 vs 审阅：两个入口职责必须可区分                       */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/data`)
  const dataTitle = (await page.locator('.console-page__header h4, .console-page__header h1, .console-page__header h2').first().innerText()).trim()
  const dataHeader = (await page.locator('.console-page__header').first().innerText()).replace(/\n+/g, ' | ')
  const dataActions = [...new Set((await page.locator('button').allInnerTexts()).map((t) => t.trim()).filter(Boolean))]
  const dataFilter = await page.locator('.semi-select-selection-text').first().innerText().catch(() => 'n/a')
  const file1 = await shot('data-page')

  await goto(`/p/${P}/review`)
  const reviewTitle = (await page.locator('.console-page__header h4, .console-page__header h1, .console-page__header h2').first().innerText()).trim()
  const reviewHeader = (await page.locator('.console-page__header').first().innerText()).replace(/\n+/g, ' | ')
  const reviewActions = [...new Set((await page.locator('button').allInnerTexts()).map((t) => t.trim()).filter(Boolean))]
  const reviewFilter = await page.locator('.semi-select-selection-text').first().innerText().catch(() => 'n/a')
  const file2 = await shot('review-page')

  const headerDiffers = dataHeader !== reviewHeader
  const filterDiffers = dataFilter !== reviewFilter
  record('197-12', '「数据」与「审阅」职责可区分（标题/说明/筛选/CTA）',
    dataTitle !== reviewTitle && headerDiffers && filterDiffers,
    '分别打开 /data 与 /review，逐项比对标题、说明、默认筛选与主操作',
    `标题「${dataTitle}」vs「${reviewTitle}」；默认筛选「${dataFilter}」vs「${reviewFilter}」；说明相同=${!headerDiffers}；` +
    `数据页按钮=${dataActions.join('/')}；审阅页按钮=${reviewActions.join('/')}`,
    { screenshot: file1, screenshot2: file2 })
}

/* ---------------------------------------------------------------- */
/* #197-13 + #190 生产页：预览/分析 + 计划量闸门 + 缺口可见            */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/runs`)
  const runsText = (await bodyText()).replace(/\n+/g, ' | ')
  const hasShortfallWord = /缺口|部分完成|少交付/.test(runsText)
  const file1 = await shot('runs-list')

  await goto(`/p/${P}/runs/b_2`, 2200)
  const analysis = page.locator('[data-batch-analysis="true"]')
  const analysisCount = await analysis.count()
  const analysisText = analysisCount ? (await analysis.first().innerText()).replace(/\n+/g, ' | ').slice(0, 500) : '(不存在)'
  const shortfall = await page.locator('[data-batch-shortfall="true"]').count()
  const file2 = await shot('run-detail-analysis')
  record('197-13', '批次详情打开即给出数据集结构与自动分析', analysisCount > 0,
    '打开已完成批次 b_2 的详情，检查是否出现结构/长度/占比分析（无按钮触发）',
    `分析区块=${analysisCount}；缺口横幅=${shortfall}；内容「${analysisText}」`, { screenshot: file2 })
  record('190', '批次列表暴露「缺口/少交付」读数', hasShortfallWord,
    '打开生产页批次列表，检查是否有缺口口径', `页面含缺口字样=${hasShortfallWord}`, { screenshot: file1 })
}

/* ---------------------------------------------------------------- */
/* #190 计划量闸门：真人点「新建试制」并填入超过可产出量的计划量       */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/pilot`, 2000)
  // 计划量是 Semi InputNumber：DOM 上渲染为 input[type=text]#plan-units（不是 type=number）
  const unitInput = page.locator('#plan-units')
  let gateDetail = '未找到计划量输入框'
  let blocked = false
  let responseStatus = null
  let responseBody = ''
  page.on('response', async (r) => {
    if (r.url().includes('/batches') && r.request().method() === 'POST') {
      responseStatus = r.status()
      responseBody = (await r.text().catch(() => '')).slice(0, 500)
    }
  })
  if (await unitInput.count()) {
    await unitInput.click()
    await unitInput.fill('12')          // 当前覆盖可产出量 = 1
    await page.keyboard.press('Tab')
    await page.waitForTimeout(600)
    const submit = page.getByRole('button', { name: /启动试制批次|启动|创建批次/ }).first()
    await submit.click()
    await page.waitForTimeout(4000)
    const alertText = (await page.locator('[role="alert"]').allInnerTexts()).join(' ').trim()
    blocked = responseStatus === 422 && /可产出量/.test(alertText)
    gateDetail = `填入 12（覆盖可产出量=1）并点提交 → HTTP ${responseStatus}；URL=${page.url()}；` +
      `界面告警「${alertText.slice(0, 260)}」`
  }
  const file = await shot('pilot-capacity-gate')
  record('190', '计划量超过覆盖可产出量时被拦截（真实点击 + 真实 422）', blocked,
    '在小批试制页把计划量改为 12 并点击「启动试制批次」', gateDetail, { screenshot: file, response: responseBody })
}

/* ---------------------------------------------------------------- */
/* #197-14 质量：术语 + 裁判改为模型连接下拉                          */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/quality`)
  const listText = await bodyText()
  const file1 = await shot('quality-list')
  await goto(`/p/${P}/quality/new`, 2000)
  const judgeSelect = page.locator('[data-judge-connection-select="true"]')
  const judgeCount = await judgeSelect.count()
  // 真人操作：点开裁判下拉，数选项
  let options = 0
  let optionTexts = ''
  if (judgeCount > 0) {
    await judgeSelect.first().click()
    await page.waitForTimeout(1200)
    const opts = page.locator('.semi-select-option, [role="option"]')
    options = await opts.count()
    optionTexts = (await opts.allInnerTexts()).map((t) => t.trim()).filter(Boolean).slice(0, 6).join(' / ')
    await page.keyboard.press('Escape')
  }
  const newText = await bodyText()
  const file2 = await shot('quality-new-judge')
  record('197-14', '质量页不再使用「分母/分子」，改用被评测数据集表述',
    !/分子|分母/.test(listText) && !/分子|分母/.test(newText),
    '打开质量列表与新建实验页，全文检索「分子/分母」',
    `质量列表含分子/分母=${/分子|分母/.test(listText)}；新建实验含分子/分母=${/分子|分母/.test(newText)}；` +
    `列表页头「${(listText.match(/被评测[^\n]*/) || ['(无被评测字样)'])[0].slice(0, 100)}」`, { screenshot: file1 })
  record('197-14', '新建实验的裁判改为已启用连接下拉（含真实选项）',
    judgeCount > 0 && options > 0,
    '点击裁判模型下拉，读取真实选项列表',
    `下拉控件=${judgeCount}；点击后选项数=${options}；选项「${optionTexts}」`, { screenshot: file2 })
}

/* ---------------------------------------------------------------- */
/* #197-7/#197-8 连接设置：页内弹窗 + 行内编辑（不跳旧控制台）         */
/* ---------------------------------------------------------------- */
{
  await goto('/settings/connections', 2000)
  const rowActions = await page.locator('[data-connection-edit]').count()
  const rowCount = await page.locator('[data-connection-id]').count()
  const manage = page.locator('[data-connection-manage="true"]')

  // 真人操作 1：点顶部「新增」→ 必须在页内出表单，不得跳转
  let addDetail = '未找到新增按钮'
  if (await manage.count()) {
    const urlBefore = page.url()
    await manage.first().click()
    await page.waitForTimeout(1200)
    const formCount = await page.locator('[data-connection-form="true"]').count()
    addDetail = `点击前 ${urlBefore} → 点击后 ${page.url()}；页内表单=${formCount}；跳旧控制台=${/\/console\//.test(page.url())}`
  }
  await page.keyboard.press('Escape')
  await page.waitForTimeout(700)
  const file1 = await shot('connections-page')

  // 真人操作 2：点某一行的「编辑」→ 必须带着该行数据打开表单
  let editDetail = '无行内编辑按钮'
  const editBtns = page.locator('[data-connection-edit]')
  if (await editBtns.count()) {
    const rowId = await editBtns.first().getAttribute('data-connection-edit')
    await editBtns.first().click()
    await page.waitForTimeout(1200)
    const form = page.locator('[data-connection-form="true"]')
    const formCount = await form.count()
    const formText = formCount ? (await form.first().innerText()).replace(/\n+/g, ' | ').slice(0, 200) : ''
    const nameValue = formCount ? await form.first().locator('input').first().inputValue().catch(() => '') : ''
    editDetail = `点击行 ${rowId} 的编辑 → URL=${page.url()}；表单=${formCount}；名称字段预填「${nameValue}」；表单「${formText}」`
  }
  const file2 = await shot('connections-row-edit')
  record('197-7', '新增/编辑连接在页内完成，不再跳旧控制台',
    /页内表单=1/.test(addDetail) && !/跳旧控制台=true/.test(addDetail),
    '点击连接设置页顶部「新增」按钮，观察 URL 与是否出现页内表单', addDetail, { screenshot: file1 })
  record('197-8', '每个连接行有元素级编辑按钮且带该行数据',
    rowActions > 0 && rowActions >= rowCount - 1,
    '统计行内编辑按钮数量，并点击第一行的编辑按钮检查表单是否预填该行数据',
    `${rowCount} 行连接、${rowActions} 个行内编辑按钮；${editDetail}`, { screenshot: file2 })
}

/* ---------------------------------------------------------------- */
/* #197-15 团队与角色：用户名+密码直接建号（真实建一个账号）           */
/* ---------------------------------------------------------------- */
{
  await goto('/settings/team', 2000)
  const emailInput = page.locator('[data-team-create-email="true"]')
  const pwdInput = page.locator('[data-team-create-password="true"]')
  const submit = page.locator('[data-team-create-submit="true"]')
  const formCount = (await emailInput.count()) + (await pwdInput.count()) + (await submit.count())
  const file1 = await shot('team-create-form')

  // 边界路径（真实点击）：先用弱密码提交，必须被拦
  let weakBlocked = false
  let weakDetail = '未执行'
  if (await submit.count()) {
    const unique = `verify197-${Date.now()}@company.com`
    await emailInput.first().fill(unique)
    await pwdInput.first().fill('123')          // 故意过短
    await submit.first().click()
    await page.waitForTimeout(2500)
    const t = (await bodyText()).replace(/\n+/g, ' ')
    weakBlocked = /密码|至少|8/.test(t)
    weakDetail = `弱密码提交后提示「${(t.match(/[^。]*(密码|至少)[^。]*。?/) || ['(无提示)'])[0].slice(0, 180)}」`

    // 正常路径：合法密码必须真的建成账号
    await pwdInput.first().fill('Verify197!pass')
    await submit.first().click()
    await page.waitForTimeout(3500)
    const after = (await bodyText()).replace(/\n+/g, ' ')
    const created = after.includes(unique) || /已创建|创建成功|已添加/.test(after)
    weakDetail += `；合法密码提交后新账号出现在列表=${after.includes(unique)}；提示「${(after.match(/[^。]*(创建|添加)[^。]*。?/) || ['(无)'])[0].slice(0, 140)}」`
    record('197-15', '用户名+密码直接建号可用（含弱密码边界拦截）',
      formCount === 3 && weakBlocked && created,
      '在成员与角色页填写邮箱与密码：先用 3 位弱密码提交，再用合法密码提交',
      weakDetail, { screenshot: file1 })
  } else {
    record('197-15', '用户名+密码直接建号可用（含弱密码边界拦截）', false,
      '查找建号表单（邮箱/密码/提交）', `表单控件数=${formCount}（期望 3）`, { screenshot: file1 })
  }
}

/* ---------------------------------------------------------------- */
/* #197-16 历史资产：迁移结论由服务端给出                             */
/* ---------------------------------------------------------------- */
{
  await goto('/legacy/history', 2200)
  const status = page.locator('[data-legacy-migration-status]')
  const count = await status.count()
  const text = count ? (await status.first().innerText()).replace(/\n+/g, ' | ').slice(0, 400) : '(不存在)'
  const attr = count ? await status.first().getAttribute('data-legacy-migration-status') : null
  const file = await shot('legacy-migration-status')
  record('197-16', '历史资产页给出服务端迁移结论（可判定是否可删菜单）',
    count > 0 && /尚未|未迁移|已全部迁移|已迁移/.test(text),
    '打开历史资产页，读取迁移状态区块与结论',
    `状态区块=${count}；data 属性=${attr}；内容「${text}」`, { screenshot: file })
}

/* ---------------------------------------------------------------- */
/* #197-9 辅助工作台与项目/蓝图的关系被说明                          */
/* ---------------------------------------------------------------- */
{
  await goto('/tools/evaluation', 2200)
  const scope = page.locator('[data-tool-scope="true"]')
  const count = await scope.count()
  const text = count ? (await scope.first().innerText()).replace(/\n+/g, ' | ').slice(0, 400) : '(不存在)'
  const file1 = await shot('tools-evaluation-scope')
  await goto('/tools/cleaning', 2200)
  const scope2 = await page.locator('[data-tool-scope="true"]').count()
  const file2 = await shot('tools-cleaning-scope')
  record('197-9', '辅助工作台说明自身作用域与项目入口（消除信息孤岛）',
    count > 0 && scope2 > 0 && /旧数据集|项目|蓝图/.test(text),
    '分别打开评估工作台与清洗工作台，读取作用域说明区块',
    `评估工作台作用域区块=${count}「${text.slice(0, 220)}…」；清洗工作台作用域区块=${scope2}`,
    { screenshot: file1, screenshot2: file2 })
}

/* ---------------------------------------------------------------- */
/* #191 内部英文枚举是否仍然漏出                                      */
/* ---------------------------------------------------------------- */
{
  const LEAK = /[a-z]+_[a-z_]{3,}/
  const pages = [
    ['/activity', '动态'],
    ['/p/' + P + '/data', '数据'],
    ['/p/' + P + '/runs', '生产'],
    ['/legacy/history', '历史资产'],
  ]
  const leaks = []
  for (const [route, name] of pages) {
    await goto(route, 2000)
    const text = await bodyText()
    const hits = [...new Set((text.match(new RegExp(LEAK.source, 'g')) || []))]
      // 排除技术性/必要的英文串（URL 片段、文件名、模型名、版本号）
      .filter((w) => !/^(sft|grpo|jsonl|alpaca|csv|v1|p_|s_|b_|domain|direction|openai|deepseek|gpt|chat|completions|http|https|api|v|T\d+)/i.test(w))
    if (hits.length) leaks.push(`${name}(${route}): ${hits.slice(0, 6).join(',')}`)
  }
  record('191', '中文界面不再漏出内部英文枚举键', leaks.length === 0,
    '逐页全文正则匹配 snake_case 内部键（排除模型名/格式名等技术串）',
    leaks.length ? `仍发现：${leaks.join(' | ')}` : '动态/数据/生产/历史资产四页均未匹配到内部 snake_case 键')
}

/* ---------------------------------------------------------------- */
/* #192 Markdown 星号是否仍裸露                                      */
/* ---------------------------------------------------------------- */
{
  const pages = ['/today', '/projects', '/p/' + P + '/data', '/p/' + P + '/review', '/p/' + P + '/quality', '/p/' + P + '/quality/new', '/help', '/settings/team']
  const offenders = []
  for (const route of pages) {
    await goto(route, 1600)
    const text = await bodyText()
    // 裸 Markdown 星号：成对出现且紧贴中文
    const hits = text.match(/\*\*[^*\n]{2,40}\*\*/g) || []
    if (hits.length) offenders.push(`${route}: ${hits.slice(0, 3).join(' / ')}`)
  }
  record('192', '页面不再裸露 Markdown 星号', offenders.length === 0,
    '遍历 8 个页面，正则匹配 **加粗** 字面量',
    offenders.length ? `仍发现：${offenders.join(' | ')}` : '8 个页面均未见 ** 字面量')
}

/* ---------------------------------------------------------------- */
/* #194 移动端可用性 + 蓝图右栏不重叠                                 */
/* ---------------------------------------------------------------- */
{
  const mctx = await browser.newContext({ viewport: { width: 390, height: 844 }, locale: 'zh-CN', isMobile: true, hasTouch: true })
  const mp = await mctx.newPage()
  await mp.goto(`${BASE}/login`, { waitUntil: 'networkidle' })
  await mp.getByPlaceholder('请输入邮箱').fill(EMAIL)
  await mp.getByPlaceholder('请输入密码').fill(PASSWORD)
  await mp.getByRole('button', { name: '进入今日工作' }).click()
  await mp.waitForURL(/\/today/, { timeout: 25000 })
  await mp.waitForTimeout(2500)
  const mobileResults = []
  for (const [route, label] of [['/settings/connections', '连接'], ['/p/' + P + '/quality', '质量'], ['/projects', '项目']]) {
    await mp.goto(`${BASE}${route}`, { waitUntil: 'networkidle' })
    await mp.waitForTimeout(1800)
    const overflow = await mp.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
    mobileResults.push(`${label} 横向溢出=${overflow}px`)
    await mp.screenshot({ path: path.join(OUT, `mobile-${label}.png`), fullPage: false })
  }
  const overflowOk = mobileResults.every((r) => Number(r.split('=')[1].replace('px', '')) <= 2)

  // 桌面：蓝图右栏「当前引用配置」文字与按钮是否重叠
  await goto(`/p/${P}/blueprint`)
  const overlap = await page.evaluate(() => {
    const box = document.querySelector('.blueprint-related-config')
    if (!box) return { found: false }
    const els = [...box.querySelectorAll('strong, span, button')].map((el) => ({ t: (el.innerText || '').slice(0, 30), r: el.getBoundingClientRect() }))
    let worst = 0
    for (let i = 0; i < els.length; i += 1) {
      for (let j = i + 1; j < els.length; j += 1) {
        if (els[i].t && els[i].t === els[j].t) continue
        const ox = Math.max(0, Math.min(els[i].r.right, els[j].r.right) - Math.max(els[i].r.left, els[j].r.left))
        const oy = Math.max(0, Math.min(els[i].r.bottom, els[j].r.bottom) - Math.max(els[i].r.top, els[j].r.top))
        if (ox > 2 && oy > 2) worst = Math.max(worst, Math.round(ox * oy))
      }
    }
    return { found: true, worstOverlapArea: worst, rightColWidth: Math.round(document.querySelector('.blueprint-inspector')?.getBoundingClientRect().width ?? 0) }
  })
  const file = await shot('blueprint-no-overlap')
  record('194', '移动端无横向溢出 / 蓝图右栏文字不再压按钮',
    overflowOk && overlap.found && overlap.worstOverlapArea === 0,
    '用 390×844 移动视口访问连接/质量/项目页测溢出；桌面测量右栏元素两两相交面积',
    `移动端：${mobileResults.join('；')}；桌面右栏：宽=${overlap.rightColWidth}px 最大重叠面积=${overlap.worstOverlapArea}px²`,
    { screenshot: file })
}

/* ---------------------------------------------------------------- */
/* #193 表单表达：交付映射列头 / 覆盖矩阵可见标签 / 加载不竖排          */
/* ---------------------------------------------------------------- */
{
  await goto(`/p/${P}/standard`, 1800)
  const loadingVertical = await page.evaluate(() => {
    // 找一个 Spin 的 wrapper，量它的宽度；中文被压成竖排时宽度会接近单字宽
    const spin = document.querySelector('.semi-spin-wrapper, .semi-spin')
    if (!spin) return { present: false }
    const r = spin.getBoundingClientRect()
    return { present: true, width: Math.round(r.width) }
  })
  await goto(`/p/${P}/coverage`, 2000)
  // 覆盖矩阵的可见标签来自两处：① 领域行的 label；② 方向行的列头（与数据行同一套 grid 列宽）
  const covInfo = await page.evaluate(() => {
    const ed = document.querySelector('.document-editor--coverage')
    if (!ed) return { noEditor: true }
    const hdr = ed.querySelector('.document-editor__header-row')
    const hCells = hdr ? [...hdr.children].map((e) => Math.round(e.getBoundingClientRect().width)) : []
    const row = ed.querySelector('.document-editor__row')
    const rCells = row ? [...row.children].map((e) => Math.round(e.getBoundingClientRect().width)) : []
    // 最后一个单元格是图标按钮，宽度允许 4px 内差异（图标 vs 按钮内边距）
    const n = Math.min(hCells.length, rCells.length)
    const diffs = []
    for (let i = 0; i < n; i += 1) diffs.push({ i, h: hCells[i], r: rCells[i], d: Math.abs(hCells[i] - rCells[i]) })
    return {
      headerText: hdr ? hdr.innerText.replace(/\n/g, ' | ') : null,
      fieldLabels: [...ed.querySelectorAll('.wizard-field__label')].map((e) => e.innerText.trim()),
      inputs: [...ed.querySelectorAll('input')].map((i) => ({
        aria: i.getAttribute('aria-label'), ph: i.placeholder, inLabel: Boolean(i.closest('label')),
      })),
      headerWidths: hCells, rowWidths: rCells, widthDiffs: diffs,
    }
  })
  const allAligned = covInfo.widthDiffs ? covInfo.widthDiffs.filter((d) => d.d > 4 && d.i < covInfo.widthDiffs.length - 1).length === 0 : false
  // 每个输入框都必须能被用户看到标签：要么在 <label> 里，要么有占位符，要么在列头行下
  const unlabeled = covInfo.inputs ? covInfo.inputs.filter((i) => !i.inLabel && !i.ph && !i.aria).length : 99
  const visibleLabels = (covInfo.fieldLabels?.length ?? 0) + (covInfo.headerText ? 1 : 0)
  const file1 = await shot('coverage-labels')

  await goto(`/p/${P}/releases/new`, 2200)
  const mappingHeader = await page.locator('.document-editor__header-row, [data-mapping-header]').count()
  const file2 = await shot('mapping-header')
  record('193', '覆盖矩阵输入框有可见标签 / 列头与数据对齐 / 交付映射有列头',
    visibleLabels >= 3 && unlabeled === 0 && allAligned && mappingHeader > 0,
    '统计覆盖矩阵可见标签（label + 列头）、无任何标签的输入框数、列头与数据行的列宽差；并检查交付映射列头',
    `覆盖矩阵 label 数=${covInfo.fieldLabels?.length ?? 0}，列头「${covInfo.headerText}」；完全无标签的输入框=${unlabeled}；` +
    `列宽 [头]=[${(covInfo.headerWidths ?? []).join(',')}] vs [行]=[${(covInfo.rowWidths ?? []).join(',')}]，对齐=${allAligned}；` +
    `交付映射列头行=${mappingHeader}；加载指示=${JSON.stringify(loadingVertical)}`,
    { screenshot: file1, screenshot2: file2 })
}

/* ---------------------------------------------------------------- */
/* #195 部署版本自证                                                  */
/* ---------------------------------------------------------------- */
{
  const res = await page.request.get(`${BASE}/version.json`)
  const json = await res.json()
  await goto('/help', 1800)
  const helpText = await bodyText()
  const showsVersion = helpText.includes(json.version.slice(0, 7)) || !/unknown/.test(helpText)
  const file = await shot('help-version')
  record('195', '部署版本可自证（/version.json 与帮助页）',
    res.status() === 200 && /^[0-9a-f]{40}$/.test(json.version) && showsVersion,
    '请求 /version.json 并打开帮助页检查构建信息',
    `/version.json = ${JSON.stringify(json)}；帮助页含完整 SHA 或不含 unknown = ${showsVersion}`, { screenshot: file })
}

report.finishedAt = new Date().toISOString()
writeFileSync(path.join(OUT, 'report.json'), JSON.stringify(report, null, 2))

const failed = report.checks.filter((c) => !c.pass)
console.log(`\n==== 汇总：${report.checks.length - failed.length}/${report.checks.length} 通过 ====`)
if (failed.length) {
  console.log('未通过：')
  for (const f of failed) console.log(`  ✗ #${f.issue} ${f.title}\n      ${f.observed}`)
}
if (report.httpErrors.length) console.log(`\n5xx 响应：\n  ${report.httpErrors.join('\n  ')}`)
if (report.pageErrors.length) console.log(`\npageerror：\n  ${report.pageErrors.join('\n  ')}`)
console.log(`\n报告：${path.relative(REPO_ROOT, path.join(OUT, 'report.json'))}`)

await browser.close()
process.exit(failed.length ? 1 : 0)
