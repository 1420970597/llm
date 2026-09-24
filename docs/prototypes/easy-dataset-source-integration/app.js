/* ==========================================================================
   Atelier · 素材来源（source）原型
   --------------------------------------------------------------------------
   评审用独立原型，使用 Hash 路由。不进入生产构建（apps/web-user 不被修改）。
   视觉 token 与 apps/web-user/src/styles.css 一致；文案与信息架构遵循
   docs/design/2026-09-21-data-studio/ 的 Atelier 设计基线。
   ========================================================================== */

// ---------- 图标（lucide 风格描边，与生产界面同族） ----------
const I = {
  today: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>',
  projects: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 3a9 9 0 0 1 0 18"/></svg>',
  recipes: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H19v15H6.5A2.5 2.5 0 0 0 4 20.5z"/><path d="M4 20.5A2.5 2.5 0 0 1 6.5 18H19v3H6.5"/></svg>',
  delivery: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8"/><path d="M2 4h20v4H2z"/><path d="M10 12h4"/></svg>',
  activity: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><path d="M3 12h4l3 8 4-16 3 8h4"/></svg>',
  connect: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><path d="M9 17H7A5 5 0 0 1 7 7h2"/><path d="M15 7h2a5 5 0 0 1 0 10h-2"/><path d="M8 12h8"/></svg>',
  members: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><path d="M16 20v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2"/><circle cx="9" cy="7" r="3.2"/><path d="M22 20v-2a4 4 0 0 0-3-3.85"/><path d="M16.5 4.2a4 4 0 0 1 0 7.6"/></svg>',
  help: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.7.4-1 .9-1 1.7v.3"/><path d="M12 17h.01"/></svg>',
  search: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="m20 20-3.2-3.2"/></svg>',
  exit: '<svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 4h4a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-4"/><path d="M10 17l-5-5 5-5"/><path d="M5 12h10"/></svg>',
  plus: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>',
  download: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="m7 12 5 5 5-5"/><path d="M4 21h16"/></svg>',
  save: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 3h11l3 3v15H5z"/><path d="M8 3v6h7V3"/><path d="M8 15h8"/></svg>',
  arrow: '<svg viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M5 12h14"/><path d="m13 6 6 6-6 6"/></svg>',
  file: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5"/></svg>',
  box: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M21 8 12 3 3 8v8l9 5 9-5z"/><path d="m3 8 9 5 9-5"/><path d="M12 13v8"/></svg>',
  warn: '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3 2 20h20z"/><path d="M12 9v5"/><path d="M12 17h.01"/></svg>',
};

// ---------- 骨架 ----------
const rail = (active) => `
<aside class="rail">
  <div class="rail__brand">a.</div>
  ${[['today', '今日工作'], ['projects', '数据项目'], ['recipes', '方案库'], ['delivery', '交付库']]
    .map(([k, label]) => `<div class="rail__item ${active === k ? 'is-active' : ''}">${I[k]}<span>${label}</span></div>`)
    .join('')}
  <div class="rail__spacer"></div>
  ${[['activity', '动态'], ['connect', '连接设置'], ['members', '成员与角色'], ['help', '帮助']]
    .map(([k, label]) => `<div class="rail__item">${I[k]}<span>${label}</span></div>`)
    .join('')}
  <div class="rail__exit">${I.exit}</div>
</aside>`;

const topbar = (crumb) => `
<header class="topbar">
  <nav class="crumb">${crumb.map((c, i) =>
    i === crumb.length - 1 ? `<b>${c}</b>` : `<span>${c}</span><span class="crumb__sep">›</span>`).join('')}</nav>
  <div class="topbar__spacer"></div>
  <div class="search">${I.search}<span>搜索</span></div>
  <div class="avatar">A</div>
</header>`;

const projectHead = (activeTab = '设计') => `
<div class="project-head">
  <h2>医疗问答知识库</h2>
  <p>把临床指南与规范转成可交付的 SFT / GRPO 训练数据</p>
</div>
<div class="workspace-tabs">
  ${['概览', '设计', '生产', '数据', '质量', '发布']
    .map((t) => `<span class="${t === activeTab ? 'is-active' : ''}">${t}</span>`).join('')}
</div>`;

const pageHead = (eyebrow, title, lede, actions = '') => `
<div class="page-head">
  <div class="page-head__text">
    <div class="eyebrow">${eyebrow}</div>
    <h1>${title}</h1>
    <p class="lede">${lede}</p>
  </div>
  ${actions}
</div>`;

// 文档台账：五类既有文档 + 本轮新增的第六类「素材来源」
const docLedger = () => `
<div class="docs__title">项目文档</div>
${[
  ['覆盖范围', 'v4'],
  ['思维标准', 'v2'],
  ['生产蓝图', 'v3'],
  ['素材来源', 'v3', true, true],
  ['质量策略', 'v1'],
  ['交付映射', 'v2'],
].map(([name, ver, active, isNew]) => `
  <div class="doc ${active ? 'is-active' : ''}">
    <span class="doc__mark"></span>
    <span class="doc__name">${name}</span>
    ${isNew ? '<span class="tree__badge">新增</span>' : ''}
    <span class="doc__ver">${ver}</span>
  </div>`).join('')}
<div class="doc is-planned"><span class="doc__mark"></span><span class="doc__name">评估量表</span><span class="doc__ver">v1</span></div>`;

const ledger = (rows) => `
<div class="ledger">
  <span class="ledger__label">版本历史（只读）</span>
  ${rows.map(([ver, when, who], i) =>
    `${i ? '<span class="ledger__sep">·</span>' : ''}<span class="ledger__item"><b>${ver}</b> ${when} ${who}</span>`).join('')}
</div>`;

// ---------- S01：素材来源总览（Default） ----------
const S01 = () => `
${rail('projects')}
<div class="main">
  ${topbar(['数据项目', '医疗问答知识库', '设计'])}
  <div class="content">
    ${projectHead()}
    ${pageHead('DESIGN / SOURCE', '素材来源', '决定问题从哪里来。来源与切分参数保存为版本，只影响之后的批次；已运行批次不变。',
      '<div style="display:flex;gap:9px"><button class="btn">' + I.download + ' 导入外部产物</button><button class="btn btn--primary">' + I.plus + ' 添加来源</button></div>')}

    <div class="cols">
      <!-- 左：文档台账 -->
      <div class="card"><div class="card__body card__body--tight"><div class="docs">${docLedger()}</div></div></div>

      <!-- 中：来源清单 + 文档大纲 -->
      <div>
        <div class="card">
          <div class="card__head">
            <h3>来源清单</h3>
            <span class="pill pill--mute">3 份 · 214 块</span>
            <div class="spacer"></div>
            <span class="pill pill--ok">${'<span class="pill__dot"></span>'}已就绪</span>
          </div>
          <div class="card__body card__body--tight">
            <div class="srclist">
              <div class="src">
                <span class="src__icon src__icon--pdf">PDF</span>
                <span class="src__name"><b>临床诊疗指南_2026修订版.pdf</b><small>章节感知分块 · 1.2–8.4 节</small></span>
                <span class="src__num">128<small>块</small></span>
                <span class="src__num src__num--hide">42.1 MB</span>
                <span><span class="pill pill--ok">${'<span class="pill__dot"></span>'}已完成</span></span>
              </div>
              <div class="src">
                <span class="src__icon src__icon--docx">DOC</span>
                <span class="src__name"><b>用药规范_v5.docx</b><small>章节感知分块 · 全部</small></span>
                <span class="src__num">62<small>块</small></span>
                <span class="src__num src__num--hide">18.4 MB</span>
                <span><span class="pill pill--ok">${'<span class="pill__dot"></span>'}已完成</span></span>
              </div>
              <div class="src">
                <span class="src__icon src__icon--md">MD</span>
                <span class="src__name"><b>典型病例集.md</b><small>固定长度分块 · chunkSize 1500 / 分隔 \n\n</small></span>
                <span class="src__num">24<small>块</small></span>
                <span class="src__num src__num--hide">6.2 MB</span>
                <span><span class="pill pill--run">${'<span class="pill__dot"></span>'}解析中 60%</span></span>
              </div>
              <div class="src">
                <span class="src__icon src__icon--zip">ZIP</span>
                <span class="src__name"><b>影像附件.zip</b><small>格式不支持（支持 PDF / MD / DOCX / TXT / EPUB）</small></span>
                <span class="src__num">—</span>
                <span class="src__num src__num--hide">0 MB</span>
                <span><span class="pill pill--bad">${'<span class="pill__dot"></span>'}不支持</span></span>
              </div>
            </div>
          </div>
        </div>

        <div class="card mt-16">
          <div class="card__head"><h3>文档大纲（章节结构）</h3><span class="pill pill--mute">随来源一起冻结</span><div class="spacer"></div><span class="pill pill--mute">只读</span></div>
          <div class="card__body card__body--tight">
            <div class="tree">
              <div class="tree__row"><span class="tree__caret">▾</span><span class="tree__name"><b>内科</b></span><span class="tree__count">128 块</span></div>
              <div class="tree__row tree__row--l2"><span class="tree__caret">▾</span><span class="tree__name">心血管</span><span class="tree__count">46 块</span></div>
              <div class="tree__row tree__row--l3"><span class="tree__caret"></span><span class="tree__name">高血压</span><span class="tree__count">22 块</span></div>
              <div class="tree__row tree__row--l3"><span class="tree__caret"></span><span class="tree__name">冠心病</span><span class="tree__count">24 块</span></div>
              <div class="tree__row tree__row--l2"><span class="tree__caret">▾</span><span class="tree__name">呼吸科</span><span class="tree__count">42 块</span></div>
              <div class="tree__row tree__row--l2"><span class="tree__caret">▸</span><span class="tree__name">消化科</span><span class="tree__count">40 块</span></div>
              <div class="tree__row"><span class="tree__caret">▾</span><span class="tree__name"><b>外科</b></span><span class="tree__count">62 块</span></div>
              <div class="tree__row tree__row--l2"><span class="tree__caret">▸</span><span class="tree__name">普外</span><span class="tree__count">38 块</span></div>
              <div class="tree__row"><span class="tree__caret">▾</span><span class="tree__name"><b>影像科</b></span><span class="tree__count">24 块</span></div>
            </div>
            <p class="field__hint" style="margin-top:10px">大纲来自来源文档的标题层级（块 name + summary），随来源版本冻结。它<b>只是章节结构</b>，不决定问题属于哪个领域 —— 领域/方向由覆盖矩阵定义。</p>
          </div>
        </div>
      </div>

      <!-- 右：检查器 -->
      <div class="card inspector">
        <div class="card__head"><h3>切分策略</h3><div class="spacer"></div><span class="pill pill--mute">v3</span></div>
        <div class="card__body">
          <div class="field">
            <label class="field__label">分块算法<em>*</em></label>
            <div class="select"><span>章节感知（recursive）</span><span>▾</span></div>
            <p class="field__hint">与外部工具的词表对齐：<code>text</code> / <code>token</code> / <code>code</code> / <code>recursive</code> / <code>custom</code>。</p>
          </div>
          <div class="field">
            <div class="field-row">
              <div>
                <label class="field__label">最小长度<em>*</em></label>
                <div class="input input--num">200</div>
              </div>
              <div>
                <label class="field__label">最大长度<em>*</em></label>
                <div class="input input--num">2000</div>
              </div>
            </div>
            <p class="field__hint">默认 <code>chunkSize</code> 1500 / <code>textSplitMaxLength</code> 2000（与外部工具默认值对齐）。</p>
          </div>
          <div class="field">
            <label class="field__label">处理选项</label>
            <div class="check"><span class="check__box">✓</span><span>保留标题层级</span></div>
            <div class="check"><span class="check__box">✓</span><span>生成块摘要</span></div>
            <div class="check"><span class="check__box check__box--off"></span><span>代码块整体保留</span></div>
          </div>
          <div class="field">
            <label class="field__label">来源类型</label>
            <div class="select"><span>文档（document）</span><span>▾</span></div>
            <p class="field__hint">写入覆盖矩阵的「方向来源」。可选 document / ai / manual，参与服务端校验。</p>
          </div>
          <div class="note">⚠ 影响：新版本只影响之后的批次，已运行批次继续用旧快照。<b>不会</b>重新生成已有样本。</div>
        </div>
        <div class="inspector__foot">
          <button class="btn btn--primary">${I.save} 保存为新版本</button>
          <button class="btn">试切 10 块</button>
        </div>
      </div>
    </div>

    ${ledger([['v3', '2026-09-23', '张工'], ['v2', '2026-09-20', '李工'], ['v1', '2026-09-18', '张工']])}
  </div>
</div>`;

// ---------- S02：导入向导 ----------
const S02 = () => `
${rail('projects')}
<div class="main">
  ${topbar(['数据项目', '医疗问答知识库', '设计', '导入外部产物'])}
  <div class="content">
    ${projectHead()}
    ${pageHead('DESIGN / SOURCE / IMPORT', '导入外部产物', '把外部工具产出的中间产物接进来。只接收素材（切分块），不接收成品问答对 —— 问题仍由本项目按覆盖与标准生成。')}

    <div class="steps">
      <div class="step is-done"><span class="step__num">✓</span><span class="step__label">选择来源</span></div>
      <div class="step__line"></div>
      <div class="step is-done"><span class="step__num">✓</span><span class="step__label">上传产物</span></div>
      <div class="step__line"></div>
      <div class="step is-active"><span class="step__num">3</span><span class="step__label">字段映射</span></div>
      <div class="step__line"></div>
      <div class="step"><span class="step__num">4</span><span class="step__label">预检与提交</span></div>
    </div>

    <div class="cols--2" style="display:grid;gap:16px">
      <div>
        <div class="card">
          <div class="card__head"><h3>产物字段 → 本项目字段</h3><div class="spacer"></div><span class="pill pill--ok">${'<span class="pill__dot"></span>'}6/6 已映射</span></div>
          <div class="card__body card__body--tight">
            <table class="grid">
              <thead><tr><th>产物字段</th><th>类型</th><th>→ 本项目字段</th><th>说明</th></tr></thead>
              <tbody>
                <tr><td class="mono">name</td><td>string</td><td class="mono">source_chunks.name</td><td>块名（含章节路径）</td></tr>
                <tr><td class="mono">content</td><td>string</td><td class="mono">source_chunks.content</td><td>块正文（必填）</td></tr>
                <tr><td class="mono">summary</td><td>string</td><td class="mono">source_chunks.summary</td><td>章节摘要（可空）</td></tr>
                <tr><td class="mono">fileName</td><td>string</td><td class="mono">source_chunks.file_name</td><td>归属文件</td></tr>
                <tr><td class="mono">size</td><td>int</td><td class="mono">source_chunks.size</td><td>字符数</td></tr>
                <tr><td class="mono">projectId</td><td>string</td><td class="mono">source_chunks.external_project</td><td>只作追溯，不作为归属</td></tr>
              </tbody>
            </table>
            <p class="field__hint" style="margin-top:12px">上游产物<b>不包含</b>块级 hash 与标签路径 —— 已核实其 chunk 导出只有上述字段。因此 <code>content_hash</code> 由本项目在导入时<b>自行计算</b>（幂等依据），标签关联也由本项目建立。</p>
          </div>
        </div>

        <div class="card mt-16">
          <div class="card__head"><h3>预检结果</h3><div class="spacer"></div><span class="pill pill--warn">2 条待处理</span></div>
          <div class="card__body card__body--tight">
            <div class="src">
              <span class="src__icon src__icon--md">OK</span>
              <span class="src__name"><b>214 块结构完整</b><small>name / content / summary 均非空</small></span>
              <span class="src__num">214<small>条</small></span><span class="src__num src__num--hide"></span>
              <span><span class="pill pill--ok">通过</span></span>
            </div>
            <div class="src">
              <span class="src__icon src__icon--zip">!</span>
              <span class="src__name"><b>3 块 content 为空</b><small>chunk#47, chunk#112, chunk#198</small></span>
              <span class="src__num">3<small>条</small></span><span class="src__num src__num--hide"></span>
              <span><span class="pill pill--warn">待人工</span></span>
            </div>
            <div class="src">
              <span class="src__icon src__icon--zip">!</span>
              <span class="src__name"><b>2 块内容 hash 与既有版本重复</b><small>本项目自行计算 hash 后判定；将跳过，不产生新块</small></span>
              <span class="src__num">2<small>条</small></span><span class="src__num src__num--hide"></span>
              <span><span class="pill pill--warn">跳过</span></span>
            </div>
          </div>
        </div>
      </div>

      <div class="card inspector">
        <div class="card__head"><h3>幂等与来源</h3></div>
        <div class="card__body">
          <div class="field">
            <label class="field__label">来源标识<em>*</em></label>
            <div class="input input--num" style="font-family:'JetBrains Mono',monospace;font-size:11.5px">easy-dataset/med-kb/v3</div>
            <p class="field__hint">唯一键的一部分：重复导入同一 sourceKey 会回放结果，不产生副作用。</p>
          </div>
          <div class="field">
            <label class="field__label">内容摘要</label>
            <div class="input input--num" style="font-family:'JetBrains Mono',monospace;font-size:11.5px">sha256:9f2c…a41e</div>
            <p class="field__hint">与台账比对，判定「这份产物是否已经导入过」。</p>
          </div>
          <div class="field">
            <label class="field__label">关联方向</label>
            <div class="select"><span>人工指定（无自动匹配）</span><span>▾</span></div>
            <p class="field__hint">上游产物<b>不携带领域标签</b>（其标签挂在问题上，不挂在块上）。因此方向关联在导入后于覆盖矩阵中人工建立。</p>
          </div>
          <div class="note note--info">本次导入只写 <b>source 文档版本 + source_chunks</b>。不生成任何问答对，不消耗模型预算。</div>
        </div>
        <div class="inspector__foot">
          <button class="btn btn--primary">提交导入</button>
          <button class="btn">返回上一步</button>
        </div>
      </div>
    </div>
  </div>
</div>`;

// ---------- S03：方向 ↔ 源材料 关联 ----------
const S03 = () => `
${rail('projects')}
<div class="main">
  ${topbar(['数据项目', '医疗问答知识库', '设计', '覆盖范围'])}
  <div class="content">
    ${projectHead()}
    ${pageHead('DESIGN / COVERAGE', '覆盖矩阵', '领域与方向使用稳定 ID：删除草稿方向不会破坏已引用该版本的批次。右列显示每个方向的问题将从哪些素材块产生。',
      '<button class="btn">' + I.arrow + ' 去小批试制</button>')}

    <div class="cols--2" style="display:grid;gap:16px">
      <div class="card">
        <div class="card__head">
          <h3>领域 / 方向 / 来源</h3>
          <div class="spacer"></div>
          <span class="pill pill--mute">4 个领域 · 配额合计 96</span>
          <span class="pill pill--warn">1 个缺口</span>
        </div>
        <div class="card__body card__body--tight">
          <div class="maprow maprow--gap">
            <span class="maprow__dir"><b>内科 › 心血管</b><small>配额 24 · 难度 easy 40% / medium 40% / hard 20%</small></span>
            <span><span class="pill pill--ok">document</span></span>
            <span class="maprow__refs"><span class="chip chip--violet">指南 §1.2</span><span class="chip chip--violet">指南 §1.4</span></span>
            <span class="maprow__arrow" style="color:var(--text-muted)">${I.arrow}</span>
          </div>
          <div class="maprow">
            <span class="maprow__dir"><b>内科 › 呼吸科</b><small>配额 18 · 难度 medium 60% / hard 40%</small></span>
            <span><span class="pill pill--ok">document</span></span>
            <span class="maprow__refs"><span class="chip chip--violet">指南 §2.1</span><span class="chip chip--violet">用药规范 §3</span></span>
            <span class="maprow__arrow" style="color:var(--text-muted)">${I.arrow}</span>
          </div>
          <div class="maprow maprow--gap">
            <span class="maprow__dir"><b>内科 › 消化科</b><small>配额 16 · 难度 easy 50% / medium 50%</small></span>
            <span><span class="pill pill--warn">未关联</span></span>
            <span class="maprow__refs"><span class="chip chip--empty">无素材块</span></span>
            <span class="maprow__arrow" style="color:var(--warning)">${I.warn}</span>
          </div>
          <div class="maprow">
            <span class="maprow__dir"><b>外科 › 普外</b><small>配额 20 · 难度 medium 50% / hard 50%</small></span>
            <span><span class="pill pill--ok">document</span></span>
            <span class="maprow__refs"><span class="chip chip--violet">规范 §4</span><span class="chip chip--violet">病例集</span></span>
            <span class="maprow__arrow" style="color:var(--text-muted)">${I.arrow}</span>
          </div>
          <div class="maprow">
            <span class="maprow__dir"><b>影像科 › 影像诊断</b><small>配额 18 · 难度 medium 100%</small></span>
            <span><span class="pill pill--mute">ai</span></span>
            <span class="maprow__refs"><span class="chip">无源材料 · 按关键词生成</span></span>
            <span class="maprow__arrow" style="color:var(--text-muted)">${I.arrow}</span>
          </div>
        </div>
      </div>

      <div class="card inspector">
        <div class="card__head"><h3>方向来源</h3><div class="spacer"></div><span class="pill pill--warn">1 个缺口</span></div>
        <div class="card__body">
          <div class="field">
            <label class="field__label">「内科 › 消化科」的来源<em>*</em></label>
            <div class="select"><span>未选择</span><span>▾</span></div>
            <p class="field__hint">可选：引用素材块 / 按关键词生成 / 人工撰写。</p>
          </div>
          <div class="note">⚠ 该方向有配额 16，但没有素材块。执行时会<b>拒绝生成</b>并标记为缺口 —— 不会静默退回模板问题。</div>
          <div class="field mt-16">
            <label class="field__label">降级策略</label>
            <div class="select"><span>拒绝生成（推荐）</span><span>▾</span></div>
            <p class="field__hint">可选「退回关键词生成」。选它需要显式确认，且会在数据卡中记录。</p>
          </div>
        </div>
        <div class="inspector__foot"><button class="btn btn--primary">${I.save} 保存为新版本</button></div>
      </div>
    </div>

    <div class="section-title">来源类型分布</div>
    <div class="card"><div class="card__body card__body--tight">
      <table class="grid">
        <thead><tr><th>来源类型</th><th>方向数</th><th>配额合计</th><th>问题如何产生</th></tr></thead>
        <tbody>
          <tr><td><span class="pill pill--ok">document</span></td><td class="num">3</td><td class="num">62</td><td>由素材块接地，经 GenerateQuestionsV2 生成</td></tr>
          <tr><td><span class="pill pill--mute">ai</span></td><td class="num">1</td><td class="num">18</td><td>仅按关键词与方向名生成（无素材接地）</td></tr>
          <tr><td><span class="pill pill--warn">未关联</span></td><td class="num">1</td><td class="num">16</td><td>执行时拒绝，需先补齐</td></tr>
        </tbody>
      </table>
    </div></div>
  </div>
</div>`;

// ---------- S04：问题生成对比 ----------
const S04 = () => `
${rail('projects')}
<div class="main">
  ${topbar(['数据项目', '医疗问答知识库', '生产', '批次详情'])}
  <div class="content">
    ${projectHead('生产')}
    ${pageHead('RUN / PILOT-24', '小批试制 · 12 单元', '同一个方向的两条产出路径对比。左侧是当前实现的占位模板，右侧是接通素材后的真实问题。')}

    <div class="compare">
      <div class="compare__col compare__col--before">
        <h4>${I.warn} 现状：模板占位（questionFor）</h4>
        <div class="qbox qbox--bad">
          <div class="qbox__q">心血管（难度 easy）：第 1 题</div>
          <div class="qbox__meta"><span>source: <code>template</code></span><span>素材接地: 无</span></div>
        </div>
        <div class="qbox qbox--bad">
          <div class="qbox__q">心血管（难度 easy）：第 2 题</div>
          <div class="qbox__meta"><span>source: <code>template</code></span><span>素材接地: 无</span></div>
        </div>
        <div class="qbox qbox--bad">
          <div class="qbox__q">呼吸科（难度 medium）：第 1 题</div>
          <div class="qbox__meta"><span>source: <code>template</code></span><span>素材接地: 无</span></div>
        </div>
        <div class="codeblock"><span class="c">// apps/worker/studio_batch.go:210</span>
<span class="del">- return fmt.Sprintf(</span>
<span class="del">-   "%s（难度 %s）：第 %d 题",</span>
<span class="del">-   direction, difficulty, ordinal)</span></div>
        <p class="field__hint">问题文本与素材无关：换了文档，问题形态完全不变。训练样本的 <code>question</code> 无法回答「这个方向为什么产出这 3 个问题」。</p>
      </div>

      <div class="compare__col compare__col--after">
        <h4>✓ 目标：素材接地（GenerateQuestionsV2）</h4>
        <div class="qbox qbox--good">
          <div class="qbox__q">老年高血压患者合并 2 型糖尿病时，一线降压药物的选择依据与禁忌是什么？</div>
          <div class="qbox__meta"><span>接地: <code>指南 §1.2 · 高血压</code></span><span>难度: easy</span></div>
        </div>
        <div class="qbox qbox--good">
          <div class="qbox__q">冠心病二级预防中，他汀类药物的强度分级如何根据 LDL-C 目标值确定？</div>
          <div class="qbox__meta"><span>接地: <code>指南 §1.4 · 冠心病</code></span><span>难度: easy</span></div>
        </div>
        <div class="qbox qbox--good">
          <div class="qbox__q">社区获得性肺炎的经验性抗感染方案，在合并 COPD 时为何需要升级覆盖？</div>
          <div class="qbox__meta"><span>接地: <code>指南 §2.1</code> <code>用药规范 §3</code></span><span>难度: medium</span></div>
        </div>
        <div class="codeblock"><span class="c">// apps/worker/studio_batch.go — 替换而非新增平行函数</span>
<span class="add">+ return llm.GenerateQuestionsV2(ctx, provider,</span>
<span class="add">+   llm.QuestionGenInput{</span>
<span class="add">+     RootKeyword: rootKeyword,</span>
<span class="add">+     Directions:  directions,   <span class="c">// 含素材块</span></span>
<span class="add">+     DifficultyMix: mix,</span>
<span class="add">+   })</span></div>
        <p class="field__hint">每个问题可追溯到具体素材块。覆盖矩阵、思维标准、评估量表重新成为<b>真实输入</b>而非装饰。</p>
      </div>
    </div>

    <div class="card mt-24">
      <div class="card__head"><h3>本批次事实</h3><div class="spacer"></div><span class="pill pill--mute">快照冻结</span></div>
      <div class="card__body card__body--tight">
        <table class="grid">
          <thead><tr><th>单元</th><th>方向</th><th>难度</th><th>问题来源</th><th>素材块</th><th>状态</th></tr></thead>
          <tbody>
            <tr><td class="mono">内科/心血管#1</td><td>心血管</td><td>easy</td><td><span class="pill pill--ok">document</span></td><td class="mono">指南 §1.2</td><td><span class="pill pill--ok">已生成</span></td></tr>
            <tr><td class="mono">内科/心血管#2</td><td>心血管</td><td>easy</td><td><span class="pill pill--ok">document</span></td><td class="mono">指南 §1.4</td><td><span class="pill pill--ok">已生成</span></td></tr>
            <tr><td class="mono">内科/呼吸科#1</td><td>呼吸科</td><td>medium</td><td><span class="pill pill--ok">document</span></td><td class="mono">指南 §2.1</td><td><span class="pill pill--ok">已生成</span></td></tr>
            <tr><td class="mono">内科/消化科#1</td><td>消化科</td><td>easy</td><td><span class="pill pill--warn">未关联</span></td><td class="mono">—</td><td><span class="pill pill--bad">已拒绝</span></td></tr>
            <tr><td class="mono">影像科/影像诊断#1</td><td>影像诊断</td><td>medium</td><td><span class="pill pill--mute">ai</span></td><td class="mono">—</td><td><span class="pill pill--run">运行中</span></td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</div>`;

// ---------- S05：五大界面状态 ----------
const stateCard = (tag, title, body) => `
<div class="card state-card">
  <div class="card__head"><h3>${title}</h3><span class="state-card__tag">${tag}</span></div>
  <div class="card__body">${body}</div>
</div>`;

const S05 = () => `
${rail('projects')}
<div class="main">
  ${topbar(['数据项目', '医疗问答知识库', '设计', '状态定义'])}
  <div class="content">
    ${projectHead()}
    ${pageHead('DESIGN / SOURCE / STATES', '素材来源 · 五大界面状态', '每个状态都定义了触发条件、界面表现与行动出口。原型线框见 docs/prototypes/easy-dataset-source-integration/。')}

    <div class="states">
      ${stateCard('Default', '常规完整数据态', `
        <div class="srclist">
          <div class="src"><span class="src__icon src__icon--pdf">PDF</span><span class="src__name"><b>临床诊疗指南.pdf</b><small>章节感知 · 128 块</small></span><span class="src__num">128<small>块</small></span><span class="src__num src__num--hide">42 MB</span><span><span class="pill pill--ok">${'<span class="pill__dot"></span>'}已完成</span></span></div>
          <div class="src"><span class="src__icon src__icon--docx">DOC</span><span class="src__name"><b>用药规范_v5.docx</b><small>章节感知 · 62 块</small></span><span class="src__num">62<small>块</small></span><span class="src__num src__num--hide">18 MB</span><span><span class="pill pill--ok">${'<span class="pill__dot"></span>'}已完成</span></span></div>
        </div>
        <div style="display:flex;gap:9px;margin-top:14px"><button class="btn btn--primary btn--sm">${I.save} 保存为新版本</button><button class="btn btn--sm">试切 10 块</button></div>`)}
      ${stateCard('Loading', '骨架屏 / 加载指示', `
        <div class="skeleton skeleton--w40"></div>
        <div class="skeleton skeleton--w90"></div>
        <div class="skeleton skeleton--w75"></div>
        <div class="skeleton skeleton--w60"></div>
        <div class="skeleton skeleton--w90"></div>
        <p class="field__hint" style="margin-top:12px">正在读取版本 v3…　页签保持可点，表单禁用并显示半透明遮罩。</p>`)}
      ${stateCard('Empty', '缺省状态', `
        <div class="empty">
          <div class="empty__art">${I.box}</div>
          <h4>还没有素材来源</h4>
          <p>上传文档，或从外部工具导入已生成的中间产物。素材决定问题从哪里来。</p>
          <div class="empty__actions">
            <button class="btn btn--primary btn--sm">${I.plus} 添加来源</button>
            <button class="btn btn--sm">${I.download} 导入外部产物</button>
          </div>
        </div>`)}
      ${stateCard('Error', '网络错误 / 校验失败', `
        <div class="banner">
          <span class="banner__icon">!</span>
          <span>
            <b>解析失败：影像附件.zip 格式不支持</b>
            支持 PDF / MD / DOCX / TXT / EPUB。该来源已跳过，其余 3 份继续解析。
            <span class="banner__actions">
              <button class="btn btn--sm">重试</button>
              <button class="btn btn--sm">移除该来源</button>
              <button class="btn btn--sm">查看支持格式</button>
            </span>
          </span>
        </div>
        <div class="banner" style="margin-top:12px">
          <span class="banner__icon">!</span>
          <span><b>保存冲突（409）</b>版本已被「李工」更新为 v4，请刷新后重试。
            <span class="banner__actions"><button class="btn btn--sm">刷新</button></span>
          </span>
        </div>`)}
      ${stateCard('Edge-Case', '极值 / 溢出适配', `
        <div class="edge-row"><span class="edge-row__label">超长文件名</span><span class="edge-row__value"><span class="truncate">临床诊疗指南_2026年修订版_第3次增补_心血管分册…pdf</span></span></div>
        <div class="edge-row"><span class="edge-row__label">超大文件</span><span class="edge-row__value">286 MB · 后台解析，可离开页面<div class="progress"><div class="progress__bar" style="width:38%"></div></div></span></div>
        <div class="edge-row"><span class="edge-row__label">极多来源</span><span class="edge-row__value">128 条 · 虚拟滚动 + <code>显示全部 128 条</code></span></div>
        <div class="edge-row"><span class="edge-row__label">标签层级</span><span class="edge-row__value">缩进封顶，显示层级徽标 <span class="tree__badge">L5</span></span></div>
        <div class="edge-row"><span class="edge-row__label">极大块数</span><span class="edge-row__value">统计改为约数：<code>约 12.4 万块</code></span></div>`)}
    </div>
  </div>
</div>`;

// ---------- 路由 ----------
const ROUTES = {
  '': { title: 'S01 素材来源总览', render: S01 },
  '#/s01': { title: 'S01 素材来源总览', render: S01 },
  '#/s02': { title: 'S02 导入向导', render: S02 },
  '#/s03': { title: 'S03 覆盖矩阵与来源', render: S03 },
  '#/s04': { title: 'S04 问题生成对比', render: S04 },
  '#/s05': { title: 'S05 五大界面状态', render: S05 },
};
const ORDER = ['#/s01', '#/s02', '#/s03', '#/s04', '#/s05'];

function demoBar(current) {
  return `<nav class="demo-bar">
    ${ORDER.map((h) => `<a href="${h}" class="${h === current ? 'is-active' : ''}">${ROUTES[h].title}</a>`).join('')}
    <span class="demo-bar__sep"></span>
    <a href="#/s01">原型评审稿 · 非生产界面</a>
  </nav>`;
}

// 把 HTML 字符串挂载为真实 DOM 节点。
//
// 用 DOMParser 解析再 importNode，而不是给活元素赋 innerHTML：本原型的
// 所有字符串都是本文件内的静态字面量（没有用户输入、没有外部数据插值），
// 但 DOMParser 提供了一个显式的解析边界，且不会把解析结果直接当活 DOM 执行。
function mount(target, html) {
  const parsed = new DOMParser().parseFromString(html, 'text/html');
  const nodes = Array.from(parsed.body.childNodes).map((node) => document.importNode(node, true));
  target.replaceChildren(...nodes);
}

function render() {
  const hash = location.hash || '#/s01';
  const route = ROUTES[hash] || ROUTES['#/s01'];
  const app = document.querySelector('#app');
  if (!app) return;
  mount(app, `<div class="shell">${route.render()}</div>${demoBar(hash)}`);
  document.title = `${route.title} · Atelier 素材来源原型`;
}

window.addEventListener('hashchange', render);
render();
