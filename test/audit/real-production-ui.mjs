/** Real browser acceptance against live services. No API interception or fixture writes. */
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync, readFileSync, existsSync } from 'node:fs'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const output = 'output/playwright/real-production'
mkdirSync(output, { recursive: true })
const baseURL = process.env.REAL_UI_URL || 'http://127.0.0.1:3210'
const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'], ...(process.env.CHROMIUM_PATH ? { executablePath: process.env.CHROMIUM_PATH } : {}) })
const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
context.setDefaultTimeout(15000)
const page = await context.newPage()
const report = { startedAt: new Date().toISOString(), baseURL, serviceMode: 'live', apiInterception: false, steps: [], findings: [], status: 'running' }
page.on('pageerror', (error) => report.findings.push({ kind: 'pageerror', message: error.message }))
await context.tracing.start({ screenshots: true, snapshots: true, sources: true })

async function capture(name) {
  await page.screenshot({ path: `${output}/${name}.png`, fullPage: true })
  writeFileSync(`${output}/${name}.txt`, await page.locator('body').innerText())
  report.steps.push({ name, url: page.url() })
}

try {
  await page.goto(`${baseURL}/projects`)
  await page.locator('#atelier-login-email, [data-studio-page="projects"]').first().waitFor()
  if (new URL(page.url()).pathname === '/login') {
    assert.ok(process.env.REAL_UI_EMAIL && process.env.REAL_UI_PASSWORD, 'Provide real test account through environment variables')
    await page.getByLabel('邮箱', { exact: true }).fill(process.env.REAL_UI_EMAIL)
    await page.getByLabel('密码', { exact: true }).fill(process.env.REAL_UI_PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await page.waitForURL((url) => url.pathname !== '/login')
  }
  await capture('01-projects')
  const savedFile = `${output}/project.json`
  let projectId = Number(process.env.REAL_PROJECT_ID || (existsSync(savedFile) ? JSON.parse(readFileSync(savedFile)).projectId : 0))
  if (!projectId) {
    await page.getByRole('button', { name: '新建项目', exact: true }).click()
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.getByRole('alert').filter({ hasText: '必填' }).waitFor()
    const focused = await page.evaluate(() => document.activeElement?.id)
    if (focused !== 'wizard-name') report.findings.push({ kind: 'usability', code: 'wizard-invalid-focus', actual: focused, expected: 'wizard-name' })
    await capture('02-wizard-empty-validation')
    const projectName = `真实点击验收-${new Date().toISOString().replace(/[:.]/g, '-')}`
    await page.getByLabel('项目名称').fill(projectName)
    await page.getByLabel('目标', { exact: true }).fill('通过真实浏览器验证单条问答设计、试制、审阅和发布流程。')
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    for (const id of ['wizard-domains', 'wizard-directions', 'wizard-questions', 'wizard-pilot']) await page.locator(`#${id}`).fill('1')
    await capture('03-wizard-scale')
    await page.getByRole('button', { name: '下一步', exact: true }).click()
    await page.getByLabel('预算上限（分）').fill('100')
    await capture('04-wizard-budget')
    await page.getByRole('button', { name: '创建项目草稿', exact: true }).click()
    await page.waitForURL(/\/p\/\d+\/overview/)
    projectId = Number(new URL(page.url()).pathname.split('/')[2])
    writeFileSync(savedFile, JSON.stringify({ projectId, projectName, createdThrough: 'UI', createdAt: new Date().toISOString() }, null, 2))
  } else await page.goto(`${baseURL}/p/${projectId}/overview`)
  report.projectId = projectId
  await capture('05-project-overview')
  await page.getByRole('link', { name: /^1\s+设计$/ }).click()
  await page.locator('[data-studio-page="blueprint"]').waitFor()
  await capture('06-design-start')
  const coverageLink = page.getByRole('button', { name: /查看覆盖矩阵|编辑覆盖矩阵|编辑目标结构/ })
  await coverageLink.click()
  await page.locator('[data-studio-page="coverage"]').waitFor()
  await capture('07-target-open-from-design')
  if (new URL(page.url()).searchParams.has('version')) {
    report.findings.push({ kind: 'usability', code: 'design-target-opens-readonly', url: page.url() })
    await page.getByRole('link', { name: '目标结构', exact: true }).click()
  }
  await page.getByLabel('领域名称', { exact: true }).fill('生活常识')
  await page.getByLabel('方向名称', { exact: true }).fill('节约用水')
  await page.getByLabel('每个方向计划数量', { exact: true }).fill('1')
  const sourceSelect = page.locator('.source-direction .semi-select').first()
  if (await sourceSelect.count()) {
    await sourceSelect.click()
    await page.getByRole('listbox').getByText('AI 合成', { exact: true }).click()
  }
  if (await page.getByRole('button', { name: '保存覆盖方案新版本' }).isEnabled()) await page.getByRole('button', { name: '保存覆盖方案新版本' }).click()
  await page.getByRole('button', { name: '继续配置生成 →' }).waitFor()
  await page.getByRole('button', { name: '继续配置生成 →' }).click()
  await page.locator('[data-studio-page="blueprint"]').waitFor()
  await capture('08-target-return-design')
  await page.locator('[data-configuration-step="generation:model"]').click()
  await page.locator('#blueprint-generation-modelConnectionId').click()
  await page.getByRole('listbox').waitFor()
  await capture('09-model-options')
  const connectionsResponse = await page.request.get(`${baseURL}/api/v1/settings/connection-options`)
  assert.equal(connectionsResponse.ok(), true, 'Real model connections must be readable')
  const connectionBody = await connectionsResponse.json()
  const providers = connectionBody.providers || []
  report.connections = providers.map(({ id, name, model, isActive, configIssues }) => ({ id, name, model, isActive, configIssues }))
  const chosen = providers.find((item) => item.isActive && !item.configIssues?.length && /deepseek/.test(item.model)) || providers.find((item) => item.isActive && !item.configIssues?.length)
  if (chosen) {
    await page.getByRole('listbox').getByText(chosen.name || `未命名连接 #${chosen.id}`, { exact: true }).click()
    await page.getByRole('button', { name: '保存并继续', exact: true }).click()
    await capture('10-generation-model-saved')
    await page.locator('[data-configuration-step="standard:reasoning"]').click()
    await capture('11-standard-edit')
    await page.locator('#blueprint-standard-steps-title-0').fill('确定节约用水任务')
    await page.locator('#blueprint-standard-steps-checkpoint-0').fill('明确提出一个可执行的节水动作，避免未经证实的数字。')
    await page.getByRole('button', { name: '保存并继续', exact: true }).click()
    await capture('12-standard-saved')
    await page.locator('[data-configuration-step="evaluation:judges"]').click()
    const judge = providers.find((item) => item.isActive && !item.configIssues?.length && item.model !== chosen.model)
    if (judge) {
      if (!(await page.locator('#blueprint-evaluation-judgeConnectionIds').innerText()).includes(judge.name)) {
        await page.locator('#blueprint-evaluation-judgeConnectionIds').click()
        await page.getByRole('listbox').getByText(judge.name || `未命名连接 #${judge.id}`, { exact: true }).click()
        await page.keyboard.press('Escape')
      }
      await page.getByRole('button', { name: '保存并继续', exact: true }).click()
      await page.locator('#blueprint-evaluation-rubricVersionId').click()
      await page.getByRole('listbox').getByText(/项目创建：初始化 Atelier 配置/).first().click()
      await capture('13a-rubric-selected')
      await page.getByRole('button', { name: '保存并继续', exact: true }).click()
      await capture('13-judge-configured')
    } else report.findings.push({ kind: 'environment', code: 'no-independent-model' })
    await page.getByRole('link', { name: /^2\s+生产$/ }).click()
    await page.getByRole('button', { name: /^(开始试制|新建试制)$/ }).click()
    await page.locator('[data-studio-page="pilot"]').waitFor()
    await page.getByLabel('生产数量（1–100）').fill('1')
    await page.getByLabel('预算上限（分）').fill('100')
    await capture('14-pilot-preflight')
    const detailsResponse = await page.request.get(`${baseURL}/api/v1/projects/${projectId}/blueprint-versions?limit=1`)
    const details = await detailsResponse.json()
    const latestBlueprint = await page.request.get(`${baseURL}/api/v1/projects/${projectId}/blueprint-versions/${details.items[0].version}`)
    report.blueprint = (await latestBlueprint.json()).version?.payload
    const savedBatchPath = `${output}/batch.json`
    if (existsSync(savedBatchPath) && JSON.parse(readFileSync(savedBatchPath)).projectId === projectId) {
      const savedBatch = JSON.parse(readFileSync(savedBatchPath))
      await page.getByRole('link', { name: /^2\s+生产$/ }).click()
      await page.getByRole('button', { name: savedBatch.resourceId, exact: true }).click()
    } else if (await page.getByRole('button', { name: '启动试制批次', exact: true }).isEnabled()) {
      await page.getByRole('button', { name: '启动试制批次', exact: true }).click()
      await page.waitForURL(/\/runs\/b_\d+$/)
      const resourceId = new URL(page.url()).pathname.split('/').at(-1)
      writeFileSync(savedBatchPath, JSON.stringify({ resourceId, projectId, startedThrough: 'UI', unitCount: 1, budgetLimitMinor: 100 }, null, 2))
    } else report.findings.push({ kind: 'blocked', code: 'pilot-preflight', message: await page.locator('[data-preflight]').innerText() })
    if (existsSync(savedBatchPath) && JSON.parse(readFileSync(savedBatchPath)).projectId === projectId) {
      await capture('15-live-pilot-started')
      await page.waitForFunction(() => {
        const status = document.querySelector('[data-studio-page="batch-detail"]')?.getAttribute('data-batch-status')
        return status && !['queued', 'running', 'pause_requested'].includes(status)
      }, undefined, { timeout: 60000 }).catch(() => {})
      await capture('16-live-pilot-result')
      const batchStatus = await page.locator('[data-studio-page="batch-detail"]').getAttribute('data-batch-status')
      report.batchStatus = batchStatus
      if (batchStatus !== 'completed') {
        await page.getByRole('button', { name: '异常恢复', exact: true }).click()
        await capture('17-live-failure-details')
        report.findings.push({ kind: 'environment', code: 'real-generation-failed', status: batchStatus, message: await page.locator('body').innerText() })
      }
    }
  } else {
    report.findings.push({ kind: 'environment', code: 'no-active-complete-provider' })
    await page.keyboard.press('Escape')
  }
  console.log(JSON.stringify({ projectId, findings: report.findings, pageText: await page.locator('body').innerText() }, null, 2))
  report.status = 'passed'
} catch (error) {
  report.status = 'failed'
  report.error = error.stack || error.message
  await capture('failure').catch(() => {})
  throw error
} finally {
  report.finishedAt = new Date().toISOString()
  writeFileSync(`${output}/report.json`, JSON.stringify(report, null, 2))
  await context.tracing.stop({ path: `${output}/trace.zip` })
  await browser.close()
}
