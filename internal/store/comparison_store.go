package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件实现同基准比较的读写与配对观测加载（Issue #160 T18）。
//
// 契约：docs/plans/atelier-implementation.md §3（P06）；
// internal/model/comparison.go（判据）；
// sql/migrations/0036_studio_comparison.sql（表结构取舍）。
//
// 本文件的唯一职责是**把两侧的评分按配对键对齐**：
// 比较的正确性完全取决于「同一题的两侧评分被放在一起」这件事，
// 而按错键（例如按样本 ID 而不是单元键）对齐会得到一组几乎不相交的集合，
// 于是配对完成数极低 —— 那至少是可发现的（报告会说不可比）。
// 真正危险的是「按错键但恰好有交集」：那时差异来自对齐错误而非方案。

// scoreKey 是一侧评分的键：**单元键 + 维度**。
//
// 定义成命名类型而不是内联匿名结构：后者在 map 类型里写不出合法语法
// （`map[struct{...}, float64]` 是错的），而且配对键本来就是本文件的核心概念，
// 值得有名字。
type scoreKey struct {
	itemKey   string
	dimension string
}

// ComparisonStore 提供比较基准与采用决定的读写。
type ComparisonStore struct {
	db *pgxpool.Pool
}

// NewComparisonStore 构造比较 store。
func NewComparisonStore(db *pgxpool.Pool) *ComparisonStore {
	return &ComparisonStore{db: db}
}

// CreateComparisonBaselineInput 是创建比较基准的请求。
type CreateComparisonBaselineInput struct {
	ProjectID     int64
	InputRef      string
	CoverageSlice map[string]any
	SamplingSeed  int64
	Rubric        model.RubricSpec
	Judges        []model.JudgeSpec
	Metric        string
	LeftBatchID   int64
	RightBatchID  int64
	Name          string
	CreatedBy     *int64
}

// CreateComparisonBaseline 冻结一份比较前提。
//
// 校验先于写入（`ValidateComparisonBaseline`），因此不可比的基准**根本进不了库**：
// 「先存下来再慢慢补输入」会让一份声明逐题配对却未固定输入的基准存在，
// 而它一旦被用来生成报告，差异就主要来自输入 —— 那正是 T18 要防的事。
func (s *ComparisonStore) CreateComparisonBaseline(ctx context.Context, input CreateComparisonBaselineInput) (model.ComparisonBaseline, error) {
	baseline := model.ComparisonBaseline{
		ProjectID: input.ProjectID, InputRef: strings.TrimSpace(input.InputRef),
		CoverageSlice: input.CoverageSlice, SamplingSeed: input.SamplingSeed,
		Rubric: input.Rubric, Judges: input.Judges, Metric: input.Metric,
		Name: strings.TrimSpace(input.Name), CreatedBy: input.CreatedBy,
	}
	left, right := input.LeftBatchID, input.RightBatchID
	baseline.LeftBatchID, baseline.RightBatchID = &left, &right
	if len(baseline.CoverageSlice) == 0 {
		baseline.CoverageSlice = map[string]any{}
	}
	// 两侧批次必须属于本项目（跨项目比较没有意义，且会泄漏别人的批次）。
	for _, batchID := range []int64{left, right} {
		var projectID int64
		if err := s.db.QueryRow(ctx, `
      SELECT project_id FROM batches WHERE id = $1`, batchID).Scan(&projectID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return model.ComparisonBaseline{}, &apiStoreError{Message: fmt.Sprintf(
					"批次 %d 不存在，无法作为比较的一侧", batchID)}
			}
			return model.ComparisonBaseline{}, err
		}
		if projectID != input.ProjectID {
			return model.ComparisonBaseline{}, &apiStoreError{Message: fmt.Sprintf(
				"批次 %d 不属于本项目，不能参与比较", batchID)}
		}
	}

	// `inputRef` 不能由客户端拿两个批次 ID 拼出来：那只能证明「选了哪两批」，
	// 不能证明两侧实际使用了同一份输入。逐题配对时由服务端读取两侧批次已经
	// 产出的样本键与题面，计算不可伪造的输入指纹；客户端传入的 inputRef
	// 仅作为旧客户端兼容字段，永远不会成为判定依据。
	if baseline.Metric == model.ComparisonMetricPaired {
		inputRef, err := s.resolvePairedInputRef(ctx, left, right)
		if err != nil {
			return model.ComparisonBaseline{}, err
		}
		baseline.InputRef = inputRef
	}
	if err := model.ValidateComparisonBaseline(baseline); err != nil {
		return model.ComparisonBaseline{}, err
	}

	rubricJSON, err := json.Marshal(baseline.Rubric)
	if err != nil {
		return model.ComparisonBaseline{}, err
	}
	judgesJSON, err := json.Marshal(baseline.Judges)
	if err != nil {
		return model.ComparisonBaseline{}, err
	}
	sliceJSON, err := json.Marshal(baseline.CoverageSlice)
	if err != nil {
		return model.ComparisonBaseline{}, err
	}

	var created model.ComparisonBaseline
	var rawSlice, rawRubric, rawJudges []byte
	if err := s.db.QueryRow(ctx, `
    INSERT INTO comparison_baselines
      (project_id, input_ref, coverage_slice, sampling_seed, rubric, judges, metric,
       left_batch_id, right_batch_id, name, created_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
    RETURNING id, project_id, input_ref, coverage_slice, sampling_seed, rubric, judges, metric,
              left_batch_id, right_batch_id, name, created_by, created_at`,
		input.ProjectID, baseline.InputRef, sliceJSON, baseline.SamplingSeed,
		rubricJSON, judgesJSON, baseline.Metric, left, right, baseline.Name, input.CreatedBy,
	).Scan(&created.ID, &created.ProjectID, &created.InputRef, &rawSlice, &created.SamplingSeed,
		&rawRubric, &rawJudges, &created.Metric, &created.LeftBatchID, &created.RightBatchID,
		&created.Name, &created.CreatedBy, &created.CreatedAt); err != nil {
		return model.ComparisonBaseline{}, err
	}
	_ = json.Unmarshal(rawSlice, &created.CoverageSlice)
	_ = json.Unmarshal(rawRubric, &created.Rubric)
	_ = json.Unmarshal(rawJudges, &created.Judges)
	return created, nil
}

// resolvePairedInputRef 返回两侧实际样本输入的服务端指纹。
//
// 每一侧取每个稳定 sample_key 最新版本中的 question 字段；键和题面都纳入
// 摘要，因此「同一批次 ID 格式」或客户端伪造字符串无法把不同输入标成可比。
// 没有已产出的输入时拒绝建立 paired 基准：空集合相等并不等于固定了输入。
func (s *ComparisonStore) resolvePairedInputRef(ctx context.Context, leftBatchID, rightBatchID int64) (string, error) {
	load := func(batchID int64) ([]string, error) {
		rows, err := s.db.Query(ctx, `
      SELECT DISTINCT ON (sm.sample_key)
             sm.sample_key, COALESCE(sv.payload->>'question', '')
      FROM sample_versions sv
      JOIN samples sm ON sm.id = sv.sample_id
      WHERE sv.batch_id = $1
      ORDER BY sm.sample_key, sv.created_at DESC, sv.id DESC`, batchID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		inputs := []string{}
		for rows.Next() {
			var key, question string
			if err := rows.Scan(&key, &question); err != nil {
				return nil, err
			}
			// Length-prefix each field so concatenation cannot create ambiguous
			// identities (e.g. ["ab", "c"] vs ["a", "bc"]).
			inputs = append(inputs, fmt.Sprintf("%d:%s:%d:%s", len(key), key, len(question), question))
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return inputs, nil
	}

	left, err := load(leftBatchID)
	if err != nil {
		return "", err
	}
	right, err := load(rightBatchID)
	if err != nil {
		return "", err
	}
	if len(left) == 0 || len(right) == 0 || len(left) != len(right) {
		return "", model.FieldErrors{{Field: "inputRef",
			Message: "两侧批次没有相同且完整的输入题集，无法进行逐题配对；请使用同一输入版本重新试制"}}
	}
	for index := range left {
		if left[index] != right[index] {
			return "", model.FieldErrors{{Field: "inputRef",
				Message: "两侧批次的实际输入题集不一致，无法进行逐题配对；请使用同一输入版本重新试制"}}
		}
	}
	hash := sha256.Sum256([]byte(strings.Join(left, "\n")))
	return "sample-input-v1:" + hex.EncodeToString(hash[:]), nil
}

// GetComparisonBaseline 读取一份比较基准（项目作用域）。
func (s *ComparisonStore) GetComparisonBaseline(ctx context.Context, projectID, baselineID int64) (model.ComparisonBaseline, error) {
	var baseline model.ComparisonBaseline
	var rawSlice, rawRubric, rawJudges []byte
	err := s.db.QueryRow(ctx, `
    SELECT id, project_id, input_ref, coverage_slice, sampling_seed, rubric, judges, metric,
           left_batch_id, right_batch_id, name, created_by, created_at
    FROM comparison_baselines WHERE id = $1 AND project_id = $2`, baselineID, projectID,
	).Scan(&baseline.ID, &baseline.ProjectID, &baseline.InputRef, &rawSlice, &baseline.SamplingSeed,
		&rawRubric, &rawJudges, &baseline.Metric, &baseline.LeftBatchID, &baseline.RightBatchID,
		&baseline.Name, &baseline.CreatedBy, &baseline.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.ComparisonBaseline{}, ErrComparisonBaselineNotFound
	}
	if err != nil {
		return model.ComparisonBaseline{}, err
	}
	_ = json.Unmarshal(rawSlice, &baseline.CoverageSlice)
	_ = json.Unmarshal(rawRubric, &baseline.Rubric)
	_ = json.Unmarshal(rawJudges, &baseline.Judges)
	return baseline, nil
}

// ErrComparisonBaselineNotFound 表示比较基准不存在或不属于本项目。
var ErrComparisonBaselineNotFound = errors.New("比较基准不存在或不属于本项目")

// ListComparisonBaselines 列出项目的比较基准。
func (s *ComparisonStore) ListComparisonBaselines(ctx context.Context, projectID int64, limit int) ([]model.ComparisonBaseline, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
    SELECT id FROM comparison_baselines WHERE project_id = $1
    ORDER BY created_at DESC, id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	items := make([]model.ComparisonBaseline, 0, len(ids))
	for _, id := range ids {
		baseline, err := s.GetComparisonBaseline(ctx, projectID, id)
		if err != nil {
			return nil, err
		}
		items = append(items, baseline)
	}
	return items, nil
}

// LoadPairedObservations 把两侧评分按**单元键 + 维度**对齐。
//
// 配对键用 `sample_versions.sample_key`（T12 的覆盖分配把它写成
// `domain/direction#ordinal`）：两侧基于同一份覆盖时键空间相同，
// 因此「同一题在两方案下」这件事有确定的对齐依据。
//
// 评分取**当前有效**的 experiment_scores（排除被取代的），
// 且只取 `score_state='scored'` 的格 —— 缺分与出错不是分数（§2.6），
// 把它们算成 0 会凭空制造差异。
func (s *ComparisonStore) LoadPairedObservations(ctx context.Context, baseline model.ComparisonBaseline) ([]model.PairedObservation, error) {
	if baseline.LeftBatchID == nil || baseline.RightBatchID == nil {
		return nil, &apiStoreError{Message: "比较基准缺少两侧批次"}
	}

	left, err := s.loadSideScores(ctx, baseline.ProjectID, *baseline.LeftBatchID, baseline.Rubric)
	if err != nil {
		return nil, err
	}
	right, err := s.loadSideScores(ctx, baseline.ProjectID, *baseline.RightBatchID, baseline.Rubric)
	if err != nil {
		return nil, err
	}

	// 合并：以两侧键的并集为范围，因此「只在一侧有」的观测会**显式出现**
	// （分数为 nil），从而被报告计为 LeftOnly/RightOnly 而不是静默消失。
	all := map[scoreKey]bool{}
	for key := range left {
		all[key] = true
	}
	for key := range right {
		all[key] = true
	}

	observations := make([]model.PairedObservation, 0, len(all))
	for key := range all {
		observation := model.PairedObservation{
			PairKey: key.itemKey, Dimension: key.dimension,
		}
		if value, found := left[key]; found {
			observation.LeftScore = &value
		}
		if value, found := right[key]; found {
			observation.RightScore = &value
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

// loadSideScores 加载一侧的 (单元键, 维度) → 归一化均值。
//
// 归一化在这里做（而不是在 SQL 里）：范围来自**比较基准冻结的量表**，
// 而 SQL 里要拼 JSON 数组查询会引入动态 SQL；用 Go 侧的
// `model.NormalizedScore` 还能复用同一份「范围非法时不可归一化」的判定。
func (s *ComparisonStore) loadSideScores(ctx context.Context, projectID, batchID int64, rubric model.RubricSpec) (map[scoreKey]float64, error) {
	rangeByDimension := map[string]model.RubricDimension{}
	for _, dimension := range rubric.Dimensions {
		rangeByDimension[dimension.Key] = dimension
	}

	// 配对键取自 **samples.sample_key**（不是 sample_versions 上的列）：
	// 单元键标识「哪一题」，而 sample_versions 只承载内容版本。
	// 写成 sv.sample_key 会直接报列不存在 —— 这是测试当场发现的，
	// 也说明这类「看起来同名的字段」必须由真实查询验证。
	rows, err := s.db.Query(ctx, `
    SELECT sm.sample_key, es.dimension, AVG(es.raw_score)
    FROM sample_versions sv
    JOIN samples sm ON sm.id = sv.sample_id
    JOIN experiment_items ei ON ei.sample_version_id = sv.id
    JOIN experiments e ON e.id = ei.experiment_id AND e.batch_id = $2
    JOIN experiment_scores es ON es.experiment_item_id = ei.id
    WHERE sv.project_id = $1 AND sv.batch_id = $2
      AND es.superseded_by IS NULL
      AND es.score_state = 'scored'
      AND es.raw_score IS NOT NULL
    GROUP BY sm.sample_key, es.dimension`,
		projectID, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scores := map[scoreKey]float64{}
	for rows.Next() {
		var itemKey, dimension string
		var mean float64
		if err := rows.Scan(&itemKey, &dimension, &mean); err != nil {
			return nil, err
		}
		dimensionRange, found := rangeByDimension[dimension]
		if !found {
			// 评分引用了基准量表里没有的维度：跳过而不是按某个范围粗略归一化
			// （粗略归一化会给出一个看起来正常但含义不明的分数）。
			continue
		}
		normalized, ok := model.NormalizedScore(mean, dimensionRange)
		if !ok {
			// 范围非法（上界 ≤ 下界）：该维度无法归一化，跳过并计入缺失。
			continue
		}
		scores[scoreKey{itemKey: itemKey, dimension: dimension}] = normalized
	}
	return scores, rows.Err()
}

// ComparisonCosts 读取两侧成本（实际与未知分列）。
//
// 未知不得并入实际、也不得记 0：超时或断连的调用可能已产生费用，
// 而把它记成 0 会让成本对比看起来「右侧更便宜」—— 那是会直接导致
// 错误选型的失真（与 §2.4 的四态一致）。
func (s *ComparisonStore) ComparisonCosts(ctx context.Context, baseline model.ComparisonBaseline) (model.ComparisonCosts, error) {
	costs := model.ComparisonCosts{Currency: "CNY"}
	read := func(batchID *int64) (actual, uncertain int64, currency string, err error) {
		if batchID == nil {
			return 0, 0, "CNY", nil
		}
		err = s.db.QueryRow(ctx, `
      SELECT budget_settled_minor, budget_uncertain_minor, budget_currency
      FROM batches WHERE id = $1`, *batchID).Scan(&actual, &uncertain, &currency)
		return actual, uncertain, currency, err
	}
	leftActual, leftUncertain, currency, err := read(baseline.LeftBatchID)
	if err != nil {
		return model.ComparisonCosts{}, err
	}
	rightActual, rightUncertain, _, err := read(baseline.RightBatchID)
	if err != nil {
		return model.ComparisonCosts{}, err
	}
	costs.LeftActualMinor, costs.LeftUncertainMinor = leftActual, leftUncertain
	costs.RightActualMinor, costs.RightUncertainMinor = rightActual, rightUncertain
	if currency != "" {
		costs.Currency = currency
	}
	return costs, nil
}

// BuildComparisonReport 组装完整报告（基准 + 配对观测 + 成本）。
func (s *ComparisonStore) BuildComparisonReport(ctx context.Context, projectID, baselineID int64) (model.ComparisonReport, error) {
	baseline, err := s.GetComparisonBaseline(ctx, projectID, baselineID)
	if err != nil {
		return model.ComparisonReport{}, err
	}
	observations, err := s.LoadPairedObservations(ctx, baseline)
	if err != nil {
		return model.ComparisonReport{}, err
	}
	report := model.BuildComparisonReport(baseline, observations)
	costs, err := s.ComparisonCosts(ctx, baseline)
	if err != nil {
		return model.ComparisonReport{}, err
	}
	report.Costs = costs
	// 成本读出来之后重算风险：未知费用必须出现在结论旁边，
	// 而在 BuildComparisonReport 里读不到成本（那是纯函数）。
	report.Risks = append(report.Risks, model.ComparisonCostRisks(costs)...)
	return report, nil
}

// AdoptComparisonInput 是一次采用决定。
type AdoptComparisonInput struct {
	ProjectID  int64
	BaselineID int64
	Side       string
	Reason     string
	CreatedBy  *int64
}

// Adopt 记录采用决定并更新项目采用指针。
//
// 两件事在同一事务里：**只追加的依据** + **一行的当前指针**。
// 采用**不自动运行或发布**（T18 验收项）：它只更新指针并把版本预填到
// `/runs/new`，是否扩量仍由用户显式发起。
func (s *ComparisonStore) Adopt(ctx context.Context, input AdoptComparisonInput) (model.ComparisonBaseline, int64, error) {
	report, err := s.BuildComparisonReport(ctx, input.ProjectID, input.BaselineID)
	if err != nil {
		return model.ComparisonBaseline{}, 0, err
	}
	if err := model.ValidateAdoption(input.Side, input.Reason, report); err != nil {
		return model.ComparisonBaseline{}, 0, err
	}

	baseline, err := s.GetComparisonBaseline(ctx, input.ProjectID, input.BaselineID)
	if err != nil {
		return model.ComparisonBaseline{}, 0, err
	}
	adoptedBatchID := baseline.LeftBatchID
	if input.Side == "right" {
		adoptedBatchID = baseline.RightBatchID
	}
	if adoptedBatchID == nil {
		return model.ComparisonBaseline{}, 0, &apiStoreError{Message: "该侧没有批次，无法采用"}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return model.ComparisonBaseline{}, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 依据快照：把当时的报告数值冻结进 adoptions，避免「以后改了基准
	// 却让旧采用决定的依据跟着变」。
	evidence, err := json.Marshal(map[string]any{
		"pairedCount": report.PairedCount,
		"metric":      report.Metric,
		"label":       report.Comparability.Label,
		"dimensions":  report.Dimensions,
		"costs":       report.Costs,
	})
	if err != nil {
		return model.ComparisonBaseline{}, 0, err
	}

	var adoptionID int64
	if err := tx.QueryRow(ctx, `
    INSERT INTO comparison_adoptions
      (project_id, baseline_id, adopted_side, adopted_batch_id, reason, evidence, created_by)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    RETURNING id`,
		input.ProjectID, baseline.ID, input.Side, *adoptedBatchID,
		strings.TrimSpace(input.Reason), evidence, input.CreatedBy,
	).Scan(&adoptionID); err != nil {
		return model.ComparisonBaseline{}, 0, err
	}

	// 当前指针：唯一可更新的地方（历史由 adoptions 保留）。
	if _, err := tx.Exec(ctx, `
    INSERT INTO project_adopted_batches
      (project_id, batch_id, baseline_id, adopted_side, adoption_id, updated_at)
    VALUES ($1, $2, $3, $4, $5, NOW())
    ON CONFLICT (project_id) DO UPDATE
    SET batch_id = EXCLUDED.batch_id, baseline_id = EXCLUDED.baseline_id,
        adopted_side = EXCLUDED.adopted_side, adoption_id = EXCLUDED.adoption_id,
        updated_at = NOW()`,
		input.ProjectID, *adoptedBatchID, baseline.ID, input.Side, adoptionID); err != nil {
		return model.ComparisonBaseline{}, 0, err
	}

	if err := writeStudioAuditTx(ctx, tx, StudioAudit{
		ActorID: derefInt64(input.CreatedBy), Action: "comparison_adopt",
		Resource: "comparison_baseline", ResourceID: fmt.Sprint(baseline.ID),
		ProjectID: input.ProjectID,
		Reason:    fmt.Sprintf("side=%s batch=%d paired=%d", input.Side, *adoptedBatchID, report.PairedCount),
	}); err != nil {
		return model.ComparisonBaseline{}, 0, err
	}

	if err := tx.Commit(ctx); err != nil {
		return model.ComparisonBaseline{}, 0, err
	}
	return baseline, *adoptedBatchID, nil
}

// AdoptedBatch 返回项目当前采用的批次（无则返回 false）。
//
// 供 `/runs/new` 预填版本：采用只更新指针，扩量仍由用户显式发起。
func (s *ComparisonStore) AdoptedBatch(ctx context.Context, projectID int64) (int64, int64, string, bool, error) {
	var batchID, baselineID, adoptionID int64
	var side string
	err := s.db.QueryRow(ctx, `
    SELECT batch_id, baseline_id, adopted_side, adoption_id
    FROM project_adopted_batches WHERE project_id = $1`, projectID).
		Scan(&batchID, &baselineID, &side, &adoptionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, "", false, nil
	}
	if err != nil {
		return 0, 0, "", false, err
	}
	return batchID, baselineID, side, true, nil
}

// ListAdoptions 列出采用历史（只追加，供审计与「依据是什么」）。
func (s *ComparisonStore) ListAdoptions(ctx context.Context, projectID int64, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.Query(ctx, `
    SELECT id, baseline_id, adopted_side, adopted_batch_id, reason, created_at
    FROM comparison_adoptions WHERE project_id = $1
    ORDER BY created_at DESC, id DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []map[string]any{}
	for rows.Next() {
		var id, baselineID, batchID int64
		var side, reason string
		var createdAt time.Time
		if err := rows.Scan(&id, &baselineID, &side, &batchID, &reason, &createdAt); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"id": id, "baselineId": baselineID, "adoptedSide": side,
			"adoptedBatchId": batchID, "reason": reason, "createdAt": createdAt,
		})
	}
	return items, rows.Err()
}
