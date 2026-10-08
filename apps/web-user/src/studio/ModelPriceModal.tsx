import { useEffect, useState } from 'react'
import { Button, Input, Modal, Spin, TextArea, Typography } from '@douyinfe/semi-ui'
import { modelPriceApi } from '../lib/api/pricing'
import type { ModelPriceVersion } from '../lib/api/pricing'

const { Text } = Typography
type PriceConnection = { id: number; name: string; model: string }
type PriceDraft = { priceVersion: string; input: string; output: string; isFree: boolean; isEstimated: boolean; note: string }

function draftFromPrice(price: ModelPriceVersion | null): PriceDraft {
  return { priceVersion: `price-${new Date().toISOString().replace(/[^0-9]/g, '')}`, input: String(price?.inputPriceMinorPerMillion ?? 0), output: String(price?.outputPriceMinorPerMillion ?? 0), isFree: price?.isFree ?? false, isEstimated: price?.isEstimated ?? false, note: price?.note ?? '' }
}

/** 价格只追加版本，连接模型与地址由服务端锁定为该连接的当前配置。 */
export function ModelPriceModal({ connection, onClose, onSaved }: { connection: PriceConnection | null; onClose: () => void; onSaved: () => void }) {
  const [current, setCurrent] = useState<ModelPriceVersion | null>(null)
  const [draft, setDraft] = useState<PriceDraft | null>(null)
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [retry, setRetry] = useState(0)

  useEffect(() => {
    if (!connection) return
    let cancelled = false
    setLoading(true); setDraft(null); setCurrent(null); setError(null); setLoadError(null)
    void modelPriceApi.get(connection.id).then((price) => {
      if (!cancelled) { setCurrent(price); setDraft(draftFromPrice(price)) }
    }).catch((reason) => { if (!cancelled) setLoadError(reason instanceof Error ? reason.message : '价格配置读取失败') })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [connection?.id, retry])

  const save = async () => {
    if (!connection || !draft || loading || saving) return
    setError(null)
    if (!draft.priceVersion.trim()) { setError('请填写价格版本名。'); return }
    if (!/^\d+$/.test(draft.input) || !/^\d+$/.test(draft.output)) { setError('输入与输出单价必须是非负整数，单位为分/百万 token。'); return }
    const input = Number(draft.input), output = Number(draft.output)
    if (!Number.isSafeInteger(input) || !Number.isSafeInteger(output)) { setError('单价超出支持范围，请填写有效的非负整数。'); return }
    if (!draft.isFree && input === 0 && output === 0) { setError('零价格必须显式确认免费模型；收费模型请填写单价。'); return }
    setSaving(true)
    try {
      await modelPriceApi.save(connection.id, { priceVersion: draft.priceVersion.trim(), inputPriceMinorPerMillion: input, outputPriceMinorPerMillion: output, isFree: draft.isFree, isEstimated: draft.isEstimated, note: draft.note.trim() })
      onSaved(); onClose()
    } catch (reason) { setError(reason instanceof Error ? reason.message : '价格配置保存失败，请检查后重试。') }
    finally { setSaving(false) }
  }

  return <Modal visible={connection !== null} title={`价格配置：${connection?.name || '模型连接'}`} onCancel={() => { if (!saving) onClose() }} onOk={() => void save()} okText="保存价格新版本" cancelText="取消" confirmLoading={saving} okButtonProps={{ disabled: loading || !draft || Boolean(loadError), 'aria-label': '保存价格新版本' }} cancelButtonProps={{ disabled: saving, 'aria-label': '取消价格配置' }} width={620} style={{ maxWidth: 'calc(100vw - 32px)' }} maskClosable={!saving}>
    {loading ? <div className="py-8 text-center"><Spin tip="正在读取当前模型价格" /></div> : loadError ? <div role="alert"><Text type="danger">{loadError}</Text><Button size="small" className="ml-2" onClick={() => setRetry(retry + 1)}>重新读取价格</Button></div> : draft ? <div className="wizard-grid" data-model-price-form="true">
      <Text type="tertiary" size="small" className="block">模型：{current?.modelName || connection?.model || '未设置'}。币种为人民币；100 分 = 1 元。价格版本用于预估预算与记账，保存后用于后续调用。</Text>
      <Text size="small" className="block">{current ? `当前价格版本：${current.priceVersion}；${current.isFree ? '免费' : current.isEstimated ? '保守估算' : '已配置单价'}` : '当前没有价格配置，请填写实际单价或明确标记免费。'}</Text>
      <label className="wizard-field"><span className="wizard-field__label">新价格版本名</span><Input aria-label="新价格版本名" value={draft.priceVersion} disabled={saving} onChange={(value) => setDraft({ ...draft, priceVersion: value })} placeholder="例如：2026-10官方价格" /></label>
      <div className="document-editor__grid document-editor__grid--two">
        <label className="wizard-field"><span className="wizard-field__label">输入单价（分/百万 token）</span><Input aria-label="输入单价（分/百万 token）" type="number" min={0} step={1} value={draft.input} disabled={saving || draft.isFree} onChange={(value) => setDraft({ ...draft, input: value })} /></label>
        <label className="wizard-field"><span className="wizard-field__label">输出单价（分/百万 token）</span><Input aria-label="输出单价（分/百万 token）" type="number" min={0} step={1} value={draft.output} disabled={saving || draft.isFree} onChange={(value) => setDraft({ ...draft, output: value })} /></label>
      </div>
      <label className="flex items-center gap-2"><input type="checkbox" aria-label="显式确认免费模型" checked={draft.isFree} disabled={saving} onChange={(event) => setDraft({ ...draft, isFree: event.target.checked, ...(event.target.checked ? { input: '0', output: '0', isEstimated: false } : {}) })} />显式确认免费模型</label>
      <label className="flex items-center gap-2"><input type="checkbox" aria-label="使用保守估算价格" checked={draft.isEstimated} disabled={saving || draft.isFree} onChange={(event) => setDraft({ ...draft, isEstimated: event.target.checked })} />使用保守估算价格</label>
      <label className="wizard-field"><span className="wizard-field__label">价格说明（选填）</span><TextArea aria-label="价格说明" value={draft.note} disabled={saving} onChange={(value) => setDraft({ ...draft, note: value })} autosize={{ minRows: 2, maxRows: 5 }} placeholder="价格来源、更新时间或估算依据" /></label>
      {error ? <div className="wizard-field__error" role="alert">{error}</div> : null}
    </div> : null}
  </Modal>
}
