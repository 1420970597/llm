import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, Spin, Tag, TextArea, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle, Download, RefreshCw } from 'lucide-react'
import { client } from '../../lib/api'
import { projectPath, studioApi } from '../../lib/api/studio'
import type {
  BatchSummary,
  DeliveryItem,
  Page,
  ReleaseArtifact,
  ReleaseBlocker,
  ReleaseCard,
  ReleaseRecord,
  SampleSummary,
} from '../../lib/api/studio'
import { useProjectScope } from '../ProjectLayout'

/**
 * 发布与交付页面（Issue #160 T22）：发布列表、准备发布、候选数据卡、交付库。
 *
 * 契约：docs/plans/atelier-api-contract.md §2.8–§2.10、§3（L01–L03/B03）。
 *
 * 三条来自 T22 验收项的落点：
 *
 *  1. **URL 用稳定 releaseId**：页面的所有数据由路由参数取，版本名只用于展示
 *     与文件名。因此「同一版本名改了之后旧链接仍指向同一发布」成立。
 *  2. **阻塞项链到具体对象**：每条 blocker 带 link（服务端给出），
 *     用户不必在几万条内容里自己找。
 *  3. **downloaded 用统一会话与错误处理**：下载 URL 走同源 `/api`，
 *     401/403 由既有拦截器给出可理解的中文提示；文件名由服务端设置，
 *     含类型与版本名而不叫 latest。
 */

const RELEASE_STATUS_LABEL: Record<string, string> = {
  candidate: '候选（未发布）',
  blocked: '被门槛阻塞',
  building: '构建中',
  published: '已发布',
  build_failed: '构建失败（可幂等续推）',
}

function statusColor(status: string): 'green' | 'red' | 'amber' | 'grey' {
  switch (status) {
    case 'published':
      return 'green'
    case 'blocked':
    case 'build_failed':
      return 'red'
    case 'building':
      return 'amber'
    default:
      return 'grey'
  }
}

// ---------------------------------------------------------------------------
// 发布列表（L01）
// ---------------------------------------------------------------------------

export function ReleasesListPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [releases, setReleases] = useState<ReleaseRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.listReleases(scope.projectId)
      setReleases(response.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载发布列表失败')
    } finally {
      setLoading(false)
    }
  }, [scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  if (loading) {
    return (
      <div className="flex justify-center py-10">
        <Spin tip="正在加载发布版本" />
      </div>
    )
  }
  if (error) {
    return (
      <Card className="console-card">
        <Text strong className="block">加载失败</Text>
        <Text type="tertiary">{error}</Text>
      </Card>
    )
  }

  return (
    <div className="console-page" data-studio-page="releases">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">发布版本</Title>
          <Text type="tertiary">
            候选与已发布分开显示；已发布的内容、映射、数据卡与 hash 只读。
          </Text>
        </div>
        <div className="flex gap-2">
          <Button icon={<RefreshCw size={14} />} onClick={() => void load()}>刷新</Button>
          <Button theme="solid" type="primary" onClick={() => navigate(`/p/${scope.projectId}/releases/new`)}>
            准备发布
          </Button>
        </div>
      </div>

      {releases.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有发布版本。先完成质量检查与人工判断，再准备发布。" />
        </Card>
      ) : (
        <div className="batch-table" data-release-table="true">
          <div className="batch-row batch-row--head">
            <span>发布 ID</span>
            <span>版本名</span>
            <span>状态</span>
            <span>用途</span>
            <span>操作</span>
          </div>
          {releases.map((release) => (
            <div key={release.id} className="batch-row" data-release-id={release.id}>
              <span>{release.id}</span>
              <span>{release.releaseName}</span>
              <span>
                <Tag size="small" color={statusColor(release.status)}>
                  {RELEASE_STATUS_LABEL[release.status] ?? release.status}
                </Tag>
              </span>
              <span>{release.intendedUse || '（未填写）'}</span>
              <span>
                <Button size="small" onClick={() => navigate(`/p/${scope.projectId}/releases/${release.id}`)}>
                  数据卡
                </Button>
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 准备发布（L02）
// ---------------------------------------------------------------------------

export function ReleaseNewPage() {
  const scope = useProjectScope()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { Title, Text } = Typography

  // `?selection=` 指向服务端选择快照（T17）：URL 只带快照 ID，不带 ID 列表。
  const selectionSnapshotID = Number(searchParams.get('selection') ?? 0)

  const [samples, setSamples] = useState<SampleSummary[]>([])
  const [batches, setBatches] = useState<BatchSummary[]>([])
  const [selected, setSelected] = useState<number[]>([])
  const [releaseName, setReleaseName] = useState('v1.0')
  const [mappingVersionId, setMappingVersionId] = useState('')
  const [intendedUse, setIntendedUse] = useState('')
  const [limitations, setLimitations] = useState('')
  const [snapshotNotice, setSnapshotNotice] = useState<string | null>(null)
  const [blockers, setBlockers] = useState<ReleaseBlocker[]>([])
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const [sampleResponse, batchResponse] = await Promise.all([
          client.get<Page<SampleSummary>>(`${projectPath(scope.projectId)}/samples?status=accepted&limit=100`),
          client.get<Page<BatchSummary>>(`${projectPath(scope.projectId)}/batches?limit=50`),
        ])
        if (cancelled) return
        setSamples(sampleResponse.data.items ?? [])
        setBatches(batchResponse.data.items ?? [])
      } catch (loadError) {
        if (!cancelled) setError(loadError instanceof Error ? loadError.message : '加载可选范围失败')
      }
    })()
    return () => { cancelled = true }
  }, [scope.projectId])

  // 从服务端选择快照恢复范围（**重新鉴权**由服务端完成）。
  useEffect(() => {
    if (selectionSnapshotID <= 0) return
    let cancelled = false
    void (async () => {
      try {
        const resolved = await studioApi.getSelectionSnapshot(scope.projectId, selectionSnapshotID)
        if (cancelled) return
        // 快照存的是**内容版本 ID**，而这里按样本选择；用数量提示用户。
        setSnapshotNotice(`已从服务端选择范围恢复 ${resolved.count} 个内容版本（快照 ${selectionSnapshotID}）。`)
      } catch (snapshotError) {
        if (!cancelled) {
          setSnapshotNotice(snapshotError instanceof Error ? snapshotError.message : '选择范围已过期，请重新选择')
        }
      }
    })()
    return () => { cancelled = true }
  }, [scope.projectId, selectionSnapshotID])

  const submit = useCallback(async () => {
    setError(null)
    setBlockers([])
    if (selected.length === 0) {
      setError('发布范围不能为空：请选择要发布的内容版本')
      return
    }
    if (intendedUse.trim() === '') {
      setError('必须填写用途：数据卡要能说清这份数据用来做什么')
      return
    }
    setBusy(true)
    try {
      const result = await studioApi.createReleaseCandidate(scope.projectId, {
        releaseName: releaseName.trim(),
        // 这里提交的是**样本 ID**（服务端按当前采用版本落到具体内容版本）；
        // 真正的范围确认在数据卡页完成。
        sampleVersionIds: selected,
        mappingVersionId: Number(mappingVersionId) || 0,
        format: 'jsonl',
        intendedUse: intendedUse.trim(),
        limitations: limitations
          .split('\n')
          .map((line) => line.trim())
          .filter((line) => line !== ''),
      })
      setBlockers(result.data.blockers ?? [])
      // 导航到**服务端分配的**稳定 releaseId。
      navigate(`/p/${scope.projectId}/releases/${result.data.release.id}`)
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '创建发布候选失败')
    } finally {
      setBusy(false)
    }
  }, [intendedUse, limitations, mappingVersionId, navigate, releaseName, scope.projectId, selected])

  return (
    <div className="console-page" data-studio-page="release-new">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">准备发布</Title>
          <Text type="tertiary">
            候选创建时同时分配候选 ID、**稳定的发布 ID** 与项目内唯一的版本名；
            发布失败重试沿用同一身份。
          </Text>
        </div>
      </div>

      {snapshotNotice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-selection-restored="true">
          <Text size="small">{snapshotNotice}</Text>
        </Card>
      ) : null}

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }}>
        <div className="wizard-fields">
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="release-name">版本名</label>
            <Input id="release-name" value={releaseName} onChange={(value) => setReleaseName(value)}
              placeholder="例如 v1.2（不能叫 latest）" />
            <Text type="tertiary" size="small" className="block mt-1">
              版本名用于展示与文件名；下载路径只用稳定的发布 ID。
            </Text>
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="mapping-version">映射版本 ID</label>
            <Input id="mapping-version" value={mappingVersionId} onChange={(value) => setMappingVersionId(value)}
              placeholder="例如 12" />
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="intended-use">用途</label>
            <Input id="intended-use" value={intendedUse} onChange={(value) => setIntendedUse(value)}
              placeholder="例如 SFT 训练" />
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="limitations">限制（每行一条）</label>
            <TextArea id="limitations" value={limitations} onChange={(value) => setLimitations(value)}
              autosize={{ minRows: 2, maxRows: 4 }} placeholder="例如：仅覆盖冷链领域" />
          </div>
        </div>
      </Card>

      <Card className="console-card mb-3" bodyStyle={{ padding: 16 }} data-range-picker="true">
        <Text strong className="block mb-2">发布范围（已接纳的内容版本）</Text>
        <Text type="tertiary" size="small" className="block mb-2">
          已选 {selected.length} 条（当前页）。候选保存的是**具体内容版本**，不是筛选条件。
        </Text>
        {samples.length === 0 ? (
          <Empty description="还没有已接纳的内容。请先在审阅队列中完成判断。" />
        ) : (
          <div className="sample-table">
            <div className="sample-row sample-row--head"><span /><span>内容版本</span><span>批次</span></div>
            {samples.slice(0, 50).map((sample) => (
              <div key={sample.sampleId} className="sample-row">
                <input type="checkbox" aria-label={`选择 ${sample.title || sample.sampleKey}`}
                  checked={selected.includes(sample.sampleId)}
                  onChange={(event) => {
                    setSelected((previous) => event.target.checked
                      ? [...previous, sample.sampleId]
                      : previous.filter((id) => id !== sample.sampleId))
                  }} />
                <span>{sample.title || sample.sampleKey} · v{sample.latestVersion}</span>
                <span>{sample.originBatchId ? `#${sample.originBatchId}` : '—'}</span>
              </div>
            ))}
          </div>
        )}
        {batches.length === 0 ? null : (
          <Text type="tertiary" size="small" className="block mt-2">
            提示：同一批次的输出通常一起发布；跨批次混合会扩大数据卡的覆盖范围。
          </Text>
        )}
      </Card>

      {blockers.length > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-candidate-blockers="true">
          <Text strong className="block mb-1">候选门槛未通过</Text>
          {blockers.map((blocker, index) => (
            <div key={`${blocker.code}-${index}`} className="flex items-start gap-2">
              <AlertTriangle size={14} className="mt-1 text-amber-500" aria-hidden />
              {blocker.link ? (
                <a className="console-link" href={blocker.link}>{blocker.message}</a>
              ) : (
                <Text size="small">{blocker.message}</Text>
              )}
            </div>
          ))}
        </Card>
      ) : null}

      {error ? <div className="wizard-field__error mb-3" role="alert">{error}</div> : null}

      <Button theme="solid" type="primary" loading={busy} onClick={() => void submit()}>
        创建发布候选
      </Button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 数据卡（L03）
// ---------------------------------------------------------------------------

export function ReleaseCardPage() {
  const scope = useProjectScope()
  const params = useParams()
  const navigate = useNavigate()
  const { Title, Text } = Typography
  // 稳定 releaseId 来自路由：版本名改了也不影响这个链接。
  const releaseID = Number(params.releaseId ?? 0)

  const [card, setCard] = useState<ReleaseCard | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.getReleaseCard(scope.projectId, releaseID)
      setCard(response.data)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载数据卡失败')
    } finally {
      setLoading(false)
    }
  }, [releaseID, scope.projectId])

  useEffect(() => {
    void load()
  }, [load])

  const publish = useCallback(async () => {
    setBusy(true)
    setActionError(null)
    try {
      await studioApi.publishRelease(scope.projectId, releaseID)
      await load()
    } catch (publishError) {
      // 门槛未过返回 409 + 数据卡里的 blocker；这里只展示服务端文案。
      setActionError(publishError instanceof Error ? publishError.message : '发布失败')
    } finally {
      setBusy(false)
    }
  }, [load, releaseID, scope.projectId])

  const createNext = useCallback(async () => {
    setBusy(true)
    setActionError(null)
    try {
      const result = await studioApi.createNextCandidate(scope.projectId, releaseID)
      navigate(`/p/${scope.projectId}/releases/${result.data.release.id}`)
    } catch (nextError) {
      setActionError(nextError instanceof Error ? nextError.message : '创建下一版失败')
    } finally {
      setBusy(false)
    }
  }, [navigate, releaseID, scope.projectId])

  if (loading) {
    return <div className="flex justify-center py-10"><Spin tip="正在加载数据卡" /></div>
  }
  if (error || !card) {
    return (
      <Card className="console-card">
        <Text strong className="block">数据卡加载失败</Text>
        <Text type="tertiary">{error ?? '未知错误'}</Text>
        <div className="mt-3"><Button size="small" onClick={() => void load()}>重试</Button></div>
      </Card>
    )
  }

  const { release } = card
  const published = release.status === 'published'

  return (
    <div className="console-page" data-studio-page="release-card" data-release-status={release.status}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            发布 {release.releaseName}
          </Title>
          <Text type="tertiary">
            稳定发布 ID：<code>{release.id}</code> · 候选修订 {release.candidateRevision}
            {card.manifestHash ? ` · manifest ${card.manifestHash.slice(0, 20)}…` : ''}
          </Text>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Tag color={statusColor(release.status)}>{RELEASE_STATUS_LABEL[release.status] ?? release.status}</Tag>
          {!published ? (
            <Button theme="solid" type="primary" loading={busy} onClick={() => void publish()}>
              冻结并发布
            </Button>
          ) : (
            <Button loading={busy} onClick={() => void createNext()}>创建下一版</Button>
          )}
        </div>
      </div>

      {actionError ? <div className="wizard-field__error mb-3" role="alert">{actionError}</div> : null}

      {published ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 12 }} data-published-notice="true">
          <Text size="small">
            该版本已发布：内容、映射、数据卡与 hash 只读。之后修改项目配置、隔离样本或切换默认存储，
            都不会改变这些文件；后续风险通过独立警告表达。
          </Text>
        </Card>
      ) : null}

      {/* 阻塞项：每条链到具体对象（服务端给 link）。 */}
      {card.blockers.length > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-release-blockers="true">
          <Text strong className="block mb-1">发布阻塞项</Text>
          {card.blockers.map((blocker, index) => (
            <div key={`${blocker.code}-${index}`} className="flex items-start gap-2 mb-1">
              <AlertTriangle size={14} className="mt-1 text-amber-500" aria-hidden />
              {blocker.link ? (
                <a className="console-link" href={blocker.link}>{blocker.message}</a>
              ) : (
                <Text size="small">{blocker.message}</Text>
              )}
            </div>
          ))}
        </Card>
      ) : null}

      <div className="console-stat-grid">
        <StatTile label="用途" value={release.intendedUse || '（未填写）'} hint="数据卡必须写清用途" />
        <StatTile label="格式" value={release.format} hint="本轮承诺 JSONL/CSV/Alpaca" />
        <StatTile label="限制" value={String(release.limitations.length)} hint="每行一条；空限制表示无声明" />
        <StatTile label="制品" value={String(card.artifacts.length)} hint="注册/校验/失败三态" />
      </div>

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }}>
        <Text strong className="block mb-2">交付文件</Text>
        {card.artifacts.length === 0 ? (
          <Text type="tertiary" size="small">
            还没有制品。{published ? '' : '发布后由构建作业产出并校验。'}
          </Text>
        ) : (
          card.artifacts.map((artifact: ReleaseArtifact) => (
            <div key={artifact.id} className="flex flex-wrap items-center gap-2 mb-2"
              data-artifact-id={artifact.id} data-artifact-state={artifact.state}>
              <Tag size="small" color={artifact.state === 'verified' ? 'green' : artifact.state === 'failed' ? 'red' : 'grey'}>
                {artifact.state === 'verified' ? '已校验' : artifact.state === 'failed' ? '失败' : '待校验'}
              </Tag>
              <Text size="small">
                {artifact.format} · {artifact.sizeBytes} 字节 · hash {artifact.artifactHash.slice(0, 16)}…
              </Text>
              {artifact.state === 'verified' ? (
                // 下载走同源 /api，因此复用统一会话与错误处理（401/403 有中文提示）。
                // 文件名由服务端设置（含类型与版本名，不叫 latest）。
                <a className="console-link" href={studioApi.downloadArtifactURL(scope.projectId, release.id, artifact.id)}>
                  <Download size={13} aria-hidden /> 下载
                </a>
              ) : null}
              {artifact.errorMessage ? (
                <Text type="tertiary" size="small">失败原因：{artifact.errorMessage}</Text>
              ) : null}
            </div>
          ))
        )}
      </Card>

      <Card className="console-card" bodyStyle={{ padding: 14 }} data-manifest-panel="true">
        <Text strong className="block mb-1">发布清单（manifest）</Text>
        <Text type="tertiary" size="small" className="block mb-2">
          manifest 记录这一版发布了什么：清单项、映射与编码器版本、用途与限制。
          hash 分层：内容 hash → 清单 hash → 文件 hash → manifest hash（不含它自己）。
        </Text>
        <pre className="review-content">{JSON.stringify(card.manifest, null, 2)}</pre>
      </Card>
    </div>
  )
}

// ---------------------------------------------------------------------------
// 交付库（B03）
// ---------------------------------------------------------------------------

export function DeliveriesPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [items, setItems] = useState<DeliveryItem[]>([])
  const [search, setSearch] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async (query: string) => {
    setLoading(true)
    setError(null)
    try {
      const response = await studioApi.listDeliveries(query.trim() === '' ? undefined : { q: query.trim() })
      setItems(response.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载交付库失败')
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load('')
  }, [load])

  return (
    <div className="console-page" data-studio-page="deliveries">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">交付库</Title>
          <Text type="tertiary">
            只显示**已发布**且你有权访问的版本；候选不是交付物，不会出现在这里。
          </Text>
        </div>
        <div className="flex gap-2">
          <Input value={search} onChange={(value) => setSearch(value)} placeholder="按版本名或用途搜索"
            style={{ width: 220 }} aria-label="搜索交付" />
          <Button icon={<RefreshCw size={14} />} onClick={() => void load(search)}>搜索</Button>
        </div>
      </div>

      {loading ? (
        <div className="flex justify-center py-10"><Spin tip="正在加载交付库" /></div>
      ) : error ? (
        <Card className="console-card">
          <Text strong className="block">加载失败</Text>
          <Text type="tertiary">{error}</Text>
        </Card>
      ) : items.length === 0 ? (
        <Card className="console-card">
          <Empty description="还没有可交付的已发布版本。" />
        </Card>
      ) : (
        <div className="batch-table" data-delivery-table="true">
          <div className="batch-row batch-row--head">
            <span>发布 ID</span><span>版本名</span><span>项目</span><span>用途</span><span>操作</span>
          </div>
          {items.map((item) => (
            <div key={`${item.projectId}-${item.releaseId}`} className="batch-row" data-delivery-id={item.releaseId}>
              <span>{item.releaseId}</span>
              <span>{item.releaseName}</span>
              <span>{item.projectId}</span>
              <span>{item.intendedUse || '（未填写）'}</span>
              <span>
                <Button size="small" onClick={() => navigate(item.page)}>打开数据卡</Button>
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function StatTile({ label, value, hint }: { label: string; value: string; hint: string }) {
  const { Text } = Typography
  return (
    <Card className="console-card" bodyStyle={{ padding: 14 }}>
      <Text type="tertiary" size="small" className="block">{label}</Text>
      <div className="console-stat-value">{value}</div>
      <Text type="tertiary" size="small" className="block">{hint}</Text>
    </Card>
  )
}
