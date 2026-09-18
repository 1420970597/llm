// Package eval 提供数据集评估能力：维度目录、裁判接入、抽样打分、汇总报告。
//
// 本文件（catalog.go）由 lane L8 独占，提供「内置 ≥50 个长链思考评估维度 + 用户自定义维度」能力。
// 契约见 docs/plans/eval-and-cleaning-plan.md 第 2 节（0011 eval_dimensions）与第 3.8 节。
package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/1420970597/llm/internal/model"
)

// Dimension 内置维度的静态定义。落库时转换为 model.EvalDimension。
type Dimension struct {
	Key         string
	Name        string
	Category    string
	Description string
	Rubric      string
	ScaleMin    int
	ScaleMax    int
	Weight      float64
}

// 维度分类。前端按分类分组展示，Seed 与 categories 接口共用。
const (
	CategoryLongChain     = "long_chain"
	CategoryFaithfulness  = "faithfulness"
	CategoryInstruction   = "instruction"
	CategoryDomainFit     = "domain_fit"
	CategoryAnswerQuality = "answer_quality"
	CategoryRobustness    = "robustness"
	CategoryEfficiency    = "efficiency"
)

// 统一分档判据。所有维度采用 1~5 分制，避免不同维度分档口径不一致导致汇总失真。
const tierRubric = "分档判据（1~5 分）：1分=完全缺失，该维度的要求在整个输出中找不到任何对应内容；" +
	"2分=严重不足，仅有一处提及且未展开，或展开方向与问题场景明显不符；" +
	"3分=部分满足，有对应内容但存在跳跃、遗漏关键环节，或需要读者自行补全才能成立；" +
	"4分=基本满足，内容完整且方向正确，仅有细节瑕疵（措辞含糊、个别环节论证偏薄）；" +
	"5分=充分满足，内容完整、依据明确、与具体场景严格对应，可直接用于后续决策或训练。"

// 内置维度目录。覆盖 7 个类别，共 58 个维度，全部聚焦长链思考质量。
func BuiltinDimensions() []Dimension {
	return []Dimension{
		// ---------------- long_chain（12） ----------------
		{
			Key: "lc_step_sufficiency", Name: "推理步数充分性", Category: CategoryLongChain,
			Description: "长链推理的步骤数量是否足以从已知条件走到最终结论，是否存在一步跳到答案的捷径。",
			Rubric: "检查思维链是否把「已知条件 → 中间推断 → 结论」完整展开。少于 3 步且直接给结论记 1 分；" +
				"步骤数够但存在一步跨越多个推断环节记 3 分；每一步都有独立的推断动作且无跨越记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "lc_step_coherence", Name: "步骤衔接连贯性", Category: CategoryLongChain,
			Description: "相邻步骤之间是否有明确的推导关系，后一步是否由前一步的结论自然引出。",
			Rubric: "逐对检查相邻步骤。出现「突然换话题」或前后步骤无逻辑连接词/无共同变量记 1 分；" +
				"多数衔接成立但个别步骤靠读者猜测记 3 分；每对相邻步骤都能指出「因为上一步的哪个结论，所以有这一步」记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "lc_intermediate_validity", Name: "中间结论有效性", Category: CategoryLongChain,
			Description: "推理过程中给出的中间结论本身是否成立，是否基于已知条件而非凭空假定。",
			Rubric: "抽取每个中间结论，判断其依据是否来自题面条件或前序步骤。存在无依据的中间结论记 1 分；" +
				"中间结论有依据但依据表述含糊记 3 分；每个中间结论都标注了所依赖的条件编号或前序步骤记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "lc_self_correction", Name: "回溯与自我纠错", Category: CategoryLongChain,
			Description: "推理中是否识别并修正了自己的错误假设或走不通的路径，而不是把错误带进结论。",
			Rubric: "检查是否存在「先给出一个判断、随后发现不成立、并明确说明为何推翻」的段落。" +
				"全程无任何回溯且结论建立在未验证假设上记 1 分；有回溯但未说明推翻理由记 3 分；" +
				"明确写出被推翻的假设、推翻依据与替代路径记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "lc_assumption_explicit", Name: "假设显式化", Category: CategoryLongChain,
			Description: "题面未给出但推理必须依赖的前提，是否被显式声明为假设而非默默使用。",
			Rubric: "列出推理实际依赖但题面未提供的前提。默默使用且未声明记 1 分；" +
				"部分声明、部分隐含记 3 分；所有额外前提都以「假设：…」形式集中声明，并说明该假设不成立时结论如何变化记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "lc_branch_justification", Name: "分支决策论证", Category: CategoryLongChain,
			Description: "面临多个可选方案时，是否论证了为何选择当前方案而非其他方案。",
			Rubric: "识别推理中的选择点。只给一个方案且不提其他可能记 1 分；" +
				"列出备选但比较流于表面（只说「更优」不说依据）记 3 分；逐项比较备选方案的关键指标并给出取舍理由记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "lc_constraint_satisfaction", Name: "约束满足检查", Category: CategoryLongChain,
			Description: "是否逐条核对了题面给出的硬约束（数量、时限、资源、范围）并在结论中保持满足。",
			Rubric: "把题面硬约束逐条列出并与结论对照。结论违反任一硬约束记 1 分；" +
				"满足约束但未显式核对记 3 分；逐条列出约束并标注「已满足/未满足及原因」记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "lc_convergence", Name: "终态收敛性", Category: CategoryLongChain,
			Description: "推理是否真正收敛到一个明确可判定的结论，而非在多个可能间摇摆或开放收尾。",
			Rubric: "检查结尾是否给出唯一可执行的结论。以「取决于情况」收尾且未给判定条件记 1 分；" +
				"给出结论但附带大量未决条件记 3 分；给出唯一结论并说明该结论在何种条件下成立记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "lc_redundancy_control", Name: "冗余步骤控制", Category: CategoryLongChain,
			Description: "是否存在重复表述同一推断、或对结论无贡献的步骤。",
			Rubric: "标出对最终结论没有贡献的步骤。冗余步骤占比超过三分之一记 1 分；" +
				"存在少量重复表述记 3 分；每一步都推进了结论或排除了一个错误分支记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "lc_causal_chain", Name: "因果链完整性", Category: CategoryLongChain,
			Description: "从原因到结果的传递是否完整，是否存在把相关当成因果或跳过中间机制。",
			Rubric: "检查因果表述是否有中间机制支撑。直接把相关关系当作因果记 1 分；" +
				"机制存在但未说明传导路径记 3 分；完整写出「触发条件 → 中间机制 → 直接后果 → 最终影响」记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "lc_step_ordering", Name: "步骤顺序合理性", Category: CategoryLongChain,
			Description: "推理步骤的排列顺序是否符合依赖关系，是否出现先使用后定义的情况。",
			Rubric: "检查是否存在「用到了后面才引入的概念/数据」。存在先使用后定义记 1 分；" +
				"顺序基本合理但有一处需要回读记 3 分；严格按依赖关系排列，可自上而下单向阅读记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "lc_alternative_exploration", Name: "反事实分支探索", Category: CategoryLongChain,
			Description: "是否考虑了关键条件变化时的替代走向，体现对问题空间的理解深度。",
			Rubric: "检查是否有「如果 X 不成立，则…」的分析。完全没有记 1 分；" +
				"提到但未展开记 3 分；对至少一个关键条件给出完整的替代推理路径及其结论差异记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},

		// ---------------- faithfulness（9） ----------------
		{
			Key: "fa_fact_consistency", Name: "事实一致性", Category: CategoryFaithfulness,
			Description: "输出中的事实性陈述是否与公认知识一致，是否存在明确错误的事实。",
			Rubric: "逐条抽取可验证的事实性陈述并核对。出现明确错误事实记 1 分；" +
				"事实正确但有过度简化记 3 分；全部事实准确且表述严谨记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "fa_hallucination_control", Name: "幻觉控制", Category: CategoryFaithfulness,
			Description: "是否编造了不存在的数据、事件、文献、机构或接口。",
			Rubric: "标出所有具体到可核查粒度的断言（数字、名称、编号、日期）。出现编造项记 1 分；" +
				"存在无法核实但语气确定的断言记 3 分；所有具体断言都可溯源或明确标注为推测记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "fa_citation_traceability", Name: "引用可追溯", Category: CategoryFaithfulness,
			Description: "引用的依据是否给出了可追溯的来源描述，而非「研究表明」式的空引用。",
			Rubric: "检查引用表述。使用「据说」「研究表明」等无指向表述记 1 分；" +
				"给出类别化来源（如「该领域通用规程」）但不可精确定位记 3 分；给出可定位的来源名称、条款或条件编号记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},
		{
			Key: "fa_premise_no_fabrication", Name: "前提不臆造", Category: CategoryFaithfulness,
			Description: "是否把题面没有提供的信息当作既定事实使用。",
			Rubric: "对照题面列出推理使用的全部输入。使用了题面未给出的输入且未声明为假设记 1 分；" +
				"部分声明记 3 分；所有题面外输入均声明为假设或标注为待确认记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "fa_numeric_unit", Name: "数值与单位正确", Category: CategoryFaithfulness,
			Description: "涉及的数值、量纲、单位换算与量级是否准确且自洽。",
			Rubric: "检查每个数值的单位与量级。存在单位错误或量级明显不合理记 1 分；" +
				"单位正确但换算过程未展示记 3 分；数值、单位、换算过程与量级校验全部正确记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "fa_temporal_consistency", Name: "时间线一致性", Category: CategoryFaithfulness,
			Description: "涉及时间顺序的陈述是否自洽，是否存在因果倒置或时间冲突。",
			Rubric: "把输出中的时间点排序检查。存在因果倒置记 1 分；" +
				"时间顺序正确但未显式给出时序记 3 分；关键节点均标注时序且前后引用一致记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "fa_source_attribution", Name: "来源归属明确", Category: CategoryFaithfulness,
			Description: "把外部知识归因到恰当来源，而非与自身推断混为一谈。",
			Rubric: "区分「已知事实」与「本次推断」。两者混写无法区分记 1 分；" +
				"大体区分但边界模糊记 3 分；每段明确标注是引用既有知识还是本次推理所得记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "fa_scope_bounded", Name: "结论不越界", Category: CategoryFaithfulness,
			Description: "结论的适用范围是否与所给证据匹配，是否从有限前提推出普遍规律。",
			Rubric: "对照前提与结论的适用范围。由单例推出普遍结论记 1 分；" +
				"结论基本匹配但未声明适用边界记 3 分；明确写出结论成立的前提条件与失效边界记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "fa_internal_consistency", Name: "内部无矛盾", Category: CategoryFaithfulness,
			Description: "输出内部前后陈述是否自洽，是否存在自相矛盾的判断。",
			Rubric: "通读全文寻找相互否定的陈述。存在直接矛盾记 1 分；" +
				"存在表述差异但可调和记 3 分；全文陈述前后一致，术语与口径统一记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},

		// ---------------- instruction（7） ----------------
		{
			Key: "in_question_understanding", Name: "问题理解准确性", Category: CategoryInstruction,
			Description: "是否准确识别了问题真正在问什么，是否答非所问。",
			Rubric: "把问题拆成「问什么 + 要什么形式」。答非所问记 1 分；" +
				"方向正确但漏掉问题的某个限定记 3 分；完整识别问题意图与全部限定条件记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "in_constraint_compliance", Name: "指令约束遵守", Category: CategoryInstruction,
			Description: "对问题中显式给出的格式、长度、数量、范围等指令的遵守程度。",
			Rubric: "逐条核对显式指令。违反任一显式指令记 1 分；" +
				"全部遵守但个别指令执行得勉强记 3 分；全部显式指令精确遵守记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "in_format_compliance", Name: "输出格式合规", Category: CategoryInstruction,
			Description: "输出结构是否符合要求的格式（如分步、表格、JSON、指定字段）。",
			Rubric: "检查结构是否符合要求。结构完全不符记 1 分；" +
				"结构基本符合但字段缺失或命名不一致记 3 分；结构、字段名、层级全部符合要求记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "in_boundary_handling", Name: "边界条件处理", Category: CategoryInstruction,
			Description: "是否处理了题面隐含的边界情形（空值、极值、缺失信息、冲突条件）。",
			Rubric: "识别题面的边界情形。完全未处理记 1 分；" +
				"处理了主要边界但遗漏至少一处记 3 分；主要边界情形均给出明确处理方式记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "in_ambiguity_clarification", Name: "歧义澄清", Category: CategoryInstruction,
			Description: "面对含义不唯一的表述时，是否指出歧义并给出消解方式而非任选一种。",
			Rubric: "检查题面歧义点。直接按一种理解作答且未说明记 1 分；" +
				"指出了歧义但未说明选择依据记 3 分；指出歧义、说明所采用的解释并交代另一解释下的差异记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},
		{
			Key: "in_completeness_all_parts", Name: "多子问题全覆盖", Category: CategoryInstruction,
			Description: "当问题包含多个子问题时，是否每个子问题都得到了回答。",
			Rubric: "拆出全部子问题并核对。漏答任一子问题记 1 分；" +
				"全部作答但详略严重失衡记 3 分；全部子问题均作答且篇幅与重要性匹配记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "in_persona_role", Name: "角色与语气要求", Category: CategoryInstruction,
			Description: "若问题指定了角色、受众或语气（如面向指挥官、面向新手），输出是否贴合。",
			Rubric: "若问题未指定角色，本维度记 5 分（不适用即不扣分）。" +
				"指定了但输出完全忽略记 1 分；大体贴合但术语深度与受众不匹配记 3 分；术语深度、详略与语气均匹配指定受众记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 0.8,
		},

		// ---------------- domain_fit（8） ----------------
		{
			Key: "df_terminology", Name: "领域术语准确性", Category: CategoryDomainFit,
			Description: "领域专业术语的使用是否准确，是否存在术语误用或混用。",
			Rubric: "标出全部专业术语并核对用法。出现术语误用或概念混淆记 1 分；" +
				"术语使用正确但个别表述不地道记 3 分；术语准确、一致且符合该领域惯用表达记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "df_scenario_fit", Name: "场景贴合度", Category: CategoryDomainFit,
			Description: "回答是否紧扣问题给定的具体场景，而非套用通用模板。",
			Rubric: "检查内容是否引用了题面场景的具体要素。完全通用化、可套用到任何场景记 1 分；" +
				"引用了部分场景要素记 3 分；每个关键判断都落在题面给定的具体要素上记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "df_common_sense", Name: "专业常识一致性", Category: CategoryDomainFit,
			Description: "是否符合该领域的公认常识与基本规律，是否违反领域内不言自明的前提。",
			Rubric: "对照该领域基本常识。违反领域常识记 1 分；" +
				"不违反但存在外行式表述记 3 分；符合领域常识且体现从业者视角记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "df_feasibility", Name: "实操可行性", Category: CategoryDomainFit,
			Description: "给出的方案在现实中是否真的可以执行，是否停留在纸面。",
			Rubric: "检查方案所需条件是否现实可得。方案在现实中无法执行记 1 分；" +
				"可执行但未考虑执行中的现实阻力记 3 分；明确说明执行步骤、所需条件与主要阻力记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "df_regulation", Name: "法规与规程符合", Category: CategoryDomainFit,
			Description: "是否遵守该领域相关的法律法规、行业规程与操作规范。",
			Rubric: "若该领域无明确法规约束，本维度按 5 分计（不适用即不扣分）。" +
				"方案与现行法规或规程冲突记 1 分；未违反但未提及合规要求记 3 分；明确引用相关规程并说明合规依据记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "df_resource_constraint", Name: "资源约束现实性", Category: CategoryDomainFit,
			Description: "是否考虑了人力、装备、时间、经费等资源约束，而非假设资源无限。",
			Rubric: "检查方案是否受资源约束。假设资源无限记 1 分；" +
				"提到资源但未量化记 3 分；给出关键资源的数量级估算与分配方案记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "df_environment_context", Name: "环境条件适配", Category: CategoryDomainFit,
			Description: "是否考虑了地理、气候、时段、电磁等环境条件对方案的影响。",
			Rubric: "检查环境因素是否进入推理。完全未考虑记 1 分；" +
				"提及环境但未分析影响记 3 分；逐项分析关键环境条件对方案的影响与应对记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},
		{
			Key: "df_stakeholder", Name: "相关方视角完整", Category: CategoryDomainFit,
			Description: "是否考虑了方案涉及的各相关方的目标、权限与可能反应。",
			Rubric: "列出方案涉及的相关方。仅从单一视角出发记 1 分；" +
				"提到多个相关方但未分析其诉求差异记 3 分；逐个相关方分析目标、权限与可能反应记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},

		// ---------------- answer_quality（8） ----------------
		{
			Key: "aq_conclusion_clarity", Name: "结论明确性", Category: CategoryAnswerQuality,
			Description: "最终结论是否清晰、无歧义、可直接引用。",
			Rubric: "检查结论段是否可直接引用。结论含糊或需读者自行归纳记 1 分；" +
				"结论明确但夹带大量条件从句记 3 分；结论一句话可引用，条件另段说明记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "aq_completeness", Name: "答案完整性", Category: CategoryAnswerQuality,
			Description: "答案是否覆盖了从问题到结论所需的全部必要环节，无关键缺项。",
			Rubric: "列出必要环节清单并核对。缺失关键环节记 1 分；" +
				"环节齐全但部分展开不足记 3 分；全部环节完整展开记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "aq_actionability", Name: "可执行性", Category: CategoryAnswerQuality,
			Description: "读者能否据此直接行动，是否给出了可操作的步骤而非原则性建议。",
			Rubric: "检查是否给出可执行动作。只有原则性表述记 1 分；" +
				"给出动作但缺执行条件或责任主体记 3 分；动作、执行条件、责任主体与验收方式齐备记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "aq_risk_disclosure", Name: "风险提示充分", Category: CategoryAnswerQuality,
			Description: "是否主动提示了方案的风险、副作用与失败后果。",
			Rubric: "检查风险段落。完全未提风险记 1 分；" +
				"提及风险但未给应对记 3 分；逐项列出风险、触发条件与应对措施记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "aq_prioritization", Name: "优先级排序", Category: CategoryAnswerQuality,
			Description: "多个待办事项是否按重要性或时序排了优先级，而非平铺罗列。",
			Rubric: "检查多事项是否有序。平铺罗列无优先级记 1 分；" +
				"有排序但未说明排序依据记 3 分；给出优先级并说明依据（紧迫度/依赖关系/影响面）记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},
		{
			Key: "aq_contingency", Name: "备选方案与应急预案", Category: CategoryAnswerQuality,
			Description: "主方案失效时是否有可立即切换的备选路径。",
			Rubric: "检查是否有备选方案。只有单一方案记 1 分；" +
				"有备选但未说明切换条件记 3 分；给出备选方案、切换触发条件与切换代价记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "aq_metric_definition", Name: "成功标准可度量", Category: CategoryAnswerQuality,
			Description: "是否定义了判断方案成功的可度量标准，而非「效果好」这类模糊表述。",
			Rubric: "检查是否给出可度量标准。无标准或标准模糊记 1 分；" +
				"给出指标但无目标值记 3 分；给出指标、目标值与观测方式记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},
		{
			Key: "aq_concision", Name: "表达精炼度", Category: CategoryAnswerQuality,
			Description: "在保持完整的前提下，表达是否紧凑，是否存在大量无效铺垫。",
			Rubric: "检查无效铺垫占比。开场铺垫与总结套话超过全文三分之一记 1 分；" +
				"存在明显重复表述记 3 分；每一段都承载独立信息记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},

		// ---------------- robustness（8） ----------------
		{
			Key: "rb_anti_induction", Name: "抗诱导", Category: CategoryRobustness,
			Description: "面对题面中预设的错误前提或诱导性表述，是否坚持事实而非顺着说。",
			Rubric: "若题面无诱导性表述，本维度记 5 分（不适用即不扣分）。" +
				"接受错误前提并据此推理记 1 分；察觉但未明确指出记 3 分；明确指出前提有误并给出正确表述记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.3,
		},
		{
			Key: "rb_refusal_handling", Name: "拒答处理", Category: CategoryRobustness,
			Description: "是否出现「对不起，我不能」式的拒答或空洞回避，导致数据不可用于训练。",
			Rubric: "检查全文是否包含拒答模板。出现明确拒答且未给出替代内容记 1 分；" +
				"出现推脱式表述但仍给出了部分有效内容记 3 分；全程正面作答，遇不确定处以不确定表述代替拒答记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.5,
		},
		{
			Key: "rb_safety_compliance", Name: "安全合规", Category: CategoryRobustness,
			Description: "输出是否规避了违法、危险或不道德的内容，同时不因过度保守而损失有效信息。",
			Rubric: "检查是否存在违规内容。出现违法或危险内容记 1 分；" +
				"无违规但过度保守导致有效信息缺失记 3 分；既无违规内容又完整保留了合规范围内的有效信息记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.4,
		},
		{
			Key: "rb_uncertainty_expression", Name: "不确定性表达", Category: CategoryRobustness,
			Description: "对把握不足的部分是否如实标注不确定性，而非一律用确定语气。",
			Rubric: "检查断言语气与依据强度的匹配。对无依据内容使用确定语气记 1 分；" +
				"部分标注不确定性记 3 分；不确定性标注与证据强度一一对应（确定/较可能/待验证）记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "rb_edge_case_resilience", Name: "极端输入韧性", Category: CategoryRobustness,
			Description: "当题面信息极少、自相矛盾或超出常识范围时，输出是否仍保持结构完整。",
			Rubric: "若题面为常规输入，本维度记 5 分（不适用即不扣分）。" +
				"信息不足时输出崩溃为空洞表述记 1 分；结构保持但未指出输入不足记 3 分；明确说明输入不足及其对结论的影响，仍给出有条件结论记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "rb_consistency_across_runs", Name: "跨次生成一致性", Category: CategoryRobustness,
			Description: "同一问题多次生成时结论是否稳定，是否出现随机翻转。",
			Rubric: "对比同问题多次生成的结论。结论方向随机翻转记 1 分；" +
				"结论一致但关键细节每次不同记 3 分；结论与关键推理路径稳定，仅表述细节有差异记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "rb_bias_control", Name: "偏见控制", Category: CategoryRobustness,
			Description: "是否包含基于地域、群体、身份的无依据倾向性判断。",
			Rubric: "检查是否存在无依据的群体性判断。出现明显倾向性表述记 1 分；" +
				"存在可商榷的用词记 3 分；全部判断基于事实与条件而非群体属性记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},
		{
			Key: "rb_data_sensitivity", Name: "数据敏感性处理", Category: CategoryRobustness,
			Description: "涉及真实人员、单位、坐标等敏感信息时是否做了必要的脱敏处理。",
			Rubric: "若内容不涉及敏感信息，本维度记 5 分（不适用即不扣分）。" +
				"直接使用可定位的真实敏感信息记 1 分；部分脱敏记 3 分；全部敏感信息以占位或泛化表述处理且不影响可读性记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.2,
		},

		// ---------------- efficiency（6） ----------------
		{
			Key: "ef_token_efficiency", Name: "Token 效率", Category: CategoryEfficiency,
			Description: "在达成同等结论质量的前提下，是否用更少的篇幅完成推理。",
			Rubric: "对比结论质量与篇幅。同等质量下篇幅超出必要量一倍以上记 1 分；" +
				"篇幅偏长但信息密度尚可记 3 分；无冗余铺垫且信息密度高记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "ef_thought_density", Name: "思考密度", Category: CategoryEfficiency,
			Description: "单位篇幅内包含的有效推理步骤数量，衡量是否把篇幅用在推理上。",
			Rubric: "统计有效推理步骤与总篇幅之比。低于每 200 字一个推理步骤记 1 分；" +
				"每 100~200 字一个推理步骤记 3 分；每 100 字以内即有新的推理推进记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1.1,
		},
		{
			Key: "ef_key_info_ratio", Name: "关键信息占比", Category: CategoryEfficiency,
			Description: "输出中真正被后续推理或结论使用的信息占比。",
			Rubric: "标出被引用的信息与未被引用的信息。关键信息占比低于三分之一记 1 分；" +
				"占比在一半左右记 3 分；绝大多数信息都被后续步骤引用记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "ef_no_padding", Name: "无套话填充", Category: CategoryEfficiency,
			Description: "是否存在「首先/其次/最后」「综上所述」这类不承载信息的模板化填充。",
			Rubric: "标出纯过渡性语句。套话占比超过全文四分之一记 1 分；" +
				"存在少量套话记 3 分；过渡语均承载逻辑关系（如「由于…因此…」）记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 1,
		},
		{
			Key: "ef_parallel_structure", Name: "结构复用度", Category: CategoryEfficiency,
			Description: "处理同类子问题时是否复用统一结构，便于批量阅读与后续训练对齐。",
			Rubric: "检查同类子问题的组织方式。每段结构各不相同记 1 分；" +
				"大体一致但字段顺序有出入记 3 分；同类子问题使用完全一致的结构记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 0.9,
		},
		{
			Key: "ef_early_termination", Name: "适时收敛", Category: CategoryEfficiency,
			Description: "结论一旦确立即停止展开，而非继续堆砌无新增信息的论证。",
			Rubric: "检查结论之后是否还有无新增信息的段落。结论后仍有大段重复论证记 1 分；" +
				"有少量补充记 3 分；结论后仅保留必要收束记 5 分。" + tierRubric,
			ScaleMin: 1, ScaleMax: 5, Weight: 0.9,
		},
	}
}

// Categories 返回内置维度覆盖的全部分类，顺序固定（便于前端稳定渲染）。
func Categories() []string {
	seen := map[string]struct{}{}
	ordered := []string{}
	for _, dim := range BuiltinDimensions() {
		if _, exists := seen[dim.Category]; exists {
			continue
		}
		seen[dim.Category] = struct{}{}
		ordered = append(ordered, dim.Category)
	}
	sort.Strings(ordered)
	return ordered
}

// DimensionSink 是 Seed 需要的写入能力。由 store.EvalDimensionStore 实现。
type DimensionSink interface {
	// UpsertBuiltin 幂等写入内置维度，返回本次实际新增条数。
	UpsertBuiltin(ctx context.Context, dimensions []model.EvalDimension) (int, error)
	// Count 返回表中维度总数。
	Count(ctx context.Context) (int, error)
}

// ToModel 把静态定义转换为落库模型。内置维度统一标记 is_builtin=true、is_active=true。
func (d Dimension) ToModel() model.EvalDimension {
	return model.EvalDimension{
		Key:         d.Key,
		Name:        d.Name,
		Category:    d.Category,
		Description: d.Description,
		Rubric:      d.Rubric,
		ScaleMin:    d.ScaleMin,
		ScaleMax:    d.ScaleMax,
		IsBuiltin:   true,
		IsActive:    true,
		Weight:      d.Weight,
	}
}

// Validate 检查目录自身的一致性。Seed 与单测共用，避免内置目录悄悄退化。
func Validate(dimensions []Dimension) error {
	if len(dimensions) < 50 {
		return fmt.Errorf("内置维度不足 50 个：当前 %d 个", len(dimensions))
	}
	seen := map[string]struct{}{}
	for index, dim := range dimensions {
		key := strings.TrimSpace(dim.Key)
		if key == "" {
			return fmt.Errorf("第 %d 个维度缺少 key", index)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("维度 key 重复：%s", key)
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(dim.Name) == "" {
			return fmt.Errorf("维度 %s 缺少 name", key)
		}
		if strings.TrimSpace(dim.Category) == "" {
			return fmt.Errorf("维度 %s 缺少 category", key)
		}
		if len([]rune(strings.TrimSpace(dim.Rubric))) <= 40 {
			return fmt.Errorf("维度 %s 的 rubric 过于简略，必须给出可操作的分档判据", key)
		}
		if dim.ScaleMax <= dim.ScaleMin {
			return fmt.Errorf("维度 %s 的分值区间非法：%d~%d", key, dim.ScaleMin, dim.ScaleMax)
		}
		if dim.Weight <= 0 {
			return fmt.Errorf("维度 %s 的权重必须为正数", key)
		}
	}
	return nil
}

// Seed 幂等地把内置维度写入数据库，返回本次新增条数与写入后的总数。
func Seed(ctx context.Context, sink DimensionSink) (inserted int, total int, err error) {
	if sink == nil {
		return 0, 0, fmt.Errorf("dimension sink is required")
	}
	builtin := BuiltinDimensions()
	if err := Validate(builtin); err != nil {
		return 0, 0, err
	}

	models := make([]model.EvalDimension, 0, len(builtin))
	for _, dim := range builtin {
		models = append(models, dim.ToModel())
	}

	inserted, err = sink.UpsertBuiltin(ctx, models)
	if err != nil {
		return 0, 0, err
	}
	total, err = sink.Count(ctx)
	if err != nil {
		return inserted, 0, err
	}
	return inserted, total, nil
}
