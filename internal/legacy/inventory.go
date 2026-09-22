// Package legacy 承载「旧数据迁移」的只读盘点与（T31 的）幂等导入。
//
// 本文件是 Issue #160 T30 的盘点与映射规划实现。
//
// 契约：docs/plans/atelier-implementation.md §6.3（T30 落点 `cmd/studio-migrate/`）
// 与 #160 T30 的原文要求：
//
//   - 只读盘点 datasets/domains/questions/standards/sft/reasoning/grpo/rewards/
//     eval/cleaning/artifacts 的数量、大小、状态、来源、对象存在性；
//   - 设计「旧 dataset → legacy-origin 项目 / 有证据的运行 → 批次 /
//     只能确定当前内容的 → 导入快照批次」映射；
//   - 旧库没有可靠 owner 时归入受限待归属区，由管理员显式分派；
//   - dry-run 不写业务数据、不调用模型；输出各类数量与示例 ID；
//   - 所有未知来源显式标记，源数据不删。
//
// 三条**结构性**保证（不靠「记得别写」）：
//
//  1. **只读事务**：全部查询跑在 `BEGIN READ ONLY` 里。任何意外的写操作会被
//     Postgres 直接拒绝（`cannot execute INSERT in a read-only transaction`），
//     而不是留下半份迁移数据。这比「代码里没有 INSERT」强得多 ——
//     后者会在下一次重构引入一条 UPDATE 时静默失效。
//  2. **不引用 any LLM 包**：本包只 import `pgx`，因此「不调用模型」是结构性事实。
//     测试直接断言源码 import 列表里没有 `internal/llm`。
//  3. **不删任何源数据**：本工具没有任何 DELETE/TRUNCATE 语句（同样由测试断言）。
package legacy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 映射决策类型（T30 的「旧内容优先级与冲突策略」逐条落点）。
const (
	// DecisionMappableProject：有生成证据（generation_runs / questions），
	// 可以映射为 legacy-origin 项目（保留原始 dataset ID 以便追溯）。
	DecisionMappableProject = "mappable_project"
	// DecisionImportSnapshot：只能确定「当前内容」，没有可复现的运行证据，
	// 因此导入为一个独立的**快照批次**，不冒充可复现的历史运行。
	DecisionImportSnapshot = "import_snapshot"
	// DecisionNeedsOwner：旧库没有可靠 owner（created_by 为空或用户已删除），
	// 归入受限待归属区，由管理员显式分派 —— 不能默认给所有人读权限。
	DecisionNeedsOwner = "needs_owner"
	// DecisionConflict：目标项目名在同工作区已存在，导入会覆盖或产生歧义。
	DecisionConflict = "conflict"
	// DecisionNeedsReeval：有评估运行但缺少配置版本（当时用哪套维度/裁判），
	// 只能作为「旧证据/未验证」保留，不能当作 Atelier 的质量结论。
	DecisionNeedsReeval = "needs_reeval"
	// DecisionReasoningSeparateSource：旧 reasoning_records 保留为**独立来源**，
	// 不与 SFT 一等表合并（合并会让「同题两条不同来源」的冲突被静默抹掉）。
	DecisionReasoningSeparateSource = "reasoning_separate_source"
	// DecisionGRPONotTeacherMaterial：旧 reward_records 的分数**不映射**为
	// GRPO 教师材料（分数是奖励信号，不是判分标准）。
	DecisionGRPONotTeacherMaterial = "grpo_not_teacher_material"
	// DecisionArtifactUnverified：旧工件路径可能已被覆盖，只能验证现存字节，
	// 保留为「历史工件/未验证」，不自动标为 Atelier 已发布。
	DecisionArtifactUnverified = "artifact_unverified"
	// DecisionMissingReference：工件引用的存储配置已不存在，无法定位对象。
	DecisionMissingReference = "missing_reference"
	// DecisionUnknownSource：来源字段为空或无法归类 —— 显式标记，不猜。
	DecisionUnknownSource = "unknown_source"
)

// 决策严重度。
const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityBlocker = "blocker"
)

// Decision 是一条映射决策（带可行动的原因与示例 ID）。
type Decision struct {
	Kind      string  `json:"kind"`
	Severity  string  `json:"severity"`
	Reason    string  `json:"reason"`
	SampleIDs []int64 `json:"sampleIds,omitempty"`
}

// TableStat 是一张表的盘点结果。
type TableStat struct {
	Name     string           `json:"name"`
	Rows     int64            `json:"rows"`
	SizeByte int64            `json:"sizeBytes"`
	Statuses map[string]int64 `json:"statuses,omitempty"`
}

// DatasetPlan 是一个旧 dataset 的映射规划。
type DatasetPlan struct {
	DatasetID int64  `json:"datasetId"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	OwnerID   *int64 `json:"ownerId,omitempty"`

	Domains          int64 `json:"domains"`
	Questions        int64 `json:"questions"`
	SFTRecords       int64 `json:"sftRecords"`
	ReasoningRecords int64 `json:"reasoningRecords"`
	RewardRecords    int64 `json:"rewardRecords"`
	GRPOPrompts      int64 `json:"grpoPrompts"`
	EvalRuns         int64 `json:"evalRuns"`
	CleaningRuns     int64 `json:"cleaningRuns"`
	Artifacts        int64 `json:"artifacts"`
	MissingArtifacts int64 `json:"missingArtifacts"`
	GenerationRuns   int64 `json:"generationRuns"`

	Decisions []Decision `json:"decisions"`
}

// Report 是 dry-run 报告（人类可读摘要 + JSON 落盘用同一份数据）。
type Report struct {
	GeneratedAt time.Time `json:"generatedAt"`
	DryRun      bool      `json:"dryRun"`
	ReadOnly    bool      `json:"readOnlyTransaction"`

	Tables   []TableStat   `json:"tables"`
	Datasets []DatasetPlan `json:"datasets"`
	// Totals 是每个决策类型的计数（T30 验收项「输出预期/可映射/冲突/缺失/
	// 需人工归属/需重评数量与示例 ID」）。
	Totals map[string]int `json:"totals"`
	// Notes 是**必须显式给出的未知项**：本工具无法验证什么、为什么、谁来补。
	Notes []string `json:"notes"`
}

// Options 控制盘点范围。
type Options struct {
	// SampleLimit 每个决策保留的示例 ID 数（默认 5）。
	SampleLimit int
	// Now 允许测试固定时间。
	Now func() time.Time
}

// 盘点覆盖的表（固定清单，不动态发现：动态发现会让「新增表未被盘点」
// 悄无声息，而迁移计划最怕的就是漏掉一张表）。
var inventoryTables = []string{
	"datasets", "dataset_runs", "domains", "domain_edges", "questions",
	"chain_standards", "chain_standard_versions",
	"sft_records", "reasoning_records", "reward_records", "grpo_prompts",
	"eval_runs", "eval_items", "eval_item_scores", "eval_summaries", "eval_run_judges",
	"cleaning_runs", "cleaning_findings", "cleaning_keywords",
	"artifacts", "generation_runs",
	"model_providers", "storage_profiles", "users", "workspaces",
	"projects", "batches", "sample_versions", "releases",
}

// 状态分布：只对**确实有 status 列**的表统计（避免为了统一而编造）。
//
// domains 用 review_status 而不是 status：它根本没有 status 列，
// 猜一个列名会让盘点在第 3 张表上就报 SQLSTATE 42703 —— 而那是
// 「工具没跑完就给出一个看起来正常的报告」的相反面（直接失败，可以接受）。
var statusTables = map[string]string{
	"datasets":          "status",
	"domains":           "review_status",
	"questions":         "status",
	"sft_records":       "status",
	"reasoning_records": "status",
	"reward_records":    "status",
	"grpo_prompts":      "status",
	"eval_runs":         "status",
	"cleaning_runs":     "status",
	"generation_runs":   "status",
	"batches":           "status",
	"releases":          "status",
}

// Inventory 执行只读盘点并产出报告。
//
// 整个函数在**只读事务**里运行：任何写操作都会被 Postgres 拒绝。
func Inventory(ctx context.Context, pool *pgxpool.Pool, options Options) (Report, error) {
	if options.SampleLimit <= 0 {
		options.SampleLimit = 5
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return Report{}, fmt.Errorf("开启只读事务失败：%w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	report := Report{
		GeneratedAt: options.Now().UTC(),
		DryRun:      true,
		ReadOnly:    true,
		Totals:      map[string]int{},
		Notes:       []string{},
	}

	if err := inventoryTablesInto(ctx, tx, &report); err != nil {
		return Report{}, err
	}
	// 表不齐时**不产出逐个 dataset 的决策**：决策依赖 domains/questions/
	// generation_runs/eval_runs/artifacts 等多张表，缺一张就会得到
	// 「没有证据 → 导入快照」这类**看起来合理但其实是错的**结论。
	// 宁可只给「盘点不完整」的明确说明，也不给一个误导性的导入范围。
	if reportHasMissingTables(&report) {
		report.Notes = append(report.Notes,
			"因为迁移不完整，本次**不产出**逐个 dataset 的映射决策："+
				"决策依赖多张表（领域/问题/生成运行/评估/工件），缺一张就会得出看似合理其实错误的结论。"+
				"请先把迁移应用到最新后重跑。")
	} else if err := planDatasets(ctx, tx, options, &report); err != nil {
		return Report{}, err
	}
	buildNotes(&report)
	return report, nil
}

// reportHasMissingTables 判断盘点时是否遇到了缺表（由 inventoryTablesInto 写入 Note）。
func reportHasMissingTables(report *Report) bool {
	for _, note := range report.Notes {
		if strings.Contains(note, "数据库迁移不完整") {
			return true
		}
	}
	return false
}

// inventoryTablesInto 统计每张表的行数、大小与状态分布。
//
// 缺失的表**不报错**而是显式记入 Notes：实测部署中的开发库可能停在较旧的
// 迁移上（缺失 releases/sample_versions 等新表），而那时「直接失败」会把
// 一个可以部分完成的盘点变成一个字也拿不到的错误；反之静默跳过又会让
// 报告看起来完整。正确做法是：跳过它，并在报告里当 blocker 写清楚。
func inventoryTablesInto(ctx context.Context, tx pgx.Tx, report *Report) error {
	missing, err := missingTables(ctx, tx)
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"数据库迁移不完整：缺少 %d 张表（%s）。本次盘点**未覆盖**这些表，"+
				"结果不能用于确认导入范围；请先把迁移应用到最新后重跑",
			len(missing), strings.Join(sortedKeys(missing), ", ")))
	}

	for _, name := range inventoryTables {
		if missing[name] {
			continue
		}
		var stat TableStat
		stat.Name = name
		// 表名来自固定清单（不是外部输入），因此这里的字符串拼接是安全的；
		// 用拼接而不是参数化是因为 SQL 标识符不能参数化。
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM `+quoteIdent(name)).Scan(&stat.Rows); err != nil {
			return fmt.Errorf("统计 %s 行数失败：%w", name, err)
		}
		if err := tx.QueryRow(ctx, `
      SELECT COALESCE(pg_total_relation_size(to_regclass($1)), 0)`, "public."+name).Scan(&stat.SizeByte); err != nil {
			return fmt.Errorf("统计 %s 大小失败：%w", name, err)
		}
		if column, ok := statusTables[name]; ok {
			statuses, err := statusBreakdown(ctx, tx, name, column)
			if err != nil {
				return err
			}
			stat.Statuses = statuses
		}
		report.Tables = append(report.Tables, stat)
	}
	return nil
}

// missingTables 返回固定清单里在当前库里不存在的表。
func missingTables(ctx context.Context, tx pgx.Tx) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
    SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`)
	if err != nil {
		return nil, fmt.Errorf("读取表清单失败：%w", err)
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	for _, name := range inventoryTables {
		if !existing[name] {
			missing[name] = true
		}
	}
	return missing, nil
}

// statusBreakdown 统计一张表按状态分组的行数。
//
// 表名与列名都来自上面的**固定映射**，因此拼接安全（标识符不能参数化）。
func statusBreakdown(ctx context.Context, tx pgx.Tx, table, column string) (map[string]int64, error) {
	rows, err := tx.Query(ctx, `SELECT `+quoteIdent(column)+`::text, COUNT(*) FROM `+
		quoteIdent(table)+` GROUP BY 1 ORDER BY 2 DESC`)
	if err != nil {
		return nil, fmt.Errorf("统计 %s.%s 状态失败：%w", table, column, err)
	}
	defer rows.Close()
	statuses := map[string]int64{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		statuses[status] = count
	}
	return statuses, rows.Err()
}

// planDatasets 为每个旧 dataset 生成映射决策。
//
// datasets 表本身缺失（迁移停在 0001 之前的新库）时返回空计划而不是报错：
// 那时「没有旧数据」是真实结论，而不是工具故障。
func planDatasets(ctx context.Context, tx pgx.Tx, options Options, report *Report) error {
	missing, err := missingTables(ctx, tx)
	if err != nil {
		return err
	}
	if missing["datasets"] {
		report.Notes = append(report.Notes, "datasets 表不存在：当前库没有任何旧数据可盘点")
		return nil
	}
	rows, err := tx.Query(ctx, `
    SELECT id, name, status, created_by FROM datasets ORDER BY id`)
	if err != nil {
		return fmt.Errorf("读取 datasets 失败：%w", err)
	}
	plans := []DatasetPlan{}
	for rows.Next() {
		var plan DatasetPlan
		if err := rows.Scan(&plan.DatasetID, &plan.Name, &plan.Status, &plan.OwnerID); err != nil {
			rows.Close()
			return err
		}
		plans = append(plans, plan)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for index := range plans {
		plan := &plans[index]
		if err := fillDatasetCounts(ctx, tx, plan); err != nil {
			return err
		}
		if err := decideDataset(ctx, tx, options, plan); err != nil {
			return err
		}
	}
	report.Datasets = plans
	for _, plan := range plans {
		for _, decision := range plan.Decisions {
			report.Totals[decision.Kind]++
		}
	}
	return nil
}

// fillDatasetCounts 统计一个 dataset 的各来源内容量。
func fillDatasetCounts(ctx context.Context, tx pgx.Tx, plan *DatasetPlan) error {
	// 每个计数都是一条**字面量 SQL**（不拼接外部输入）；表名与列名都写死。
	counts := []struct {
		target *int64
		sql    string
	}{
		{&plan.Domains, `SELECT COUNT(*) FROM domains WHERE dataset_id = $1`},
		{&plan.Questions, `SELECT COUNT(*) FROM questions WHERE dataset_id = $1`},
		{&plan.SFTRecords, `SELECT COUNT(*) FROM sft_records WHERE dataset_id = $1`},
		{&plan.ReasoningRecords, `SELECT COUNT(*) FROM reasoning_records WHERE dataset_id = $1`},
		{&plan.RewardRecords, `SELECT COUNT(*) FROM reward_records WHERE dataset_id = $1`},
		{&plan.GRPOPrompts, `SELECT COUNT(*) FROM grpo_prompts WHERE dataset_id = $1`},
		{&plan.EvalRuns, `SELECT COUNT(*) FROM eval_runs WHERE dataset_id = $1`},
		{&plan.CleaningRuns, `SELECT COUNT(*) FROM cleaning_runs WHERE dataset_id = $1`},
		{&plan.Artifacts, `SELECT COUNT(*) FROM artifacts WHERE dataset_id = $1`},
		{&plan.GenerationRuns, `SELECT COUNT(*) FROM generation_runs WHERE dataset_id = $1`},
	}
	for _, count := range counts {
		if err := tx.QueryRow(ctx, count.sql, plan.DatasetID).Scan(count.target); err != nil {
			return fmt.Errorf("统计 dataset %d 失败：%w", plan.DatasetID, err)
		}
	}
	// 工件里 object_key 为空的记录**无法验证字节**，单独计数。
	if err := tx.QueryRow(ctx, `
    SELECT COUNT(*) FROM artifacts WHERE dataset_id = $1 AND COALESCE(object_key, '') = ''`,
		plan.DatasetID).Scan(&plan.MissingArtifacts); err != nil {
		return err
	}
	return nil
}

// decideDataset 产出一个 dataset 的全部映射决策。
//
// 决策顺序即优先级：先「能不能归属」，再「能不能映射」，最后是
// 各来源的保留策略。顺序不能随意调整 —— 例如先判「无 owner」再判
// 「有生成证据」，得到的是一条需要人工归属的 blocker，而不是
// 「可映射到某人的项目」这种会静默转移所有权的结论。
func decideDataset(ctx context.Context, tx pgx.Tx, options Options, plan *DatasetPlan) error {
	sampleLimit := options.SampleLimit

	// 1. 归属：旧库常常没有可靠 owner。
	if plan.OwnerID == nil {
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionNeedsOwner, Severity: SeverityBlocker,
			Reason: "dataset.created_by 为空：旧库没有可靠归属，必须由管理员显式分派到工作区，" +
				"不能默认给任意用户或所有人读权限",
		})
	} else {
		var ownerExists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, *plan.OwnerID).Scan(&ownerExists); err != nil {
			return err
		}
		if !ownerExists {
			plan.Decisions = append(plan.Decisions, Decision{
				Kind: DecisionNeedsOwner, Severity: SeverityBlocker,
				Reason: fmt.Sprintf("dataset.created_by = %d 对应的用户已不存在：归属无法自动确定", *plan.OwnerID),
			})
		}
	}

	// 2. 映射形态：有生成证据 → legacy-origin 项目；否则 → 导入快照批次。
	hasEvidence := plan.GenerationRuns > 0
	if hasEvidence {
		ids, err := sampleIDs(ctx, tx, `SELECT id FROM generation_runs WHERE dataset_id = $1 ORDER BY id`, plan.DatasetID, sampleLimit)
		if err != nil {
			return err
		}
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionMappableProject, Severity: SeverityInfo,
			Reason:    fmt.Sprintf("有 %d 次生成运行记录：可映射为 legacy-origin 项目，并把这些运行映射为批次（保留原 dataset ID 以便追溯）", plan.GenerationRuns),
			SampleIDs: ids,
		})
	} else if plan.Questions > 0 || plan.Domains > 0 {
		ids, err := sampleIDs(ctx, tx, `SELECT id FROM questions WHERE dataset_id = $1 ORDER BY id`, plan.DatasetID, sampleLimit)
		if err != nil {
			return err
		}
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionImportSnapshot, Severity: SeverityWarning,
			Reason: "没有生成运行记录，只能确定当前内容：导入为**快照批次**，" +
				"不冒充可复现的历史运行（不能声称这些内容当时是怎么生成的）",
			SampleIDs: ids,
		})
	} else if plan.SFTRecords == 0 && plan.ReasoningRecords == 0 && plan.RewardRecords == 0 && plan.GRPOPrompts == 0 {
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionUnknownSource, Severity: SeverityInfo,
			Reason: "没有任何领域/问题/内容记录：可能是空壳 dataset，映射时一并归档而不是导入",
		})
	}

	// 3. 项目名冲突：映射到同工作区的项目名若已存在，导入会产生歧义。
	if plan.Decisions != nil {
		var conflict bool
		if err := tx.QueryRow(ctx, `
      SELECT EXISTS(SELECT 1 FROM projects WHERE name = $1)`, plan.Name).Scan(&conflict); err != nil {
			return err
		}
		if conflict {
			plan.Decisions = append(plan.Decisions, Decision{
				Kind: DecisionConflict, Severity: SeverityWarning,
				Reason: fmt.Sprintf("已存在同名项目 %q：导入前必须改名或合并，否则无法区分哪一个是「当时那份数据」", plan.Name),
			})
		}
	}

	// 4. 旧评估证据：缺配置版本只能标「未验证」，不能当作 Atelier 质量结论。
	if plan.EvalRuns > 0 {
		ids, err := sampleIDs(ctx, tx, `SELECT id FROM eval_runs WHERE dataset_id = $1 ORDER BY id`, plan.DatasetID, sampleLimit)
		if err != nil {
			return err
		}
		var missingDimensionConfig int64
		// dimension_keys 是 JSONB（不是 text[]）：必须先判类型再取长度，
		// 否则非数组值时 jsonb_array_length 会直接报错（实测踩过一次）。
		if err := tx.QueryRow(ctx, `
      SELECT COUNT(*) FROM eval_runs
      WHERE dataset_id = $1
        AND COALESCE(CASE WHEN jsonb_typeof(dimension_keys) = 'array'
                          THEN jsonb_array_length(dimension_keys) ELSE 0 END, 0) = 0`,
			plan.DatasetID).Scan(&missingDimensionConfig); err != nil {
			return err
		}
		reason := "旧 eval_runs 只作为 legacy evidence 保留，标注为**未验证**；缺配置版本时不得当作 Atelier 的质量结论"
		if missingDimensionConfig > 0 {
			reason = fmt.Sprintf("%d 次旧评估运行缺少维度配置（无法复算当时的口径）：只能保留为未验证的历史证据；"+
				"要让结论可用必须在 Atelier 里重新创建实验", missingDimensionConfig)
		}
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionNeedsReeval, Severity: SeverityWarning, Reason: reason, SampleIDs: ids,
		})
	}

	// 5. reasoning 保留为独立来源（不与 SFT 合并）。
	if plan.ReasoningRecords > 0 {
		ids, err := sampleIDs(ctx, tx, `SELECT id FROM reasoning_records WHERE dataset_id = $1 ORDER BY id`, plan.DatasetID, sampleLimit)
		if err != nil {
			return err
		}
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionReasoningSeparateSource, Severity: SeverityInfo,
			Reason:    "旧 reasoning_records 保留为**独立来源**，不与 SFT 一等表合并（合并会让同题两条不同来源的记录互相覆盖）",
			SampleIDs: ids,
		})
	}

	// 6. reward 分数不当作 GRPO 教师材料。
	if plan.RewardRecords > 0 {
		ids, err := sampleIDs(ctx, tx, `SELECT id FROM reward_records WHERE dataset_id = $1 ORDER BY id`, plan.DatasetID, sampleLimit)
		if err != nil {
			return err
		}
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionGRPONotTeacherMaterial, Severity: SeverityWarning,
			Reason: "旧 reward_records 的分数**不映射**为 GRPO 教师材料：奖励信号不是判分标准，" +
				"把它当判分标准会让 GRPO 数据失去档位语义",
			SampleIDs: ids,
		})
	}

	// 7. 工件：路径可能已被覆盖，只能验证现存字节。
	if plan.Artifacts > 0 {
		ids, err := sampleIDs(ctx, tx, `SELECT id FROM artifacts WHERE dataset_id = $1 ORDER BY id`, plan.DatasetID, sampleLimit)
		if err != nil {
			return err
		}
		plan.Decisions = append(plan.Decisions, Decision{
			Kind: DecisionArtifactUnverified, Severity: SeverityWarning,
			Reason: fmt.Sprintf("%d 份历史工件：固定对象路径可能已被覆盖，**只能验证现存字节**，"+
				"不能证明其历史 hash；保留为「历史工件/未验证」，不自动标为已发布", plan.Artifacts),
			SampleIDs: ids,
		})
		if plan.MissingArtifacts > 0 {
			plan.Decisions = append(plan.Decisions, Decision{
				Kind: DecisionMissingReference, Severity: SeverityBlocker,
				Reason: fmt.Sprintf("%d 份工件的 object_key 为空：无法定位对象，必须人工确认或标记为丢失",
					plan.MissingArtifacts),
			})
		}
	}
	return nil
}

// sampleIDs 取一小批示例 ID。
func sampleIDs(ctx context.Context, tx pgx.Tx, sql string, datasetID int64, limit int) ([]int64, error) {
	rows, err := tx.Query(ctx, sql+fmt.Sprintf(" LIMIT %d", limit), datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// buildNotes 汇总「本工具无法验证什么」，全部显式写出。
func buildNotes(report *Report) {
	report.Notes = append(report.Notes,
		"本次运行的数据库连接处于 READ ONLY 事务：任何写操作都会被 Postgres 拒绝（dry-run 由数据库保证，不是靠代码约定）。",
		"对象存储里的字节存在性**未验证**：本工具不连接 MinIO/S3（不持有凭证）。"+
			"历史工件的 hash 只能在其字节仍存在时重新计算；缺失/被覆盖的情况必须人工确认。",
		"「有生成证据」的判据是 generation_runs 有行，而**不是** datasets.status。"+
			"status 是流程状态，历史上出现过「标记完成但零产出」，用它当证据会得到错误的可复现性结论。",
		"本工具不调用任何模型、不改任何源数据：盘点结果只用于人工确认导入范围。",
	)
}

// WriteReport 把报告写到文件（JSON）。空路径表示不落盘。
func WriteReport(report Report, path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("写入报告失败：%w", err)
	}
	return nil
}

// Summary 返回人类可读摘要（CLI 打印）。
func (report Report) Summary() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "只读盘点：%d 张表，%d 个 dataset（dry-run=%v，只读事务=%v）\n",
		len(report.Tables), len(report.Datasets), report.DryRun, report.ReadOnly)

	kinds := make([]string, 0, len(report.Totals))
	for kind := range report.Totals {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	builder.WriteString("决策统计：\n")
	for _, kind := range kinds {
		fmt.Fprintf(&builder, "  %-32s %d\n", kind, report.Totals[kind])
	}
	builder.WriteString("按严重度：\n")
	bySeverity := map[string]int{}
	withDecisions := 0
	for _, plan := range report.Datasets {
		if len(plan.Decisions) > 0 {
			withDecisions++
		}
		for _, decision := range plan.Decisions {
			bySeverity[decision.Severity]++
		}
	}
	for _, severity := range []string{SeverityBlocker, SeverityWarning, SeverityInfo} {
		fmt.Fprintf(&builder, "  %-10s %d\n", severity, bySeverity[severity])
	}
	fmt.Fprintf(&builder, "有待处理决策的 dataset：%d/%d\n", withDecisions, len(report.Datasets))
	builder.WriteString("说明：\n")
	for _, note := range report.Notes {
		fmt.Fprintf(&builder, "  - %s\n", note)
	}
	return builder.String()
}

// quoteIdent 给标识符加双引号（标识符来自固定清单，这里只防手误）。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sortedKeys 把集合转成稳定排序的切片（报告的文本必须可复现）。
func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
