# 安全扫描 finding 复核：`App.tsx:1747` 的 SSRF 判定为**误报**

## 结论

静态分析工具（pi-lens / ast-grep 规则）对 `apps/web-user/src/App.tsx:1747` 报告
「Potential SSRF — fetch with user-controlled URL」。**父代理复核后判定为误报**，
依据是 SSRF 的两个必要条件**都不成立**。本文档保留完整推理链，供后续复核复用。

## 被报告的位置

```ts
// apps/web-user/src/App.tsx:1744-1749
const downloadArtifact = async (artifact: Artifact) => {
  if (!artifact.datasetId || !artifact.id) return
  try {
    const response = await fetch(consoleApi.artifactDownloadUrl(artifact.datasetId, artifact.id), {
      credentials: 'include',
    })
```

## SSRF 的两个必要条件，逐条核对

### 条件 A：在**服务端**发起请求 —— **不成立**

这是**浏览器端** JavaScript（`apps/web-user/src/App.tsx`，React SPA）。
SSRF 的语义是「服务端被诱导去请求攻击者指定的内部资源」；
浏览器 `fetch` 由用户自己的浏览器发起，请求的目标就是用户自己所在的站点，
不存在「服务端被劫持去访问内网」这一层。

### 条件 B：URL / host 由攻击者可控 —— **不成立**

目标 URL 由 `lib/api.ts` 的 `artifactDownloadUrl` 生成：

```ts
// apps/web-user/src/lib/api.ts:607
artifactDownloadUrl: (datasetId: number, artifactId: number) =>
  `/api/v1/datasets/${datasetId}/export/download?artifactId=${artifactId}`,
```

- 两个入参的**类型都是 `number`**（TypeScript 层面强制：`Artifact` 的 `datasetId` / `id`
  均为数值字段）；
- 返回的是**相对路径**（以 `/` 开头、无 scheme、无 host）→ host 恒为当前站点；
- 因此「把 URL 换成 `http://169.254.169.254/...`」这条攻击路径在这个代码形状下不成立。

`number` 参与模板字符串拼接后不可能产生新的 host 或 scheme；即使传入 `NaN`，
结果也只是失效的相对路径（落到本站 404），不会外发到第三方。

## 顺带核实服务端端点也是安全的

```go
// apps/api/exports.go:72 downloadArtifact
artifactID := r.URL.Query().Get("artifactId")      // 只是 id 字符串
items, err := app.artifacts.List(r.Context(), id)  // 取该数据集的工件
for _, item := range items {
    if artifactID == strconv.FormatInt(item.ID, 10) {   // 必须在列表内
        objectKey := item.ObjectKey                     // 来自数据库，非用户输入
        ...objectStore.ReadBytes(r.Context(), objectKey)
```

服务端**不接受用户提供的 URL**：`artifactId` 只用于在**该数据集的工件列表**里查表，
真正的对象键 `ObjectKey` 来自数据库（服务端写库时生成）。因此服务端也不存在 SSRF。

## 为什么不「顺手修一下」

1. **没有可修的真实缺陷。** 把 `fetch(url)` 换成别的写法不会提升安全性，
   只会增加改动面与回归风险（这条路径是「导出交付」的关键动作，见 #42 的验收断言）。
2. **「为了让扫描器闭嘴而改代码」是反模式。** 它会诱导后人认为这里曾经有漏洞，
   或把真实缺陷用同样的话术掩盖过去。
3. 契约 §3 要求「禁止伪造测试通过」的精神同样适用于此：**判定必须基于证据**，
   而证据表明这不是缺陷。

## 处置

- 不修改该处代码。
- 本判定记录在案。若将来该处改为接受用户提供的 URL / host（例如
  「从自定义地址下载」），则本条判定**立即失效**，必须重新评估。
- 若工具的规则可配置，建议为「相对路径 + 数值入参」的 `fetch` 增加白名单，
  减少这类噪音 —— 但这属于工具配置，不影响本仓库代码。
