package model

// 生成记录的状态取值与「是否可用于下游」的统一判定。
//
// 为什么需要这个文件（issue #7 的完整闭环）：
//
// 契约 docs/plans/issue-remediation-plan.md §1.3 冻结了三条状态取值与一条下游规则：
//
//	reasoning_records.status / reward_records.status 允许值扩展为
//	  generated | failed | invalid
//	invalid 表示「模型返回了合法 JSON 但是占位/无效内容」
//	下游过滤规则：**只有 generated 可进入导出与评估**；
//	  invalid 与 failed 同等看待，但分开计数以便区分「网络失败」与「模型摆烂」
//
// 数据生成侧（internal/llm 的 content_validator.go）已经会写 `invalid`，
// 但下游（导出、评估）原先只判断 `status != "failed"` —— `invalid` 因此**通过**了过滤，
// 占位内容仍然会进入训练集，issue #7 的目的（占位数据不得进入训练集）在出口处失守。
//
// 修这个缺口时**不能**在每个消费点各写一次字符串字面量：那正是它当初漂移的原因
// （生成侧新增了一个取值，消费侧的 6 处判断没人知道要同步）。
// 因此把「哪些状态可用于下游」收敛成这一处判定，消费点一律调用它。
//
// 放在 internal/model 的原因：它是叶子包（不 import 仓库内其他包），
// 因此 internal/store（评估源查询）与 apps/worker（导出/评估链路）都能引用，
// 不会引入新的依赖环。

// 记录状态取值。
const (
	// RecordStatusGenerated 表示模型产出了可用内容，**唯一**可进入导出与评估的取值。
	RecordStatusGenerated = "generated"
	// RecordStatusFailed 表示模型没答上来（网络、超时、非 JSON、解析失败）。
	RecordStatusFailed = "failed"
	// RecordStatusInvalid 表示模型答上来了但内容是占位/无效的（issue #7）。
	// 与 failed 同等看待（都不可用），但分开计数以便区分两类问题。
	RecordStatusInvalid = "invalid"
)

// RecordStatusUsableForDownstream 判定某条生成记录是否可进入导出与评估。
//
// 契约规则：只有 generated 可用。因此这里采用**白名单**而不是黑名单 ——
// 将来若再新增状态取值（例如 partial），默认行为是「不可用」，
// 而不会像 `!= "failed"` 那样把未知取值静默放行。
func RecordStatusUsableForDownstream(status string) bool {
	return status == RecordStatusGenerated
}
