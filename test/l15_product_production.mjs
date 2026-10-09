/** Product-flow regression guard. Run in the Node container. */
import assert from 'node:assert/strict'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const read = (name) => readFileSync(path.join(root, 'apps/web-user/src/studio/pages', name), 'utf8')
const blueprint = read('BlueprintPages.tsx')
const runs = read('RunPages.tsx')
let checks = 0
function check(name, predicate) {
  assert.ok(predicate, name)
  checks += 1
  console.log(`PASS ${name}`)
}

function preservesFields(source) {
  return source.includes('fields: visibleFields.filter((field) => !advancedNames.has(field.name))')
    && source.includes('fields: visibleFields.filter((field) => advancedNames.has(field.name))')
}
function guardsContinue(source) {
  return source.includes('if (dirty && !(await save())) return')
    && source.includes('containsInvalidJSONMarker(draft)')
    && source.includes('expectedRevision: headRevision')
    && source.includes('setAutoSave(false)')
}
function preservesProduction(source) {
  return source.includes('idempotencyKeyRef.current')
    && source.includes('await client.post<{ data?: { batchId?: number } }>')
    && source.includes('disabled={!ready || !canRun}')
    && source.includes('blueprintPayloadError')
}

check('Linear configuration comes from the existing metadata', blueprint.includes('specs.flatMap((spec) => configurableSteps(spec)') && blueprint.includes('data-configuration-step'))
check('Every configuration field remains editable in exactly one section', preservesFields(blueprint))
check('Step navigation is keyboard accessible and shareable', blueprint.includes('aria-current={index === configurationIndex') && blueprint.includes("params.set('step', target.step.key)"))
check('Dependency graph is secondary and closed by default', blueprint.includes('<details className="blueprint-dependency-diagram">'))
check('Explicit continuation waits for successful version save', guardsContinue(blueprint))
check('Configuration conflict preserves the draft', blueprint.includes('版本已被其他人修改') && blueprint.includes('你的草稿已保留'))
check('Autosave and referenced standard saves remain intact', blueprint.includes('window.setTimeout(() => void save(), 1200)') && blueprint.includes('pendingStandard.current'))
check('Planning adopts the current project blueprint', runs.includes('overview.data.versions.blueprint?.versionId') && runs.includes('<details className="production-version-overrides">'))
check('Quantity and budget remain primary fields', runs.includes('production-planning-basics') && runs.indexOf('id="plan-units"') < runs.indexOf('production-version-overrides') && runs.indexOf('id="plan-budget"') < runs.indexOf('production-version-overrides'))
check('Paid run still uses guarded real API and a stable idempotency key', preservesProduction(runs))
check('Batch progress appears before optional analysis', runs.indexOf('production-progress-panel') < runs.indexOf('production-analysis-panel') && runs.includes('data-batch-step-status={step.status}'))
check('Snapshots and events are accessible disclosure panels', runs.includes('<details className="production-snapshot-panel">') && runs.includes('<details className="production-events-panel">'))
check('Completed output has a direct review action', runs.includes("navigate(scope.href('project.review'))"))
check('Production counts and shortfall facts are retained', ['data-count="planned"', 'data-count="completed"', 'data-count="failed"', 'data-count="inFlight"', 'data-batch-shortfall="true"'].every((marker) => runs.includes(marker)))

// Mutation checks prove the guard rejects lost fields or unsafe continuation.
check('Mutation: lost advanced fields are rejected', !preservesFields(blueprint.replace('fields: visibleFields.filter((field) => advancedNames.has(field.name))', 'fields: []')))
check('Mutation: continue after failed save is rejected', !guardsContinue(blueprint.replace('if (dirty && !(await save())) return', 'if (dirty) void save()')))
check('Mutation: bypassing paid-run readiness is rejected', !preservesProduction(runs.replace('disabled={!ready || !canRun}', 'disabled={false}')))
console.log(`${checks} production-flow checks passed`)

if (process.argv.includes('--with-browser')) {
  const { chromium } = createRequire(import.meta.url)(process.env.PLAYWRIGHT_PATH || 'playwright')
  const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE || undefined, args: ['--no-sandbox'] })
  const context = await browser.newContext({ viewport: { width: 1440, height: 960 }, locale: 'zh-CN' })
  const page = await context.newPage()
  page.setDefaultTimeout(10000)
  const base = process.env.PRODUCT_UI_BASE_URL || 'http://127.0.0.1:13310'
  // Frozen from internal/model.BlueprintNodeSpecs via Docker Go; only display help omitted.
  const metadata = {"items":[{"key":"coverage","payloadField":"coverage","label":"覆盖范围","availability":"available","task":"T04、T11","requiresGeneration":false,"fields":[{"name":"coverageVersionId","label":"覆盖版本","kind":"id","required":true}]},{"key":"standard","payloadField":"standard","label":"思维标准","availability":"available","task":"T04、T11","requiresGeneration":false,"fields":[{"name":"standardVersionId","label":"标准版本","kind":"id","required":true},{"name":"steps","label":"思考步骤（可直接编辑）","kind":"json","required":false}]},{"key":"generation","payloadField":"generation","label":"生成","availability":"available","task":"T11、T12","requiresGeneration":true,"fields":[{"name":"modelConnectionId","label":"模型服务","kind":"id","required":true},{"name":"modelVersion","label":"模型版本","kind":"string","required":false},{"name":"sourceVersionId","label":"素材来源版本","kind":"id","required":false},{"name":"schemaVersion","label":"输出内容类型","kind":"enum","required":true,"options":["sft.sample.v1","grpo.sample.v1"]},{"name":"concurrency","label":"并发","kind":"int","required":true,"min":1,"max":32},{"name":"maxTokens","label":"单次输出上限","kind":"int","required":true},{"name":"temperature","label":"温度","kind":"float","required":false,"max":2},{"name":"failurePolicy","label":"失败策略","kind":"enum","required":false,"options":["retry_then_skip","stop_batch"]},{"name":"jsonSchema","label":"输出字段约束","kind":"json","required":false}]},{"key":"evaluation","payloadField":"evaluation","label":"独立评估","availability":"available","task":"T11、T14","requiresGeneration":false,"fields":[{"name":"judgeConnectionIds","label":"检查模型服务","kind":"idList","required":true},{"name":"rubricVersionId","label":"量表版本","kind":"id","required":true},{"name":"samplingSeed","label":"可复现抽样编号","kind":"int","required":false},{"name":"weights","label":"维度权重","kind":"ratioMap","required":false},{"name":"missingScorePolicy","label":"缺分策略","kind":"enum","required":false,"options":["exclude","fail_experiment"]}]},{"key":"rules","payloadField":"rules","label":"规则检查","availability":"available","task":"T11、T15","requiresGeneration":false,"fields":[{"name":"qualityPolicyVersionId","label":"质量策略版本","kind":"id","required":true}]},{"key":"human_review","payloadField":"humanReview","label":"人工检查点","availability":"available","task":"T11、T16","requiresGeneration":false,"fields":[{"name":"assignment","label":"人工检查方式","kind":"enum","required":true,"options":["risk-based","all","sampled"]},{"name":"requiredEvidence","label":"必须提供的依据","kind":"stringList","required":true},{"name":"riskScope","label":"风险范围","kind":"string","required":false},{"name":"sampleRate","label":"抽检比例","kind":"ratio","required":false,"max":1}]},{"key":"delivery","payloadField":"delivery","label":"版本交付","availability":"available","task":"T11、T20","requiresGeneration":false,"fields":[{"name":"mappingVersionId","label":"映射版本","kind":"id","required":true},{"name":"format","label":"输出格式","kind":"enum","required":true,"options":["jsonl","csv","alpaca","sharegpt"]},{"name":"intendedUse","label":"用途","kind":"string","required":true},{"name":"limitations","label":"限制","kind":"stringList","required":false}]}],"nodeKeys":["coverage","standard","generation","evaluation","rules","human_review","delivery"]}
  const output = path.join(root, 'output/playwright/product-flow')
  mkdirSync(output, { recursive: true })
  const results = []
  const errors = []
  const unknown = []
  const writes = []
  const batchCommands = []
  const controls = []
  let mode = 'success'
  let failBatch = true
  let failBlueprintRead = false
  let canRun = true
  let revision = 5
  const envelope = (data, capabilities = {}) => ({ data, capabilities, links: {}, warnings: [], revision: 1 })
  const version = (kind, id, payload = {}) => ({ id, projectId: 1, documentId: id, kind, version: 1, schemaVersion: `${kind}.v1`, contentHash: `${kind}-fixture-hash`, payload, changeReason: '已保存方案', createdAt: '2026-10-09T00:00:00Z' })
  const std = version('standard', 21, { schemaVersion: 'standard.v1', steps: [{ id: 'identify', title: '识别条件', detail: '确认条件来源', checkpoint: '条件可验证', order: 1 }] })
  const payload = { schemaVersion: 'blueprint.v1', nodes: {
    coverage: { coverageVersionId: 11 }, standard: { standardVersionId: 21 },
    generation: { modelConnectionId: 1, modelVersion: 'fixture-model', schemaVersion: 'sft.sample.v1', maxTokens: 2000, temperature: 0.4, concurrency: 2, failurePolicy: 'retry_then_skip' },
    evaluation: { judgeConnectionIds: [2], rubricVersionId: 31, weights: { correctness: 1 }, samplingSeed: 7, missingScorePolicy: 'exclude' },
    rules: { qualityPolicyVersionId: 31 }, humanReview: { assignment: 'all', riskScope: 'all', sampleRate: 1, requiredEvidence: ['generation'] },
    delivery: { mappingVersionId: 41, format: 'jsonl', intendedUse: 'SFT 训练', limitations: ['需人工审阅'] },
  } }
  let current = { ...version('blueprint', 51, payload), version: 2 }
  const historical = { ...version('blueprint', 50, structuredClone(payload)), version: 1 }
  // The overview pointer intentionally differs from the first catalogue item.
  // This catches silently using the newest catalogue instead of the adopted head.
  const unadopted = { ...version('blueprint', 99, { ...payload, nodes: {} }), version: 9 }
  const docs = { coverage: version('coverage', 11), standard: std, 'quality-policy': version('quality-policy', 31), mapping: version('mapping', 41), source: version('source', 61) }
  const batch = { batchId: 73, resourceId: 'b_73', purpose: 'pilot', status: 'partial_failed', plannedUnits: 8, completedUnits: 5, failedUnits: 2, inFlightUnits: 1, shortfallUnits: 3, createdAt: '2026-10-09T00:00:00Z', capabilities: { canPause: false, canResume: false, canRetryFailed: true } }
  const detail = { batch, snapshot: { blueprintVersionId: 51, coverageVersionId: 11, standardVersionId: 21, qualityPolicyVersionId: 31, mappingVersionId: 41, blueprintContentHash: 'blueprint-fixture-hash', standardContentHash: 'standard-fixture-hash' }, steps: [{ phase: 'generation', unitLabel: '生成样本', status: 'partial_failed', doneUnits: 5, totalUnits: 8, failedUnits: 2 }], batchBudget: { currency: 'CNY', limitMinor: 500, reservedMinor: 10, settledMinor: 80, uncertainMinor: 0 } }
  const passed = (name, condition) => { check(`浏览器：${name}`, condition); results.push(name) }
  detail.generationConfig = structuredClone(payload.nodes.generation)
  detail.failureCount = 2
  detail.pendingJobs = 1
  page.on('pageerror', (error) => errors.push(error.message))
  try {
    assert.ok(metadata.items.length >= 7)
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const request = route.request()
      const pathname = new URL(request.url()).pathname.replace(/^\/api/, '')
      const send = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
      if (pathname === '/v1/auth/me') return send({ user: { id: 1, email: 'fixture@example.com', role: 'admin' } })
      if (pathname === '/v1/platform/runtime') return send({ api: 'ok', worker: 'ok', database: 'ok' })
      if (['/v1/admin/providers', '/v1/admin/storage-profiles', '/v1/admin/generation-strategies', '/v1/admin/prompts', '/v1/admin/audit-logs'].includes(pathname)) return send([])
      if (pathname === '/v1/admin/dashboard') return send({ providerCount: 0, activeProviderCount: 0, storageProfileCount: 0, strategyCount: 0, promptCount: 0, auditLogCount: 0 })
      if (pathname === '/v1/projects/1') return send(envelope({ name: '主线浏览器验收' }))
      if (pathname === '/v1/projects/1/overview') return send(envelope({ targetKind: 'sft', versions: { blueprint: { versionId: current.id } }, stats: { plannedQuestions: 12 } }, { canRun, canDesign: true }))
      if (pathname === '/v1/projects/1/blueprint-nodes') return send(metadata)
      if (pathname === '/v1/settings/connection-options') return send({ providers: [{ id: 1, name: '生成连接', model: 'fixture-model', isActive: true }, { id: 2, name: '独立裁判', model: 'fixture-judge', isActive: true }] })
      const doc = pathname.match(/^\/v1\/projects\/1\/(blueprint|coverage|standard|quality-policy|mapping|source)-versions(?:\/(\d+))?$/)
      if (doc) {
        const [, kind, selected] = doc
        if (request.method() === 'GET') {
          if (kind === 'blueprint') {
            if (selected && failBlueprintRead) return send({ error: { message: '蓝图服务暂时不可用' } }, 503)
            return selected ? send({ version: selected === '1' ? historical : current, readOnly: true }) : send({ items: [unadopted, current, historical], document: { revision }, canEdit: true })
          }
          return selected ? send({ version: docs[kind] }) : send({ items: [docs[kind]], document: { revision: 1 }, canEdit: true })
        }
        assert.equal(kind, 'blueprint', 'unexpected document write')
        const body = request.postDataJSON()
        writes.push({ body, key: request.headers()['idempotency-key'] })
        assert.equal(body.expectedRevision, revision)
        if (mode === '409') return send({ error: { code: 'REVISION_CONFLICT', message: '版本冲突' } }, 409)
        await new Promise((resolve) => setTimeout(resolve, 250))
        current = { ...current, id: current.id + 1, version: current.version + 1, payload: body.payload, changeReason: body.changeReason }
        revision += 1
        return send({ revision, data: { version: current } }, 201)
      }
      if (pathname === '/v1/projects/1/batches' && request.method() === 'POST') {
        batchCommands.push({ body: request.postDataJSON(), key: request.headers()['idempotency-key'] })
        if (failBatch) return send({ error: { message: '批次服务暂时不可用' } }, 503)
        return send({ data: { batchId: 73 } }, 201)
      }
      if (pathname === '/v1/projects/1/batches') return send({ items: [batch], nextCursor: '' })
      if (pathname === '/v1/projects/1/batches/b_73') return send(envelope(detail, batch.capabilities))
      if (pathname === '/v1/projects/1/batches/b_73/events') return send({ items: [{ id: 1, eventType: 'item_failed', createdAt: '2026-10-09T00:00:00Z', detail: { itemId: 2, errorClass: 'timeout', retryable: true }, errorClassLabel: '请求超时' }] })
      if (pathname === '/v1/projects/1/batches/b_73/analysis') return send(envelope({ sampleCount: 0, shortfallNote: '尚无分析结论' }))
      if (pathname === '/v1/projects/1/batches/b_73/failures') return send({ items: [] })
      if (pathname === '/v1/projects/1/batches/b_73/retry-failed') { controls.push({ pathname, method: request.method() }); return send({ data: { resetUnits: 2 } }) }
      if (pathname === '/v1/projects/1/samples') return send({ items: [], nextCursor: '' })
      if (/^\/v1\/(datasets|providers|storage-profiles|strategies|prompts|audit-logs)$/.test(pathname)) return send([])
      if (pathname === '/v1/legacy/migration-status') return send({ migrationComplete: false, legacyDatasets: 0, pendingDatasets: 0 })
      unknown.push(`${request.method()} ${pathname}`)
      return send({ error: { message: `Unmocked endpoint ${pathname}` } }, 501)
    })
    const capture = async (name) => {
      for (const width of [1440, 390]) {
        await page.setViewportSize({ width, height: width === 390 ? 844 : 960 })
        await page.evaluate(() => {
          if (document.activeElement instanceof HTMLElement) document.activeElement.blur()
          document.querySelectorAll('*').forEach((element) => { element.scrollTop = 0; element.scrollLeft = 0 })
          window.scrollTo(0, 0)
        })
        passed(`${name} ${width}px 无横向溢出`, await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth))
        await page.screenshot({ path: path.join(output, `${name}-${width}.png`), fullPage: true })
      }
      await page.setViewportSize({ width: 1440, height: 960 })
    }
    await page.goto(`${base}/p/1/blueprint?node=generation&step=dispatch`, { waitUntil: 'networkidle' })
    await page.locator('#blueprint-generation-concurrency').waitFor()
    passed('依赖图默认折叠，字段来自真实元数据', !await page.locator('.blueprint-dependency-diagram').evaluate((element) => element.open) && metadata.items.find((item) => item.key === 'generation').fields.some((field) => field.name === 'concurrency'))
    passed('高级并发字段可编辑且没有平行副本', await page.locator('#blueprint-generation-concurrency').count() === 1 && !await page.locator('#blueprint-generation-concurrency').isDisabled() && await page.locator('.blueprint-advanced-settings').evaluate((element) => element.open))
    await capture('design')
    mode = '409'
    await page.locator('#blueprint-generation-concurrency').fill('3')
    await page.getByRole('button', { name: '保存并继续', exact: true }).click()
    await page.getByText('版本已被其他人修改', { exact: false }).waitFor()
    passed('409 保留草稿并停留当前步骤', new URL(page.url()).searchParams.get('step') === 'dispatch' && await page.locator('#blueprint-generation-concurrency').inputValue() === '3')
    const failedWrites = writes.length
    await page.waitForTimeout(1400)
    passed('409 不自动重复写版本', writes.length === failedWrites)
    mode = 'success'
    await page.getByRole('button', { name: '保存并继续', exact: true }).click()
    await page.waitForURL(/node=evaluation/)
    passed('成功保存后继续且保持未编辑字段', current.payload.nodes.generation.concurrency === 3 && current.payload.nodes.generation.maxTokens === 2000 && current.payload.nodes.delivery.mappingVersionId === 41 && writes.every((write) => Boolean(write.key)))
    const beforeHistory = writes.length
    await page.goto(`${base}/p/1/blueprint?node=generation&step=dispatch&version=1`, { waitUntil: 'networkidle' })
    passed('历史版本字段与继续按钮均只读', await page.locator('#blueprint-generation-concurrency').isDisabled() && await page.getByRole('button', { name: '保存并继续', exact: true }).isDisabled())
    await page.waitForTimeout(1400)
    passed('历史浏览不产生新版本', writes.length === beforeHistory)
    await page.goto(`${base}/p/1/blueprint?node=delivery&step=use`, { waitUntil: 'networkidle' })
    await page.locator('#blueprint-delivery-intendedUse').fill('SFT 训练数据集')
    await page.getByRole('button', { name: '保存并进入试制', exact: true }).click()
    await page.waitForURL(/\/pilot$/)
    passed('最后设计步骤成功保存并进入试制', current.payload.nodes.delivery.intendedUse === 'SFT 训练数据集')
    await page.waitForLoadState('networkidle')
    await page.waitForFunction(() => document.querySelectorAll('[data-preflight-ok="false"]').length === 0 && document.querySelector('.planning-configuration-note strong')?.textContent?.includes('v'))
    await page.getByRole('button', { name: '启动试制批次', exact: true }).waitFor()
    passed('采用项目指针而非最新未采用目录项', await page.locator('.planning-configuration-note strong').innerText() === `当前方案 v${current.version}` && !await page.getByRole('button', { name: '启动试制批次', exact: true }).isDisabled())
    passed('版本覆盖默认折叠且数量预算前置', !await page.locator('.production-version-overrides').evaluate((element) => element.open) && await page.locator('#plan-units').isVisible() && await page.locator('#plan-budget').isVisible())
    await page.locator('#plan-units').fill('8')
    await page.locator('#plan-budget').fill('99')
    passed('不足最小预算禁止付费执行', await page.getByRole('button', { name: '启动试制批次', exact: true }).isDisabled())
    await page.locator('#plan-budget').fill('500')
    await capture('production-planning')
    await page.getByRole('button', { name: '启动试制批次', exact: true }).click()
    await page.getByText('批次服务暂时不可用', { exact: false }).waitFor()
    passed('创建失败保留数量预算与页面', /\/pilot$/.test(page.url()) && await page.locator('#plan-units').inputValue() === '8' && await page.locator('#plan-budget').inputValue() === '500')
    failBatch = false
    await page.getByRole('button', { name: '启动试制批次', exact: true }).click()
    await page.waitForURL(/\/runs\/b_73$/)
    assert.deepEqual(batchCommands[0].body, { purpose: 'pilot', blueprintVersionId: current.id, coverageVersionId: 11, standardVersionId: 21, qualityPolicyVersionId: 31, mappingVersionId: 41, unitCount: 8, budget: { currency: 'CNY', limitMinor: 500 } })
    passed('命令合同固定全部版本、数量预算并复用幂等键', batchCommands.length === 2 && Boolean(batchCommands[0].key) && batchCommands[0].key === batchCommands[1].key && JSON.stringify(batchCommands[0].body) === JSON.stringify(batchCommands[1].body))
    await page.locator('[data-batch-step="generation"]').waitFor()
    passed('阶段真实进度优先呈现且失败计数可见', await page.locator('[data-batch-step="generation"]').getAttribute('data-batch-step-status') === 'partial_failed' && await page.locator('progress').getAttribute('value') === '5' && await page.locator('progress').getAttribute('max') === '8' && await page.locator('[data-batch-shortfall]').isVisible())
    passed('分析、快照与事件默认折叠', await page.locator('.production-analysis-panel, .production-snapshot-panel, .production-events-panel').evaluateAll((elements) => elements.length === 3 && elements.every((element) => !element.open)))
    await capture('batch')
    const retryResponse = page.waitForResponse((response) => response.url().endsWith('/retry-failed'))
    await page.getByRole('button', { name: '恢复失败项', exact: true }).click()
    await retryResponse
    passed('恢复使用同一批次失败项端点，不创建新批次', controls.length === 1 && controls[0].method === 'POST' && batchCommands.length === 2)
    await page.getByRole('button', { name: '审阅产出', exact: true }).click()
    await page.waitForURL(/\/review$/)
    passed('产出直接进入审阅主线', /\/p\/1\/review$/.test(page.url()))
    await page.goto(`${base}/p/1/runs`, { waitUntil: 'networkidle' })
    await page.getByRole('button', { name: '处理失败', exact: true }).click()
    await page.waitForURL(/\/b_73\/failures$/)
    passed('列表失败动作直达具体批次异常恢复', /\/b_73\/failures$/.test(page.url()))
    failBlueprintRead = true
    await page.goto(`${base}/p/1/pilot`, { waitUntil: 'networkidle' })
    passed('蓝图读取失败不得用旧配置启动生产', await page.getByText('无法带入蓝图配置', { exact: false }).isVisible() && await page.getByRole('button', { name: '启动试制批次', exact: true }).isDisabled())
    failBlueprintRead = false; canRun = false
    await page.goto(`${base}/p/1/pilot`, { waitUntil: 'networkidle' })
    passed('无运行权限禁止生产', await page.getByRole('button', { name: '启动试制批次', exact: true }).isDisabled())
    assert.deepEqual(unknown, [], 'every browser API request must use an explicit fixture')
    passed('没有未捕获页面错误', errors.length === 0)
    writeFileSync(path.join(output, 'design-production-report.json'), JSON.stringify({ status: 'passed', checks: results, total: results.length, metadataSource: 'internal/model.BlueprintNodeSpecs (Go-exported typed field fixture)', limitations: ['API responses controlled; no worker/LLM execution or billing verified', 'Server idempotency persistence is not simulated; emitted retry command identity is verified'], artifacts: ['design', 'production-planning', 'batch'].flatMap((name) => [1440, 390].map((width) => `${name}-${width}.png`)) }, null, 2))
  } catch (error) {
    await page.screenshot({ path: path.join(output, 'design-production-failure.png'), fullPage: true }).catch(() => {})
    writeFileSync(path.join(output, 'design-production-report.json'), JSON.stringify({ status: 'failed', checks: results, total: results.length, currentVersion: current.version, writes, error: error.stack, pageErrors: errors, unknownEndpoints: unknown, body: await page.locator('body').innerText().catch(() => '') }, null, 2))
    throw error
  } finally { await browser.close() }
  console.log(`${results.length} Chromium design/production checks passed`)
}
