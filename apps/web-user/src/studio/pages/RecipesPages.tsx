import { useCallback, useEffect, useMemo, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { Button, Card, Empty, Input, Select, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { AlertTriangle } from 'lucide-react'
import { recipeApi } from '../../lib/api/studio'
import type { Recipe, RecipeCopyResult, RecipeDetail, RecipeVersion } from '../../lib/api/studio'
import { projectHref } from '../StudioLayout'

/**
 * 方案库页面（Issue #160 T26）：列表与详情（版本、发布、以方案创建项目）。
 *
 * 三条与契约直接相关的界面决定：
 *
 *  1. **只有 published 版本可以拿去建项目**。列表把「已发布版本」单独显示，
 *     因为用户关心的是「能不能用」，而不是「改过几版」。草稿版本在详情页
 *     只提供「发布」，不提供「用它建项目」——把不可用的动作画在界面上，
 *     用户点下去只会拿到一个 409。
 *  2. **复制结果必须回显**：缺连接（生成/裁判）与 rubric 引用被清空都是
 *     「项目建出来但还需要补配置」的事实。不显示它们会让用户在点「开始试制」
 *     时才遇到失败，而那时他刚建完项目、以为一切就绪。
 *  3. **可见范围如实显示**：private 只对创建者可见（包括工作区管理员），
 *     界面上要写清楚，否则用户会以为「同事都能看到」。
 */

function targetKindLabel(targetKind: string): string {
  return targetKind === 'grpo' ? 'GRPO' : 'SFT'
}

function visibilityLabel(visibility: string): string {
  return visibility === 'private' ? '仅自己可见' : '工作区可见'
}

function versionStatusLabel(status: string): string {
  return status === 'published' ? '已发布' : '草稿'
}

// ---------------------------------------------------------------------------
// 方案列表（B01）
// ---------------------------------------------------------------------------

export function RecipesListPage() {
  const navigate = useNavigate()
  const { Title, Text } = Typography
  const [recipes, setRecipes] = useState<Recipe[]>([])
  const [targetKind, setTargetKind] = useState('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const page = await recipeApi.list({ targetKind: targetKind || undefined, limit: 100 })
      setRecipes(page.items ?? [])
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载方案库失败')
    } finally {
      setLoading(false)
    }
  }, [targetKind])

  useEffect(() => {
    void load()
  }, [load])

  return (
    <div className="console-page" data-studio-page="recipes">
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            方案库
          </Title>
          <Text type="tertiary">
            把验证过的一套配置（覆盖 / 标准 / 质量策略 / 蓝图 / 映射）保存成方案，直接复制到新项目。
          </Text>
        </div>
        <Select
          value={targetKind || undefined}
          placeholder="全部类型"
          style={{ width: 160 }}
          aria-label="按适用类型筛选"
          optionList={[
            { value: 'sft', label: 'SFT' },
            { value: 'grpo', label: 'GRPO' },
          ]}
          onChange={(value) => setTargetKind(String(value ?? ''))}
        />
      </div>

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-recipes-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}

      {loading ? (
        <div className="flex justify-center py-10">
          <Spin tip="正在加载方案" />
        </div>
      ) : recipes.length === 0 ? (
        <Card className="console-card" bodyStyle={{ padding: 24 }} data-recipes-empty-state="true">
          <Empty
            description={(
              <div className="flex flex-col items-center gap-2">
                <Text>方案库暂时没有可用方案</Text>
                <Text type="tertiary" size="small">
                  从既有项目另存为方案尚未开放；当前可以先在数据项目中维护蓝图与版本。
                </Text>
                <Button size="small" theme="solid" type="primary" onClick={() => navigate('/projects')} data-recipes-empty-cta="true">
                  打开数据项目
                </Button>
              </div>
            )}
          />
        </Card>
      ) : (
        <div className="comparison-table" data-recipe-table="true">
          <div className="comparison-row comparison-row--head">
            <span>方案</span>
            <span>适用类型</span>
            <span>可见范围</span>
            <span>已发布版本</span>
            <span>最新版本</span>
            <span>更新时间</span>
          </div>
          {recipes.map((recipe) => (
            <div
              key={recipe.id}
              className="comparison-row"
              data-recipe-id={recipe.id}
              role="button"
              tabIndex={0}
              onClick={() => navigate(`/recipes/${recipe.id}`)}
              onKeyDown={(event) => {
                if (event.key === 'Enter' || event.key === ' ') navigate(`/recipes/${recipe.id}`)
              }}
            >
              <span>
                {recipe.name}
                {recipe.description ? (
                  <Text type="tertiary" size="small" className="block">
                    {recipe.description}
                  </Text>
                ) : null}
              </span>
              <span>
                <Tag size="small" color={recipe.targetKind === 'grpo' ? 'violet' : 'blue'}>
                  {targetKindLabel(recipe.targetKind)}
                </Tag>
              </span>
              <span>{visibilityLabel(recipe.visibility)}</span>
              <span>
                {recipe.publishedVersion > 0 ? `v${recipe.publishedVersion}` : '—（尚不可用于创建项目）'}
              </span>
              <span>v{recipe.latestVersion}</span>
              <span>{new Date(recipe.updatedAt).toLocaleString()}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

// ---------------------------------------------------------------------------
// 方案详情（B02）
// ---------------------------------------------------------------------------

export function RecipeDetailPage() {
  const { recipeId } = useParams()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { Title, Text } = Typography
  const [detail, setDetail] = useState<RecipeDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [notice, setNotice] = useState<string | null>(null)
  const [copyResult, setCopyResult] = useState<RecipeCopyResult | null>(null)
  const [createdProjectID, setCreatedProjectID] = useState<number | null>(null)
  const [projectName, setProjectName] = useState('')
  const [projectGoal, setProjectGoal] = useState('')

  const parsedRecipeID = Number.parseInt(recipeId ?? '', 10)
  const requestedVersionID = Number.parseInt(searchParams.get('version') ?? '', 10)

  const load = useCallback(async () => {
    if (!Number.isFinite(parsedRecipeID) || parsedRecipeID <= 0) {
      setError('方案地址不正确')
      setLoading(false)
      return
    }
    setLoading(true)
    setError(null)
    try {
      const response = await recipeApi.get(parsedRecipeID)
      setDetail(response)
      setProjectName(response.data.recipe.name + ' 副本')
      setProjectGoal(response.data.recipe.applicableScope || response.data.recipe.description || '')
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载方案失败')
    } finally {
      setLoading(false)
    }
  }, [parsedRecipeID])

  useEffect(() => {
    void load()
  }, [load])

  const publishedVersions = useMemo(
    () => (detail?.data.versions ?? []).filter((version) => version.status === 'published'),
    [detail],
  )
  const selectedVersion = useMemo(() => {
    if (requestedVersionID > 0) {
      return publishedVersions.find((version) => version.id === requestedVersionID) ?? null
    }
    return publishedVersions[0] ?? null
  }, [publishedVersions, requestedVersionID])

  const publish = useCallback(
    async (version: RecipeVersion) => {
      setBusy(true)
      setError(null)
      setNotice(null)
      try {
        await recipeApi.publishVersion(parsedRecipeID, version.version)
        setNotice(`已发布 v${version.version}。已发布版本才能用于创建项目。`)
        await load()
      } catch (publishError) {
        setError(publishError instanceof Error ? publishError.message : '发布失败')
      } finally {
        setBusy(false)
      }
    },
    [load, parsedRecipeID],
  )

  const createProject = useCallback(async () => {
    if (!detail) return
    if (!selectedVersion) {
      setError('该方案还没有已发布版本：请先发布一个版本，再用于创建项目。')
      return
    }
    if (projectName.trim() === '') {
      setError('请填写新项目名称')
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    setCopyResult(null)
    try {
      const response = await recipeApi.createProjectFromRecipe({
        sourceRecipeVersionId: selectedVersion.id,
        name: projectName.trim(),
        goal: projectGoal.trim(),
        targetKind: detail.data.recipe.targetKind,
      })
      setCreatedProjectID(response.data.project.id)
      setCopyResult(response.data.recipeCopy ?? null)
    } catch (createError) {
      setError(createError instanceof Error ? createError.message : '创建项目失败')
    } finally {
      setBusy(false)
    }
  }, [detail, projectGoal, projectName, selectedVersion])

  if (loading) {
    return (
      <div className="flex justify-center py-10" data-studio-page="recipe-detail">
        <Spin tip="正在加载方案" />
      </div>
    )
  }

  if (error && !detail) {
    return (
      <div className="console-page" data-studio-page="recipe-detail">
        <Card className="console-card" bodyStyle={{ padding: 24 }} data-recipe-error="true">
          <Title heading={5} className="!mb-1">
            这个方案打不开
          </Title>
          <Text type="tertiary">{error}</Text>
        </Card>
      </div>
    )
  }

  if (!detail) return null
  const recipe = detail.data.recipe

  return (
    <div className="console-page" data-studio-page="recipe-detail" data-recipe-id={recipe.id}>
      <div className="console-page__header">
        <div>
          <Title heading={4} className="!mb-1">
            {recipe.name}
          </Title>
          <Text type="tertiary">
            {targetKindLabel(recipe.targetKind)} · {visibilityLabel(recipe.visibility)} · 共 {recipe.versionCount} 版
          </Text>
        </div>
        <Button size="small" onClick={() => navigate('/recipes')}>
          返回方案库
        </Button>
      </div>

      {recipe.applicableScope ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-recipe-scope="true">
          <Text strong className="block mb-1">
            适用场景
          </Text>
          <Text type="tertiary" size="small">
            {recipe.applicableScope}
          </Text>
        </Card>
      ) : null}

      {recipe.limitations.length > 0 ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-recipe-limitations="true">
          <Text strong className="block mb-1">
            限制（复制到新项目时会一并带过去）
          </Text>
          {recipe.limitations.map((limitation) => (
            <Text key={limitation} type="tertiary" size="small" className="block">
              · {limitation}
            </Text>
          ))}
        </Card>
      ) : null}

      {error ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-recipe-error="true">
          <Text type="danger">{error}</Text>
        </Card>
      ) : null}
      {notice ? (
        <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-recipe-notice="true">
          <Text type="success">{notice}</Text>
        </Card>
      ) : null}

      <Card className="console-card mb-3" bodyStyle={{ padding: 14 }} data-recipe-versions="true">
        <Text strong className="block mb-2">
          版本
        </Text>
        <Text type="tertiary" size="small" className="block mb-2">
          只有<strong>已发布</strong>的版本可以用于创建项目：草稿是作者的工作中间态。方案升级只影响之后复制出的项目，
          已经建好的项目不会跟着变。
        </Text>
        <div className="comparison-table">
          <div className="comparison-row comparison-row--head">
            <span>版本</span>
            <span>状态</span>
            <span>变更理由</span>
            <span>内容 hash</span>
            <span>操作</span>
          </div>
          {detail.data.versions.map((version) => (
            <div key={version.id} className="comparison-row" data-recipe-version={version.version}>
              <span>v{version.version}</span>
              <span>
                <Tag size="small" color={version.status === 'published' ? 'green' : 'grey'}>
                  {versionStatusLabel(version.status)}
                </Tag>
              </span>
              <span>{version.changeReason}</span>
              <span>
                <Text type="tertiary" size="small">
                  {version.contentHash.slice(0, 16)}…
                </Text>
              </span>
              <span>
                {version.status === 'published' ? (
                  <Button size="small" theme={selectedVersion?.id === version.id ? 'solid' : 'borderless'} onClick={() => setSearchParams((params) => { params.set('version', String(version.id)); return params })}>
                    {selectedVersion?.id === version.id ? '已选择用于创建' : '选择此版本'}
                  </Button>
                ) : detail.capabilities.canPublish ? (
                  <Button size="small" theme="borderless" disabled={busy} onClick={() => void publish(version)}>
                    发布
                  </Button>
                ) : (
                  <Text type="tertiary" size="small">
                    待作者发布
                  </Text>
                )}
              </span>
            </div>
          ))}
        </div>
      </Card>

      <Card className="console-card" bodyStyle={{ padding: 14 }} data-recipe-copy="true">
        <Text strong className="block mb-2">
          用这个方案创建项目
        </Text>
        <Text type="tertiary" size="small" className="block mb-2">
          复制的是<strong>选定版本的内容快照</strong>：项目建成后与方案再无写入关系，之后改方案不会影响它。
          {selectedVersion ? `当前选择：v${selectedVersion.version}（版本 ID ${selectedVersion.id}）` : '当前没有已发布版本，暂时不能创建项目。'}
        </Text>
        <div className="wizard-grid">
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="recipe-project-name">
              新项目名称
            </label>
            <Input
              id="recipe-project-name"
              value={projectName}
              onChange={setProjectName}
              placeholder="例如 冷链问答 v2"
            />
          </div>
          <div className="wizard-field">
            <label className="wizard-field__label" htmlFor="recipe-project-goal">
              目标
            </label>
            <Input id="recipe-project-goal" value={projectGoal} onChange={setProjectGoal} placeholder="例如 冷链问答 SFT" />
          </div>
        </div>
        <div className="mt-3">
          <Button
            theme="solid"
            disabled={busy || !selectedVersion}
            onClick={() => void createProject()}
            data-recipe-create="true"
          >
            创建项目
          </Button>
        </div>

        {copyResult ? (
          <div className="mt-3" data-recipe-copy-result="true">
            <Text strong className="block mb-1">
              复制结果
            </Text>
            <Text type="tertiary" size="small" className="block">
              已复制文档：{copyResult.copiedDocuments.join('、')}
            </Text>
            {copyResult.unboundModelConnection ? (
              <div className="flex items-start gap-2 mt-1" data-recipe-unbound-model="true">
                <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
                <Text size="small">
                  生成节点引用的模型连接在当前环境不可用（凭证不跨工作区复制）：请到项目的设计页重新绑定连接后，
                  再开始试制。
                </Text>
              </div>
            ) : null}
            {copyResult.unboundJudgeConnections > 0 ? (
              <div className="flex items-start gap-2 mt-1" data-recipe-unbound-judges="true">
                <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
                <Text size="small">
                  有 {copyResult.unboundJudgeConnections} 个裁判连接不可用：质量实验会在创建时提示重新选择裁判。
                </Text>
              </div>
            ) : null}
            {copyResult.clearedRubricVersion ? (
              <div className="flex items-start gap-2 mt-1" data-recipe-cleared-rubric="true">
                <AlertTriangle size={16} className="mt-1 text-amber-500" aria-hidden />
                <Text size="small">
                  评估节点原来引用的量表版本无法跨项目映射，已被清空：请在设计页重新选择量表。
                </Text>
              </div>
            ) : null}
            {createdProjectID ? (
              <div className="mt-2">
                <Button
                  size="small"
                  theme="solid"
                  onClick={() => navigate(projectHref('project.overview', createdProjectID))}
                  data-recipe-open-project="true"
                >
                  打开新项目
                </Button>
              </div>
            ) : null}
          </div>
        ) : null}
      </Card>
    </div>
  )
}
