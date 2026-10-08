/** Real isolated-stack acceptance. No provider responses are mocked.
 * Run in a Node container with Playwright and explicit JOURNEY_URL/provider IDs.
 * Creates test projects and an estimated provider price only in that isolated
 * stack. Credentials are never saved. Automatic decisions test the workflow;
 * they do not constitute human acceptance or independent quality evaluation.
 */
import { createRequire } from 'node:module'
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import assert from 'node:assert/strict'

const require = createRequire(import.meta.url)
const { request, chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright')
const baseURL = process.env.JOURNEY_URL
assert(baseURL, 'JOURNEY_URL must point to an isolated acceptance stack')
const webURL = process.env.JOURNEY_WEB_URL || baseURL
const providerID = Number(process.env.JOURNEY_PROVIDER_ID)
assert(providerID > 0, 'A real provider ID is required')
const generationMaxTokens = Number(process.env.JOURNEY_MAX_TOKENS || 8192)
assert(Number.isInteger(generationMaxTokens) && generationMaxTokens > 0, 'JOURNEY_MAX_TOKENS must be a positive integer')
const api = await request.newContext({ baseURL, timeout: 30000 })
const runID = new Date().toISOString().replace(/[:.]/g, '-')
const evidence = { runID, baseURL, webURL, generationMaxTokens, sourceRevision: process.env.JOURNEY_SHA || 'working-tree', productSourceRunID: runID, projects: [], checks: [] }
const outputDir = `output/playwright/grounding-${runID}`
mkdirSync(outputDir, { recursive: true })
if (process.env.JOURNEY_RESUME_EVIDENCE) {
  const previous = JSON.parse(readFileSync(process.env.JOURNEY_RESUME_EVIDENCE, 'utf8'))
  assert.equal(previous.baseURL, baseURL, 'Resume evidence must belong to this isolated API')
  const sft = previous.projects.find(project => project.targetKind === 'sft' && project.releaseID && project.artifactHash)
  assert(sft, 'Resume evidence must contain a previously verified SFT release')
  evidence.projects.push(structuredClone(sft))
  evidence.resumedFromRun = previous.runID
  evidence.reusedSFTFromRun = previous.reusedSFTFromRun || previous.runID
  evidence.reusedSFTScope = previous.reusedSFTScope || { sourceRevision: previous.sourceRevision, generationMaxTokens: previous.generationMaxTokens }
  evidence.productSourceRunID = previous.productSourceRunID || previous.runID
  evidence.checks.push(...previous.checks.filter(check => check.startsWith('sft:')))
}

async function call(method, path, data, expected = [200, 201, 202]) {
  const digest = createHash('sha256').update(`${path}:${JSON.stringify(data)}`).digest('hex')
  const response = await api.fetch(path, { method, ...(data === undefined ? {} : { data }), headers: { 'Idempotency-Key': `${runID}-${method}-${digest}` } })
  const body = await response.json()
  assert(expected.includes(response.status()), `${method} ${path}: ${response.status()} ${JSON.stringify(body)}`)
  return body
}
async function waitFor(path, statuses, label, maxSeconds = 900) {
  const deadline = Date.now() + maxSeconds * 1000
  let last
  let previousStatus
  while (Date.now() < deadline) {
    last = await call('GET', path)
    const status = last.data?.batch?.status || last.data?.release?.status || last.status || last.data?.status
    assert(status, `${label}: response contains no resource status`)
    if (status !== previousStatus) console.log(`${label}: ${status}`)
    previousStatus = status
    if (statuses.includes(status)) return last
    if (['failed', 'partial_failed', 'build_failed', 'paused', 'cancelled'].includes(status)) {
      throw new Error(`${label}: ${JSON.stringify(last)}`)
    }
    await new Promise(resolve => setTimeout(resolve, 4000))
  }
  throw new Error(`${label} timed out: ${JSON.stringify(last)}`)
}
async function document(projectID, kind) {
  const result = await call('GET', `/api/v1/projects/${projectID}/${kind}-versions`)
  assert(result.items.length > 0, `${kind} was bootstrapped`)
  return { ...result.items[0], revision: result.document.revision }
}
async function save(projectID, kind, old, payload) {
  const saved = await call('POST', `/api/v1/projects/${projectID}/${kind}-versions`, {
    expectedRevision: old.revision, payload, changeReason: '真实隔离验收：冻结素材与生成配置',
  })
  return saved.data.version
}
async function runTarget(targetKind) {
  const created = await call('POST', '/api/v1/projects', {
    name: `素材接地验收-${targetKind}-${runID}`, targetKind,
    goal: '根据北辰仓库退货政策生成可核查的培训问题', pilotSize: 1,
    coverage: { domains: 1, directionsPerDomain: 1, questionsPerDirection: 1 },
    budget: { currency: 'CNY', limitMinor: 10000, onExhausted: 'pause' },
  })
  const projectID = Number((created.data.project || created.data).id)
  const prefix = `/api/v1/projects/${projectID}`
  const result = { projectID, targetKind }
  evidence.projects.push(result)
  console.log(`Created isolated ${targetKind} project ${projectID}`)
  const sourceList = await call('GET', `${prefix}/source-versions`)
  const sourceText = '# 北辰仓库退货政策\n\n北辰仓库接受到货后17天内提出的未拆封退货。冷链商品不接受普通退货，温控异常须在签收后6小时内上传温度记录，由质量组审核。普通退货须保留批次码，客服在2个工作日内响应。退款在仓库验收通过后3个工作日内原路退回。订单超过800元的普通退货提供一次免费取件，其他订单由客户承担运费。\n\n# 特殊情况\n\n包装损坏应在签收时拍照。缺少批次码的商品先隔离，不直接入可售库存。客服不得承诺绕过质量审核。'
  const uploadData = {
    file: { name: 'beichen-policy.md', mimeType: 'text/markdown', buffer: Buffer.from(sourceText) },
    expectedRevision: String(sourceList.document?.revision || 0), sourceKey: `${runID}-${targetKind}-source`,
    changeReason: '真实素材生成验收', 'chunking.minLength': '1', 'chunking.maxLength': '2000',
  }
  const upload = await api.post(`${prefix}/source-imports`, { multipart: uploadData })
  assert.equal(upload.status(), 202)
  const queued = await upload.json()
  const imported = await waitFor(`${prefix}/source-imports/${queued.importId}`, ['completed'], 'source import')
  assert.equal(imported.counts.failedItems, 0)
  const replay = await api.post(`${prefix}/source-imports`, { multipart: uploadData })
  assert.equal(replay.status(), 200)
  assert.equal((await replay.json()).replay, true)
  evidence.checks.push(`${targetKind}:source-import-and-replay`)
  const source = await document(projectID, 'source')
  const chunkIDs = source.payload.documents.flatMap(item => item.chunkIds)
  assert(chunkIDs.length > 0)
  result.sourceVersionID = source.id
  result.sourceChunkIDs = chunkIDs
  const coverageOld = await document(projectID, 'coverage')
  const coverage = await save(projectID, 'coverage', coverageOld, {
    schemaVersion: 'coverage.v1', domains: [{ stableId: 'returns', name: '北辰仓库退货政策', directions: [{
      stableId: 'cold-chain', name: '退货、冷链异常和退款时限', quota: 1,
      difficultyRatios: [{ difficulty: 'medium', ratio: 100 }], source: 'document', sourceChunkIds: chunkIDs,
    }] }],
  })
  const blueprintOld = await document(projectID, 'blueprint')
  const blueprintPayload = structuredClone(blueprintOld.payload)
  blueprintPayload.nodes.coverage.coverageVersionId = coverage.id
  Object.assign(blueprintPayload.nodes.generation, { sourceVersionId: source.id, modelConnectionId: providerID, concurrency: 1, maxTokens: generationMaxTokens })
  if (targetKind === 'grpo') {
    blueprintPayload.nodes.generation.jsonSchema = { ...blueprintPayload.nodes.generation.jsonSchema, levels: ['不合格', '合格', '优秀'] }
    result.levels = [...blueprintPayload.nodes.generation.jsonSchema.levels]
  }
  const blueprint = await save(projectID, 'blueprint', blueprintOld, blueprintPayload)
  const batch = await call('POST', `${prefix}/batches`, { purpose: 'pilot', unitCount: 1, blueprintVersionId: blueprint.id })
  result.batchID = Number(batch.id.replace(/^b_/, ''))
  await waitFor(`${prefix}/batches/b_${result.batchID}`, ['completed'], 'real model generation')
  const listed = await call('GET', `${prefix}/samples?status=all&limit=20`)
  assert.equal(listed.items.length, 1)
  const sampleID = listed.items[0].resourceId || `s_${listed.items[0].sampleId}`
  const detail = await call('GET', `${prefix}/samples/${sampleID}`)
  const version = detail.data.version || detail.data.currentVersion
  assert(version, 'Sample detail contains the immutable content version')
  assert.deepEqual([...version.source.sourceChunkIds].sort(), [...chunkIDs].sort())
  assert.equal(version.payload.source, 'document')
  assert(!/：第\s*\d+\s*题$/.test(version.payload.question), 'Question is generated by the real model')
  if (targetKind === 'sft') {
    assert(!/^[.\s…]+$/.test(version.payload.answer), 'Final answer must not be an ellipsis placeholder')
    assert(!/第\s*1\s*步\s*(?:\.{3}|…)[\s\S]*第\s*2\s*步\s*(?:\.{3}|…)/.test(version.payload.reasoning), 'Reasoning must not come from an internal format example')
  }
  result.sampleID = sampleID
  result.sampleVersionID = version.versionId
  result.contentHash = version.contentHash
  evidence.checks.push(`${targetKind}:real-generated-content-and-source-ids`)
  const sameSource = await call('POST', `${prefix}/experiments`, {
    sampleVersionIds: [version.versionId], judgeConnectionIds: [providerID], judgeMaxTokens: 1024,
    samplingSeed: 42, missingScorePolicy: 'exclude',
    ...(targetKind === 'sft' ? { rubric: { dimensions: [{ key: 'accuracy', label: '事实准确性', weight: 1, min: 0, max: 10 }] } } : {}),
  }, [422])
  assert(sameSource.error?.message.includes('同源'), 'Valid same-source experiment must be rejected for judge independence')
  evidence.checks.push(`${targetKind}:same-source-judge-rejected`)
  const policy = await document(projectID, 'quality-policy')
  await call('POST', `${prefix}/rule-previews`, { qualityPolicyVersionId: policy.id, sampleVersionIds: [version.versionId], maxHits: 20 })
  const mapping = await document(projectID, 'mapping')
  const pendingCandidate = await call('POST', `${prefix}/releases`, {
    releaseName: `pending-${runID}`, sampleVersionIds: [version.versionId], mappingVersionId: mapping.id,
    format: 'jsonl', intendedUse: '隔离验收：验证未审阅样本阻止发布', limitations: ['技术验收'], provenance: { runID },
  })
  const pendingCard = await call('GET', `${prefix}/releases/${pendingCandidate.id}`)
  assert(pendingCard.data.blockers.some(item => item.code === 'PENDING_REVIEW'))
  await call('POST', `${prefix}/releases/${pendingCandidate.id}/publish`, {}, [409])
  evidence.checks.push(`${targetKind}:pending-review-blocks-publication`)
  const review = await call('GET', `${prefix}/samples/${sampleID}/versions/${version.version}/decisions`)
  const projection = review.projection || review.data?.projection || detail.data.projection
  await call('POST', `${prefix}/samples/${sampleID}/versions/${version.version}/decisions`, {
    action: 'accepted', reason: '自动化技术验收：仅检查内容结构、素材引用与交付完整性；不是人工验收或独立质量评估。',
    evidenceRevision: projection?.evidenceRevision || 0, reviewerRevision: 1,
  })
  const release = await call('POST', `${prefix}/releases`, {
    releaseName: `grounding-${runID}`, sampleVersionIds: [version.versionId], mappingVersionId: mapping.id,
    format: 'jsonl', intendedUse: '隔离验收，非生产训练数据',
    limitations: ['单样本技术验收；未进行真人可用性验收或真实独立裁判评估'], provenance: { runID },
  })
  result.releaseID = Number(release.id)
  await call('POST', `${prefix}/releases/${release.id}/publish`, {})
  const card = await waitFor(`${prefix}/releases/${release.id}`, ['published'], 'release job', 180)
  assert.equal(card.data.manifest.grounding.groundedSamples, 1)
  assert.equal(card.data.manifest.grounding.missing, 0)
  const artifact = card.data.artifacts.find(item => item.state === 'verified')
  assert(artifact, 'Published artifact is verified')
  const download = await api.get(`${prefix}/releases/${release.id}/artifacts/${artifact.id}/download`)
  assert.equal(download.status(), 200)
  const bytes = await download.body()
  assert.equal(`sha256:${createHash('sha256').update(bytes).digest('hex')}`, artifact.artifactHash)
  writeFileSync(`${outputDir}/${targetKind}.jsonl`, bytes)
  const exported = JSON.parse(bytes.toString('utf8').trim())
  assert.equal(exported.question, version.payload.question)
  if (targetKind === 'grpo') {
    assert(Array.isArray(exported.levels) && exported.levels.length >= 2)
    assert(Array.isArray(exported.level_rubrics))
  } else assert(exported.reasoning && exported.answer)
  result.artifactHash = artifact.artifactHash
  result.budget = await call('GET', `${prefix}/budget`)
  writeFileSync(`${outputDir}/${targetKind}-manifest.json`, JSON.stringify(card.data.manifest, null, 2))
  evidence.checks.push(`${targetKind}:published-download-hash-and-typed-fields`)
  console.log(`PASS ${targetKind} project=${projectID} sample=${sampleID} release=${release.id}`)
}

async function verifyProducts() {
  const project = evidence.projects.find(item => item.targetKind === 'sft')
  const prefix = `/api/v1/projects/${project.projectID}`
  const formats = {
    alpaca: [{ instruction: '产物验收：怎样保留退货批次码？', input: '', output: '记录原批次码，缺码先隔离。' }, { instruction: 12, output: '无效字段应记录到第二行。' }],
    sharegpt: [{ conversations: [{ from: 'human', value: '产物验收：冷链异常怎样上传证据？' }, { from: 'gpt', value: '签收后6小时内上传温度记录。' }] }],
    jsonl: [{ question: '产物验收：何时退款？', reasoning: '以验收通过时间为起点。', answer: '验收通过后3个工作日内。' }],
  }
  for (const [format, rows] of Object.entries(formats)) {
    const payload = { format, sourceKey: `${evidence.productSourceRunID}-${format}`, targetKind: 'sft', content: rows.map(row => JSON.stringify(row)).join('\n'), changeReason: 'EasyDataset公开产物格式真实导入验收' }
    const preview = await call('POST', `${prefix}/source-import-products/preview`, payload)
    assert.equal(preview.validItems, 1)
    const queued = await call('POST', `${prefix}/source-import-products`, payload)
    const imported = await waitFor(`${prefix}/source-imports/${queued.importId}`, ['completed'], `${format} import`, 180)
    assert.equal(imported.counts.importedVersions, 1)
    assert.equal(imported.counts.failedItems, format === 'alpaca' ? 1 : 0)
    if (format === 'alpaca') assert.equal(imported.failures[0].sourceId, 2)
    const replay = await call('POST', `${prefix}/source-import-products`, payload, [200])
    assert.equal(replay.replay, true)
    assert.equal(replay.importId, queued.importId)
    evidence.checks.push(`${format}:product-preview-import-replay-and-failures`)
  }
  const samples = await call('GET', `${prefix}/samples?status=all&limit=20`)
  assert.equal(samples.items.length, 4)
  for (const item of samples.items.filter(item => item.resourceId !== project.sampleID)) {
    const detail = await call('GET', `${prefix}/samples/${item.resourceId}`)
    assert.equal(detail.data.version.payload.source, 'external_import')
    assert.equal(detail.data.version.source.sourceChunkIds?.length || 0, 0)
  }
  const card = await call('GET', `${prefix}/releases/${project.releaseID}`)
  const artifact = card.data.artifacts.find(item => item.state === 'verified')
  assert.equal(artifact.artifactHash, project.artifactHash)
  assert.equal(card.data.manifest.itemCount, 1)
  const downloaded = await api.get(`${prefix}/releases/${project.releaseID}/artifacts/${artifact.id}/download`)
  assert.equal(downloaded.status(), 200)
  const bytes = await downloaded.body()
  assert.equal(`sha256:${createHash('sha256').update(bytes).digest('hex')}`, project.artifactHash)
  writeFileSync(`${outputDir}/sft.jsonl`, bytes)
  writeFileSync(`${outputDir}/sft-manifest.json`, JSON.stringify(card.data.manifest, null, 2))
  evidence.checks.push('external-imports-do-not-change-frozen-release-or-claim-grounding')
}

try {
  await call('POST', '/api/v1/auth/login', { email: process.env.JOURNEY_EMAIL || 'admin@company.com', password: process.env.JOURNEY_PASSWORD || 'admin123456' })
  await call('PUT', `/api/v1/settings/model-prices/${providerID}`, {
    priceVersion: `acceptance-estimate-${runID}`, inputPriceMinorPerMillion: 1000,
    outputPriceMinorPerMillion: 1000, isFree: false, isEstimated: true,
    note: '隔离技术验收保守估计，非官方价格或实际账单',
  })
  if (!evidence.projects.some(project => project.targetKind === 'sft' && project.artifactHash)) await runTarget('sft')
  // Product import and the independent GRPO project can finish even when the
  // other phase fails. Stable source keys make resumed imports idempotent.
  const phases = await Promise.allSettled([runTarget('grpo'), verifyProducts()])
  evidence.phaseErrors = phases.flatMap((phase, index) => phase.status === 'rejected' ? [{ phase: ['grpo', 'products'][index], error: phase.reason.message }] : [])
  if (evidence.phaseErrors.length) throw new Error(evidence.phaseErrors.map(item => `${item.phase}: ${item.error}`).join('\n'))
  const browser = await chromium.launch({ headless: true, args: ['--no-sandbox'] })
  try {
    const context = await browser.newContext({ storageState: await api.storageState(), viewport: { width: 1440, height: 1024 } })
    const page = await context.newPage()
    for (const project of evidence.projects) {
      await page.goto(`${webURL}/p/${project.projectID}/releases/${project.releaseID}`)
      await page.locator('[data-grounding-summary="true"]').waitFor({ timeout: 15000 })
      await page.screenshot({ path: `${outputDir}/${project.targetKind}-release.png`, fullPage: true })
    }
  } finally { await browser.close() }
  evidence.result = 'passed'
} catch (error) {
  evidence.result = 'failed'
  evidence.error = error.message
  throw error
} finally {
  writeFileSync(`${outputDir}/evidence.json`, JSON.stringify(evidence, null, 2))
  await api.dispose()
  console.log(`Evidence: ${outputDir}/evidence.json`)
}
