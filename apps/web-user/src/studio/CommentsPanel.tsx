import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Card, Input, Select, Spin, Tag, Typography } from '@douyinfe/semi-ui'
import { MessageSquare, Send } from 'lucide-react'
import { activityApi } from '../lib/api/studio'
import type { StudioComment } from '../lib/api/studio'

/**
 * 评论面板（Issue #160 T27）。
 *
 * 三条与契约直接相关的界面决定：
 *
 *  1. **明确写出「评论不是判断」**。它是讨论，不改变有效处置、也不解除发布门槛。
 *     不写清楚会让用户以为「留言就能放行」，而那是发布门槛被绕过的形态。
 *  2. **更正保留旧修订**：同一作者再次提交会生成新修订（旧修订仍可见并标记）。
 *     按钮文案随「我是否已有当前评论」变化，避免用户以为自己在发第二条评论。
 *  3. **提及只能选项目成员**：候选来自服务端（`mention-candidates`），
 *     服务端还会再校验一次 —— 界面上的下拉框不是权限判定。
 */

export function CommentPanel({
  projectId,
  anchorKind,
  anchorId,
}: {
  projectId: number
  anchorKind: 'sample_version' | 'batch'
  anchorId: number
}) {
  const { Text } = Typography
  const [comments, setComments] = useState<StudioComment[]>([])
  const [viewerID, setViewerID] = useState(0)
  const [candidates, setCandidates] = useState<number[]>([])
  const [body, setBody] = useState('')
  const [mentions, setMentions] = useState<number[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)

  const load = useCallback(async () => {
    if (anchorId <= 0) return
    setLoading(true)
    setError(null)
    try {
      const response = await activityApi.listComments(projectId, anchorKind, anchorId)
      setComments(response.items ?? [])
      setViewerID(response.viewerId ?? 0)
    } catch (loadError) {
      setError(loadError instanceof Error ? loadError.message : '加载评论失败')
    } finally {
      setLoading(false)
    }
  }, [anchorId, anchorKind, projectId])

  useEffect(() => {
    void load()
  }, [load])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const response = await activityApi.mentionCandidates(projectId)
        if (!cancelled) setCandidates((response.items ?? []).map((member) => member.userId))
      } catch {
        // 候选列表拿不到不影响发评论（不提及任何人即可）：这里刻意不报错，
        // 否则一个辅助信息会挡住主操作。
      }
    })()
    return () => {
      cancelled = true
    }
  }, [projectId])

  const myCurrent = useMemo(
    () => comments.find((comment) => comment.current && comment.authorId === viewerID),
    [comments, viewerID],
  )

  const submit = useCallback(async () => {
    if (body.trim() === '') {
      setError('评论内容不能为空')
      return
    }
    setBusy(true)
    setError(null)
    setNotice(null)
    try {
      await activityApi.createComment(projectId, { anchorKind, anchorId, body: body.trim(), mentions })
      setBody('')
      setMentions([])
      setNotice(myCurrent ? '已保存更正（旧修订仍保留）' : '评论已发表')
      await load()
    } catch (submitError) {
      setError(submitError instanceof Error ? submitError.message : '保存评论失败')
    } finally {
      setBusy(false)
    }
  }, [anchorId, anchorKind, body, load, mentions, myCurrent, projectId])

  return (
    <Card className="console-card" bodyStyle={{ padding: 12 }} data-comment-panel="true">
      <Text strong className="block mb-1">
        <MessageSquare size={14} aria-hidden /> 讨论（{comments.length}）
      </Text>
      <Text type="tertiary" size="small" className="block mb-2">
        评论是讨论，不是判断：它不会改变有效处置，也不能解除发布门槛。
      </Text>

      {loading ? (
        <Spin tip="正在加载评论" />
      ) : comments.length === 0 ? (
        <Text type="tertiary" size="small" className="block mb-2" data-comment-empty="true">
          还没有评论。
        </Text>
      ) : (
        <ul className="review-evidence" data-comment-list="true">
          {comments.map((comment) => (
            <li key={comment.id} data-comment-id={comment.id} data-comment-current={comment.current}>
              <Text size="small">
                {comment.current ? null : (
                  <Tag size="small" color="grey">
                    已被更正的修订 r{comment.revision}
                  </Tag>
                )}{' '}
                {comment.body}
              </Text>
              <Text type="tertiary" size="small" className="block">
                作者 {comment.authorId} · r{comment.revision} · {new Date(comment.createdAt).toLocaleString()}
                {comment.mentions.length > 0 ? ` · 提及 ${comment.mentions.join('、')}` : ''}
              </Text>
            </li>
          ))}
        </ul>
      )}

      <div className="mt-2">
        <Input
          value={body}
          onChange={setBody}
          placeholder="写下你的意见（例如：这条推理的第二步跳得太快）"
          aria-label="评论内容"
          data-comment-body="true"
        />
        <div className="mt-2">
          <Select
            multiple
            value={mentions}
            style={{ width: '100%' }}
            placeholder="提及项目成员（只能选本项目的成员）"
            aria-label="提及成员"
            optionList={candidates.map((userID) => ({ value: userID, label: `用户 ${userID}` }))}
            onChange={(value) => setMentions((value as number[]) ?? [])}
          />
        </div>
        {error ? (
          <div className="wizard-field__error mt-1" role="alert" data-comment-error="true">
            {error}
          </div>
        ) : null}
        {notice ? (
          <Text type="success" size="small" className="block mt-1" data-comment-notice="true">
            {notice}
          </Text>
        ) : null}
        <div className="mt-2">
          <Button
            size="small"
            theme="solid"
            icon={<Send size={14} />}
            loading={busy}
            onClick={() => void submit()}
            data-comment-submit="true"
          >
            {myCurrent ? '保存更正（保留旧修订）' : '发表评论'}
          </Button>
        </div>
      </div>
    </Card>
  )
}
