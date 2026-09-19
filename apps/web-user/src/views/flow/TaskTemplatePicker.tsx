import { Card, Empty, Tag, Typography } from '@douyinfe/semi-ui'
import { LayoutTemplate } from 'lucide-react'

const { Text, Title } = Typography

/**
 * 任务模板选择器。
 *
 * 解决的问题（issue #84「操作步骤不符合人类习惯」/ #90「页面设计说明书」）：
 *
 * #84 调研发现，新建任务页一次性平铺 6 个字段，其中「生成策略 / AI 服务 /
 * 存储配置」三项属于管理员配置，却与「任务主题 / 目标样本量」两个业务必填项
 * 同权重展示；而该页自己写着「只填任务主题和目标规模即可」，文案与界面自相矛盾。
 *
 * 同类开源项目都用「选一个模板即预置好参数」来降低上手成本：
 *   - Dify：内置 5 种 Knowledge Pipeline 模板（General Mode-ECO / Parent-child-HQ /
 *     Simple Q&A / LLM Generated Q&A / Convert to Markdown）
 *   - Argilla：FeedbackDataset 预置 for_text_classification / for_summarization 等模板
 *
 * 本组件是该思路在「新建任务」页的落地：用户点一张模板卡片，即可把主题与目标
 * 规模一次性填好，无需理解背后的生成策略与存储配置（渐进披露，参见 NN/g
 * "Progressive Disclosure"）。
 *
 * 纯展示组件：props 进、回调出；不请求数据、不读路由，便于父组件任意接入。
 * 父组件负责提供 templates（可来自常量表或后端），并在 onSelect 中填充表单。
 */
export type TaskTemplate = {
  id: string
  name: string
  description: string
  /** 预置的任务主题 */
  rootKeyword: string
  /** 预置的目标样本数 */
  targetSize: number
  /** 标签，如 ['SFT', '问答'] */
  tags: string[]
}

export type TaskTemplatePickerProps = {
  templates: TaskTemplate[]
  /** 选中模板时回调，父组件据此填充表单 */
  onSelect: (template: TaskTemplate) => void
  /** 当前选中的模板 id */
  selectedId?: string
}

export function TaskTemplatePicker({ templates, onSelect, selectedId }: TaskTemplatePickerProps): JSX.Element {
  return (
    <Card className="console-panel" bodyStyle={{ padding: 20 }}>
      <div className="flex flex-wrap items-center gap-2">
        <LayoutTemplate size={16} />
        <Title heading={5} className="!mb-0">从模板开始</Title>
      </div>
      <Text className="mt-2 block console-caption">
        选一个模板即可预置任务主题与目标规模；也可以不选，直接在下方手动填写。
      </Text>

      {templates.length === 0 ? (
        <div className="mt-4">
          <Empty description="暂无可用模板，可直接手动填写任务参数" />
        </div>
      ) : (
        <div className="mt-4 grid grid-cols-1 gap-3 md:grid-cols-2 lg:grid-cols-3">
          {templates.map((template) => {
            const selected = template.id === selectedId
            return (
              <button
                key={template.id}
                type="button"
                aria-pressed={selected}
                onClick={() => onSelect(template)}
                className={[
                  'console-domain-item grid gap-2 text-left transition',
                  selected ? 'ring-2 ring-blue-400' : 'hover:ring-1 hover:ring-slate-300',
                ].join(' ')}
                style={selected ? { background: 'color-mix(in srgb, var(--semi-color-primary-light-default, #e8f3ff) 60%, white 40%)' } : undefined}
              >
                <div className="flex items-center justify-between gap-2">
                  <Text strong>{template.name}</Text>
                  {selected ? <Tag color="blue" size="small">已选择</Tag> : null}
                </div>

                <Text className="text-xs console-caption">{template.description}</Text>

                {template.tags.length > 0 ? (
                  <div className="flex flex-wrap gap-1.5">
                    {template.tags.map((tag) => (
                      <Tag key={tag} size="small" color="grey">
                        {tag}
                      </Tag>
                    ))}
                  </div>
                ) : null}

                <Text className="text-xs console-caption">
                  主题：{template.rootKeyword} · 目标 {template.targetSize} 条
                </Text>
              </button>
            )
          })}
        </div>
      )}
    </Card>
  )
}
