package store

// 可续跑的生成阶段 → job 类型映射。
//
// 契约：docs/plans/eval-and-cleaning-plan.md 第 3.1 节。
// 只有通过 generation_runs 记录进度的阶段才能续跑，其余阶段不在表内。

// directionStageJobType 是方向生成阶段对应的 job 类型。
const directionStageJobType = "directions.generate"

// stageJobTypes 汇总所有可续跑阶段。
var stageJobTypes = map[string]string{
	DirectionStage: directionStageJobType,
}

// IsResumableStage 判断阶段是否支持断点续跑。
func IsResumableStage(stage string) bool {
	_, ok := stageJobTypes[stage]
	return ok
}

// JobTypeForStage 返回阶段对应的 job 类型；不支持时返回空串。
func JobTypeForStage(stage string) string {
	return stageJobTypes[stage]
}

// ShouldInitCursor 判断本次入队/续跑是否应该初始化游标。
//
// 不变量：已有断点进度的运行绝不能被空游标覆盖，否则续跑退化为从头重跑。
// 只有从未跑过的运行（没有已完成领域）才需要写入初始游标。
func ShouldInitCursor(cursor DirectionCursor) bool {
	return len(cursor.CompletedDomainIDs) == 0
}
