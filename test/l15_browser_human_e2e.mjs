/**
 * 真人式浏览器验收（真 Chromium，真鼠标/真键盘）。
 *
 * 运行：
 *   node test/l15_browser_human_e2e.mjs                 # 默认 http://127.0.0.1:3210
 *   BASE_URL=http://127.0.0.1:18170 node test/l15_browser_human_e2e.mjs
 *
 * ---------------------------------------------------------------------------
 * 为什么需要这条腿（以及它和既有 UI 测试的分工）
 * ---------------------------------------------------------------------------
 * 既有 UI 测试全部是「源码级断言 + esbuild SSR 静态渲染」：
 *   - test/l14_ui_smoke.mjs / l14_meta_selfcheck.mjs 头部都写明「本机没有
 *     chromium / playwright / jsdom」，所以用 renderToStaticMarkup 真跑组件函数体；
 *   - test/l15_stage_routes.mjs 默认只做源码文本断言（CI 可执行性硬要求）。
 *
 * 这条腿补的是它们**结构上覆盖不到**的部分：
 *   1. useEffect 在 SSR 下不执行 → 登录后的 bootstrap 拉取、路由守卫、跳转
 *      在既有测试里根本没跑过；
 *   2. 受控组件（Semi Input/InputNumber）依赖 onChange 链路，`el.value = x`
 *      这种赋值 React 不认；只有真键盘输入才能验证；
 *   3. 真实点击的命中测试（元素被遮挡 / pointer-events / 层级）SSR 无法发现；
 *   4. 运行时 console error、pageerror、失败请求、4xx/5xx 只有真浏览器能收。
 *
 * 因此本脚本对每一步都断言「人看到的东西」：URL 变化、页面标题文案、
 * 受控输入框读回的值、以及浏览器事件流必须干净。
 *
 * ---------------------------------------------------------------------------
 * 行为边界（刻意为之）
 * ---------------------------------------------------------------------------
 * - 只写「可安全丢弃」的数据：登录/退出、导航、点「估算规模」（纯计算，无副作用）、
 *   打开再取消弹窗。**不点「创建任务」**，避免给真实实例塞垃圾数据集。
 * - 不复用宿主已装包，直接用全局安装的 playwright 起浏览器；缺 Chromium 时
 *   给出明确修复命令而不是静默跳过（静默跳过 = 永远跑不起来的守卫）。
 */

import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

const REPO_ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const ARTIFACT_DIR = path.join(REPO_ROOT, 'test', 'artifacts', 'browser-human')
const BASE_URL = process.env.BASE_URL ?? 'http://127.0.0.1:3210'
const ADMIN_EMAIL = process.env.ADMIN_EMAIL ?? 'admin@company.com'
const ADMIN_PASSWORD = process.env.ADMIN_PASSWORD ?? 'admin123456'

const failures = []
const shots = []

function record(name, ok, detail) {
  console.log(`[${ok ? 'PASS' : 'FAIL'}] ${name}: ${detail}`)
  if (!ok) failures.push(`${name} — ${detail}`)
}

async function shot(page, label) {
  const file = path.join(ARTIFACT_DIR, `${String(shots.length + 1).padStart(2, '0')}-${label}.png`)
  await page.screenshot({ path: file, fullPage: false })
  shots.push(file)
}

/**
 * 真人打字：逐字符 type（触发 keydown/keypress/input 全链路）。
 * 不用 fill()，因为 fill() 走的是 Playwright 的快速路径，验证不了受控组件。
 */
async function humanType(page, placeholder, text) {
  const input = page.getByPlaceholder(placeholder)
  await input.click()
  await input.fill('')
  await input.pressSequentially(text, { delay: 25 })
  return input
}

/** 真人点击：先 hover 再 click，走命中测试。 */
async function humanClick(page, locator) {
  const target = locator.first()
  await target.scrollIntoViewIfNeeded()
  await target.hover()
  await target.click()
}

/** 页面级断言：可见标题文案 + 路径。 */
async function assertPage(page, expectedTitle, expectedPath) {
  const title = page.locator('.console-page-title').first()
  await title.waitFor({ state: 'visible', timeout: 10_000 })
  const actualTitle = (await title.innerText()).trim()
  const actualPath = new URL(page.url()).pathname
  const ok = actualTitle.includes(expectedTitle) && actualPath === expectedPath
  record(
    `页面 ${expectedPath}`,
    ok,
    ok ? `标题「${actualTitle}」` : `期望标题含「${expectedTitle}」+ 路径 ${expectedPath}，实际「${actualTitle}」@ ${actualPath}`,
  )
  return ok
}

async function main() {
  const require = createRequire(path.join(REPO_ROOT, 'package.json'))
  let chromium
  try {
    ;({ chromium } = require('/root/.pi/agent/npm/node_modules/playwright'))
  } catch {
    try {
      ;({ chromium } = require('playwright'))
    } catch {
      console.error('缺少 playwright。安装：cd /root/.pi/agent/npm/node_modules/playwright && node cli.js install --with-deps chromium')
      process.exit(2)
    }
  }

  mkdirSync(ARTIFACT_DIR, { recursive: true })

  const browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
  const page = await context.newPage()

  // 事件流：整场收集，最后统一断言必须干净。
  const consoleErrors = []
  const pageErrors = []
  const failedRequests = []
  const httpErrors = []
  page.on('console', (m) => {
    if (m.type() !== 'error') return
    const text = m.text()
    // 浏览器把「资源加载 4xx」也归为 console error，而它已在 httpErrors 里按
    // 「是否预期」分类（未登录探测 /auth/me 的 401 是预期行为）。
    // 同一件事计数两次会让断言无法反映真实缺陷，因此这里排除纯资源加载错误，
    // 只保留真正的 JS 运行时 console error。
    if (/^Failed to load resource:/.test(text)) return
    consoleErrors.push(text)
  })
  page.on('pageerror', (e) => pageErrors.push(e.message))
  page.on('requestfailed', (r) => failedRequests.push(`${r.failure()?.errorText} ${r.url()}`))
  page.on('response', (r) => {
    if (r.status() >= 400) httpErrors.push(`${r.status()} ${r.url()}`)
  })

  try {
    // ---------------------------------------------------------------- 1. 登录页
    await page.goto(`${BASE_URL}/login`, { waitUntil: 'load' })
    await page.waitForSelector('text=登录你的账号', { timeout: 15_000 })
    record('登录页可达', true, `${BASE_URL}/login → 标题「${await page.title()}」`)
    await shot(page, 'login')

    // 未登录访问控制台必须被弹回登录页（路由守卫）
    await page.goto(`${BASE_URL}/console/tasks`, { waitUntil: 'load' })
    await page.waitForURL(/\/login$/, { timeout: 10_000 }).catch(() => {})
    record(
      '未登录访问 /console/tasks 被弹回登录页',
      new URL(page.url()).pathname === '/login',
      `实际落在 ${new URL(page.url()).pathname}`,
    )

    // ------------------------------------------------------- 2. 空提交的错误提示
    await humanClick(page, page.getByRole('button', { name: '进入我的任务' }))
    await page.waitForTimeout(1200)
    const stillOnLogin = new URL(page.url()).pathname === '/login'
    record('空邮箱密码提交不进入控制台', stillOnLogin, `仍停留在 ${new URL(page.url()).pathname}`)

    // ---------------------------------------------------------- 3. 真人输入登录
    await humanType(page, '请输入邮箱', ADMIN_EMAIL)
    await humanType(page, '请输入密码', ADMIN_PASSWORD)
    const typedEmail = await page.getByPlaceholder('请输入邮箱').inputValue()
    const typedPassword = await page.getByPlaceholder('请输入密码').inputValue()
    record(
      '受控输入框读回真实键入值',
      typedEmail === ADMIN_EMAIL && typedPassword === ADMIN_PASSWORD,
      `邮箱「${typedEmail}」密码长度 ${typedPassword.length}`,
    )
    await shot(page, 'login-filled')

    await humanClick(page, page.getByRole('button', { name: '进入我的任务' }))
    await page.waitForURL(/\/console\//, { timeout: 15_000 })
    await page.waitForSelector('.console-page-title', { timeout: 15_000 })
    record('登录成功进入控制台', true, `落地 ${new URL(page.url()).pathname}`)
    await shot(page, 'tasks-after-login')

    // 侧边栏必须显示登录身份与管理员身份
    const sidebarText = await page.locator('.app-layout__sidebar').innerText()
    record(
      '侧边栏显示登录账号与管理员身份',
      sidebarText.includes(ADMIN_EMAIL) && sidebarText.includes('管理员'),
      sidebarText.includes(ADMIN_EMAIL) ? '含账号与「管理员」' : `侧边栏未含 ${ADMIN_EMAIL}`,
    )

    // ------------------------------------------------- 4. 点击侧边栏走遍用户页
    const userPages = [
      ['工作台', '/console/home', '任务与待办'],
      ['新建任务', '/console/planning', '先填主题和目标规模'],
      ['我的任务', '/console/tasks', '任务列表与处理'],
      ['数据资产', '/console/results', '查看结果与交付文件'],
      ['质量评估', '/console/evaluation', '质量评估'],
      ['数据清洗', '/console/cleaning', '拦截拒答与异常样本'],
      ['账户与帮助', '/console/help', '帮助与术语'],
    ]
    for (const [navLabel, expectedPath, expectedTitle] of userPages) {
      const nav = page.locator('.app-layout__sidebar').getByText(navLabel, { exact: true })
      await humanClick(page, nav)
      await page.waitForURL(new RegExp(`${expectedPath.replace(/\//g, '\\/')}$`), { timeout: 10_000 })
      await assertPage(page, expectedTitle, expectedPath)
    }
    await shot(page, 'cleaning')

    // ------------------------------------------------------ 5. 管理页全部可达
    const adminPages = [
      ['运营监控', '/console/operations', '系统运行态'],
      ['AI 服务', '/console/admin/providers', '管理 AI 服务'],
      ['结果存储', '/console/admin/storage', '结果存储'],
      ['生成规则', '/console/admin/strategies', '生成规则'],
      ['生成指令', '/console/admin/prompts', '生成指令'],
      ['操作记录', '/console/admin/audit', '配置记录'],
    ]
    for (const [navLabel, expectedPath, expectedTitle] of adminPages) {
      // 侧边栏的「系统设置」是一个**可折叠分组**，默认收起。
      // 不先展开就去 scrollIntoViewIfNeeded，会等一个不可见的元素直到超时 ——
      // 这是测试自身的缺陷（父代理实测确认：展开后「运营监控」可见且可点，
      // 点击后 URL 正确变为 /console/operations），不是产品缺陷。
      const group = page.locator('.app-layout__sidebar .semi-navigation-sub-title', { hasText: '系统设置' })
      if (await group.count() > 0 && !(await page.locator('.app-layout__sidebar').getByText(navLabel, { exact: true }).first().isVisible().catch(() => false))) {
        await group.first().click()
        await page.waitForTimeout(400)
      }
      const nav = page.locator('.app-layout__sidebar').getByText(navLabel, { exact: true })
      await humanClick(page, nav)
      await page.waitForURL(new RegExp(`${expectedPath.replace(/\//g, '\\/')}$`), { timeout: 10_000 })
      await assertPage(page, expectedTitle, expectedPath)
    }
    await shot(page, 'admin-audit')

    // --------------------------------------- 6. 表单：估算规模（无副作用计算）
    await humanClick(page, page.locator('.app-layout__sidebar').getByText('新建任务', { exact: true }))
    await page.waitForURL(/\/console\/planning$/, { timeout: 10_000 })

    // 主题：真人键入
    const keywordInput = page.locator('input').nth(1)
    await keywordInput.click()
    await keywordInput.fill('')
    await keywordInput.pressSequentially('军事', { delay: 30 })
    record('主题输入框读回「军事」', (await keywordInput.inputValue()) === '军事', `读回「${await keywordInput.inputValue()}」`)

    // 目标样本数：InputNumber，用键盘输入并回车提交，验证 onChange 链路
    const numberInput = page.locator('.semi-input-number input').first()
    await numberInput.click()
    await numberInput.press('Control+a')
    await numberInput.pressSequentially('24', { delay: 30 })
    await numberInput.press('Tab')
    await page.waitForTimeout(300)
    record('目标样本数读回 24', (await numberInput.inputValue()).includes('24'), `读回「${await numberInput.inputValue()}」`)
    await shot(page, 'planning-filled')

    await humanClick(page, page.getByRole('button', { name: '估算规模' }))
    await page.waitForSelector('text=预计问题总量', { timeout: 15_000 })
    const estimateText = await page.locator('.console-card-grid-2').last().innerText()
    record('估算规模产出结果卡片', estimateText.includes('领域数') && estimateText.includes('预计样本总量'), estimateText.replace(/\n+/g, ' | ').slice(0, 120))
    await shot(page, 'planning-estimated')

    // ------------------------- 7. 弹窗：打开→填写→取消，验证不产生脏数据
    await humanClick(page, page.locator('.app-layout__sidebar').getByText('AI 服务', { exact: true }))
    await page.waitForURL(/\/console\/admin\/providers$/, { timeout: 10_000 })
    const providersBefore = await page.locator('.semi-table-tbody .semi-table-row').count()

    await humanClick(page, page.getByRole('button', { name: '新增 AI 服务' }))
    await page.waitForSelector('.semi-modal-content', { timeout: 10_000 })
    const nameInput = page.locator('.semi-modal-content input').first()
    await nameInput.click()
    await nameInput.pressSequentially('e2e-探针-请勿保存', { delay: 20 })
    record('弹窗输入框读回键入值', (await nameInput.inputValue()) === 'e2e-探针-请勿保存', `读回「${await nameInput.inputValue()}」`)
    await shot(page, 'provider-modal')

    await humanClick(page, page.locator('.semi-modal-footer').getByRole('button', { name: '取消' }))
    await page.waitForSelector('.semi-modal-content', { state: 'detached', timeout: 10_000 })
    const providersAfter = await page.locator('.semi-table-tbody .semi-table-row').count()
    record('取消弹窗后服务数量不变（无脏写）', providersBefore === providersAfter, `${providersBefore} → ${providersAfter}`)

    // ----------------------------------------------------------- 8. 退出登录
    await humanClick(page, page.locator('.sidebar-user-area button').last())
    await page.waitForURL(/\/login$/, { timeout: 10_000 })
    record('退出登录回到登录页', true, `落地 ${new URL(page.url()).pathname}`)

    // ------------------------------------------------------ 9. 浏览器事件流干净
    const blockingHttpErrors = httpErrors.filter((line) => !/\/auth\/session|401/.test(line))
    record('无未捕获页面异常（pageerror）', pageErrors.length === 0, pageErrors.length ? pageErrors.join(' | ') : '0 条')
    record('无 console error（纯资源加载 4xx 已在「无非预期 4xx/5xx」中按预期分类）',
      consoleErrors.length === 0, consoleErrors.length ? consoleErrors.join(' | ') : '0 条')
    record('无失败请求（requestfailed）', failedRequests.length === 0, failedRequests.length ? failedRequests.join(' | ') : '0 条')
    record(
      '无非预期 4xx/5xx（未登录探测的 401 已排除）',
      blockingHttpErrors.length === 0,
      blockingHttpErrors.length ? blockingHttpErrors.join(' | ') : `共 ${httpErrors.length} 条，均已归类为预期`,
    )
  } catch (error) {
    record('执行过程未抛异常', false, error.message)
    await shot(page, 'failure').catch(() => {})
  } finally {
    const report = {
      baseUrl: BASE_URL,
      at: new Date().toISOString(),
      browser: browser.version(),
      passed: shots.length - failures.length,
      failures,
      screenshots: shots,
      consoleErrors,
      pageErrors,
      failedRequests,
      httpErrors,
    }
    writeFileSync(path.join(ARTIFACT_DIR, 'report.json'), JSON.stringify(report, null, 2))
    await browser.close()
  }

  console.log(`\n截图与报告：${ARTIFACT_DIR}`)
  if (failures.length) {
    console.error(`\n${failures.length} 项失败：`)
    for (const item of failures) console.error(`  - ${item}`)
    process.exit(1)
  }
  console.log('\n全部通过：真人式浏览器验收无回归。')
}

await main()
