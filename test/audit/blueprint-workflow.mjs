/** Real Chromium regression for #197/#204; document write APIs are controlled
 * inputs, so the check never modifies the shared development database.
 * Run in a Node/browser container with BASE_URL and PLAYWRIGHT_PATH configured.
 */
import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { mkdirSync, writeFileSync } from 'node:fs'

const require = createRequire(import.meta.url)
const { chromium } = require(process.env.PLAYWRIGHT_PATH ?? 'playwright')
const base = process.env.BASE_URL ?? 'http://127.0.0.1:13211'
const output = process.env.OUT_DIR ?? '/tmp/blueprint-workflow'
mkdirSync(output, { recursive: true })
const browser = await chromium.launch({ headless: true })
const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } })
const page = await context.newPage()
const errors = []
page.on('pageerror', (error) => errors.push(error.message))
const results = []
function passed(name) { results.push(name); console.log(`[PASS] ${name}`) }

try {
  await page.goto(`${base}/login`)
  await page.getByPlaceholder('请输入邮箱').fill(process.env.ADMIN_EMAIL ?? 'admin@company.com')
  await page.getByPlaceholder('请输入密码').fill(process.env.ADMIN_PASSWORD ?? 'admin123456')
  await page.getByRole('button', { name: '进入今日工作' }).click()
  await page.waitForURL(/\/today/, { timeout: 20000 })

  const nodesResponse = await context.request.get(`${base}/api/v1/projects/1/blueprint-nodes`)
  assert.equal(nodesResponse.status(), 200, 'fixture requires an accessible existing project')
  const metadata = await nodesResponse.json()
  const stdPayload = { schemaVersion: 'standard.v1', title: '浏览器回归标准', steps: [
    { id: 'first', title: '识别条件', detail: '列出已知条件', checkpoint: '条件有来源', order: 1 },
    { id: 'second', title: '推导结论', detail: '逐项推导', checkpoint: '结论可验证', order: 2 },
  ] }
  let standard = { id: 101, documentId: 11, projectId: 1, version: 1, kind: 'standard', payload: stdPayload, changeReason: 'fixture', contentHash: 'fixture', createdAt: new Date().toISOString() }
  let blueprint = { id: 201, documentId: 12, projectId: 1, version: 1, kind: 'blueprint', payload: { schemaVersion: 'blueprint.v1', nodes: {
    coverage: { coverageVersionId: 1 }, standard: { standardVersionId: standard.id },
    generation: { modelConnectionId: 1, schemaVersion: 'sft.sample.v1', concurrency: 2, maxTokens: 2000, failurePolicy: 'retry_then_skip' },
    evaluation: {}, rules: {}, humanReview: {}, delivery: {},
  } }, changeReason: 'fixture', contentHash: 'fixture', createdAt: new Date().toISOString() }
  const initialBlueprint = structuredClone(blueprint)
  let standardRevision = 1, blueprintRevision = 1
  const writes = []
  let conflictNext = false
  let delayNext = 0
  await context.route(/\/api\/v1\/projects\/1\/(blueprint|standard)-versions(?:\?.*|\/\d+)?$/, async (route) => {
    const request = route.request()
    const kind = request.url().includes('/standard-versions') ? 'standard' : 'blueprint'
    const version = kind === 'standard' ? standard : blueprint
    if (request.method() === 'GET') {
      if (/\/\d+(?:\?|$)/.test(new URL(request.url()).pathname.split('-versions')[1] ?? '')) {
        const selected = request.url().endsWith('/1') && kind === 'blueprint' ? initialBlueprint : version
        return route.fulfill({ json: { version: selected, readOnly: true } })
      }
      return route.fulfill({ json: { items: [version], document: { revision: kind === 'standard' ? standardRevision : blueprintRevision }, canEdit: true } })
    }
    const body = request.postDataJSON()
    writes.push({ kind, body })
    if (kind === 'blueprint' && conflictNext) {
      conflictNext = false
      return route.fulfill({ status: 409, json: { error: { code: 'REVISION_CONFLICT', message: '版本冲突' } } })
    }
    if (kind === 'blueprint' && delayNext) {
      const delay = delayNext
      delayNext = 0
      await new Promise((resolve) => setTimeout(resolve, delay))
    }
    assert.equal(body.expectedRevision, kind === 'standard' ? standardRevision : blueprintRevision)
    const saved = { ...version, id: version.id + 1, version: version.version + 1, payload: body.payload, changeReason: body.changeReason }
    if (kind === 'standard') { standard = saved; standardRevision++ } else { blueprint = saved; blueprintRevision++ }
    return route.fulfill({ status: 201, json: { revision: kind === 'standard' ? standardRevision : blueprintRevision, data: { version: saved } } })
  })

  await page.goto(`${base}/p/1/blueprint?node=generation&step=dispatch`)
  await page.locator('#blueprint-generation-concurrency').waitFor()
  await page.getByRole('button', { name: '适应画布', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.blueprint-canvas__extent').clientWidth <= document.querySelector('.blueprint-canvas__viewport').clientWidth)
  assert.equal(await page.locator('.blueprint-node').count(), metadata.items.length)
  assert.equal(await page.locator('[data-dependency="generation:evaluation"]').count(), 1)
  assert.equal(await page.locator('[data-dependency="generation:rules"]').count(), 1)
  const missing = await page.locator('.blueprint-node').evaluateAll((nodes) => nodes.filter((node) => {
    const rect = node.getBoundingClientRect()
    const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
    return !hit || !node.contains(hit)
  }).map((node) => node.dataset.nodeKey))
  assert.deepEqual(missing, [], 'all fitted desktop node centres must be hit-testable')
  passed('真实依赖分支和全部节点命中')

  const node = page.locator('[data-node-key="generation"]')
  const before = await node.boundingBox()
  await page.mouse.move(before.x + 30, before.y + 25)
  await page.mouse.down()
  await page.mouse.move(before.x + 55, before.y + 55, { steps: 5 })
  await page.mouse.up()
  const after = await node.boundingBox()
  assert.ok(after.x > before.x + 10 && after.y > before.y + 10)
  assert.equal(writes.length, 0, 'layout changes must not create document versions')
  await node.focus()
  await page.keyboard.press('Alt+ArrowLeft')
  assert.ok((await node.boundingBox()).x < after.x)
  passed('鼠标拖动与键盘移动不改执行配置')

  await page.locator('#blueprint-generation-concurrency').fill('3')
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(writes.length, 1)
  assert.equal(writes[0].body.changeReason, '调整生成')
  assert.equal(blueprint.payload.nodes.generation.concurrency, 3)
  await page.waitForTimeout(1500)
  assert.equal(writes.length, 1, 'unchanged payload must not produce duplicate versions')
  passed('无手填理由自动新版本，未修改不重复保存')

  delayNext = 600
  await page.locator('#blueprint-generation-concurrency').fill('4')
  await page.waitForTimeout(1300)
  await page.locator('#blueprint-generation-concurrency').fill('5')
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(blueprint.payload.nodes.generation.concurrency, 5)
  passed('保存期间继续编辑不会丢失后续草稿')

  await page.goto(`${base}/p/1/blueprint?node=standard&step=reasoning`)
  await page.getByRole('button', { name: '下移步骤 1', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  const recent = writes.slice(-2)
  assert.equal(recent[0].kind, 'standard')
  assert.equal(recent[1].kind, 'blueprint')
  assert.equal(standard.payload.steps[0].id, 'second')
  assert.equal(blueprint.payload.nodes.standard.standardVersionId, standard.id)
  passed('步骤排序先保存真实标准，再更新蓝图版本引用')

  conflictNext = true
  const writesBeforeConflict = writes.length
  await page.getByRole('button', { name: '下移步骤 1', exact: true }).click()
  await page.getByText('版本已被其他人修改', { exact: false }).waitFor()
  await page.waitForTimeout(1600)
  assert.equal(writes.length, writesBeforeConflict + 2, 'conflict must stop automatic retries')
  const createdStandard = standard.id
  await page.getByRole('button', { name: '重读版本并保留草稿' }).click()
  await page.getByRole('button', { name: '保存为新版本', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(writes.at(-1).kind, 'blueprint')
  assert.equal(blueprint.payload.nodes.standard.standardVersionId, createdStandard)
  assert.equal(standard.id, createdStandard, 'retry must reuse the already saved standard')
  passed('409 保留草稿，重试复用已经保存的标准版本')

  const standardCount = writes.filter((write) => write.kind === 'standard').length
  delayNext = 2000
  const inFlight = page.waitForRequest((request) => request.method() === 'POST' && request.url().includes('/blueprint-versions'))
  await page.getByRole('button', { name: '下移步骤 1', exact: true }).click()
  await inFlight
  await page.locator('[data-node-key="generation"]').click()
  await page.getByRole('button', { name: '并发与失败处理', exact: true }).last().click()
  await page.locator('#blueprint-generation-concurrency').fill('6')
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(blueprint.payload.nodes.generation.concurrency, 6)
  assert.equal(writes.filter((write) => write.kind === 'standard').length, standardCount + 1, 'other edits in flight must not duplicate the saved standard')
  passed('标准保存期间编辑其他节点，保留草稿并只创建一个标准版本')

  await page.locator('[data-node-key="standard"]').click()
  await page.getByRole('button', { name: '配置思考步骤与检查点', exact: true }).last().click()
  const writesBeforeInvalid = writes.length
  const checkpoint = page.locator('#blueprint-standard-steps-checkpoint-0')
  const checkpointValue = await checkpoint.inputValue()
  await checkpoint.fill('')
  await page.getByText('思考步骤至少保留一步', { exact: false }).waitFor()
  assert.equal(writes.length, writesBeforeInvalid)
  assert.equal(await checkpoint.inputValue(), '')
  await checkpoint.fill(checkpointValue)
  await page.getByRole('button', { name: '保存为新版本', exact: true }).click()
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(writes.length, writesBeforeInvalid, 'restoring an unchanged standard must not create versions')
  passed('缺少检查点阻止写入并保留草稿，恢复原值不重复创建版本')

  await page.getByRole('button', { name: '看 JSON', exact: true }).click()
  const rawSteps = page.locator('#blueprint-standard-steps')
  const rawStepsValue = await rawSteps.inputValue()
  await rawSteps.fill('[]')
  await page.getByRole('button', { name: '保存为新版本', exact: true }).click()
  await page.getByText('思考步骤至少保留一步', { exact: false }).waitFor()
  assert.equal(writes.length, writesBeforeInvalid)
  await rawSteps.fill(rawStepsValue)
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(writes.length, writesBeforeInvalid)
  await page.locator('[data-node-key="generation"]').click()
  await page.getByRole('button', { name: '并发与失败处理', exact: true }).last().click()
  await page.locator('#blueprint-generation-concurrency').fill('7')
  await page.waitForFunction(() => document.querySelector('.blueprint-save-bar [role="status"]')?.textContent === '已保存')
  assert.equal(blueprint.payload.nodes.generation.concurrency, 7, 'restoring saved content must resume autosave')
  passed('空步骤数组阻止保存，恢复后继续工作')

  await page.goto(`${base}/p/1/blueprint?node=generation&step=dispatch&version=1`)
  await page.locator('#blueprint-generation-concurrency').waitFor()
  assert.equal(await page.locator('#blueprint-generation-concurrency').isDisabled(), true)
  await page.waitForTimeout(1600)
  assert.equal(blueprint.payload.nodes.generation.concurrency, 7)
  passed('历史版本只读且不会自动覆盖')

  for (const width of [390, 768, 1440]) {
    await page.setViewportSize({ width, height: 1000 })
    await page.goto(`${base}/p/1/blueprint?node=delivery`)
    await page.locator('[data-node-key="delivery"]').waitFor()
    await page.waitForTimeout(300)
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), `viewport ${width} must not overflow`)
    assert.ok(await page.locator('[data-node-key="delivery"]').evaluate((node) => {
      const rect = node.getBoundingClientRect()
      const hit = document.elementFromPoint(rect.x + rect.width / 2, rect.y + rect.height / 2)
      return hit && node.contains(hit)
    }), `viewport ${width} deep-linked delivery must be visible`)
  }
  assert.deepEqual(errors, [])
  passed('390/768/1440 页面无横向溢出、无前端异常')
  await page.setViewportSize({ width: 1600, height: 1000 })
  await page.goto(`${base}/p/1/blueprint?node=generation&step=dispatch`)
  await page.locator('#blueprint-generation-concurrency').waitFor()
  await page.screenshot({ path: `${output}/workflow.png`, fullPage: true })
  writeFileSync(`${output}/result.json`, JSON.stringify({ controlledDocumentAPIs: true, base, passed: results, writeCount: writes.length, pageErrors: errors }, null, 2) + '\n')
} finally { await browser.close() }
