package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DirectionRow 是方向（level=2 domain）及其长链思维标准步骤。
//
// 定义在 store 包而非 llm 包：llm 包已 import store（用 CanonicalHash），
// 反向依赖会构成循环。worker 负责把本类型转换成 llm.DirectionContext。
type DirectionRow struct {
	DomainID   int64
	DomainName string
	ChainSteps []model.ChainStep
}

// QuestionStoreV2 是 questions 表的 v2 读写入口。
//
// 与 PipelineStore 的分工：PipelineStore 服务 legacy 的 domains 维度流水线；
// QuestionStoreV2 服务方向（level=2 domain）维度的问题生成，额外提供
// 去重写入、带方向名查询与难度分布统计。
type QuestionStoreV2 struct {
	db *pgxpool.Pool
}

// 问题生成阶段的共享常量。
//
// 放在 store 包而非 apps/api 或 apps/worker：两个进程是各自独立的
// package main，无法互相引用，而这些值必须两边完全一致。
const (
	// QuestionsStage 是问题生成在 generation_runs 中使用的阶段名。
	QuestionsStage = "questions"

	// QuestionsCursorMixKey 是难度配比在 generation_runs.cursor 中的键名。
	//
	// 为什么用 cursor 传递：worker 的 job payload 只有 {type, datasetId}
	// （见 apps/api/http_util.go 的 enqueueJob，属冻结契约），配比无法随任务
	// 传递。cursor 是既有基础设施中唯一能持久化「本阶段参数」且 worker 可读的位置。
	QuestionsCursorMixKey = "difficultyMix"

	// QuestionsCursorPerDirectionKey 是 x 在 cursor 中的键名（便于排查与断点续跑）。
	QuestionsCursorPerDirectionKey = "questionsPerDirection"

	// DefaultQuestionsPerDirection 是所有回退都落空时的兜底数量。
	DefaultQuestionsPerDirection = 5
)

func NewQuestionStoreV2(db *pgxpool.Pool) *QuestionStoreV2 {
	return &QuestionStoreV2{db: db}
}

// InsertQuestionsResult 汇总一次批量写入的去重结果。
type InsertQuestionsResult struct {
	Inserted int // 实际写入行数
	Skipped  int // 因内容重复被跳过的行数
}

// InsertQuestions 批量写入问题，按内容去重后插入。
//
// 去重是三层防线（缺一不可）：
//  1. 批内去重：同一批里 DedupeKey 相同的只保留第一条
//  2. 应用层先查：把批内 DedupeKey 与库中已有 DedupeKey 对比，已存在的直接跳过
//  3. DB 层兜底：UNIQUE(dataset_id, canonical_hash) 拦下并发的精确重复
//
// 第 2 层不能省：`idx_questions_dataset_dedupe` 是**非唯一**索引
// （只有 (dataset_id, canonical_hash) 是 UNIQUE），所以跨批次的近重复
// （同一问题不同标点/全角写法）只能靠应用层拦截。
//
// 被跳过的行数计入返回值，供调用方记录统计。
func (s *QuestionStoreV2) InsertQuestions(ctx context.Context, datasetID int64, questions []model.Question) (InsertQuestionsResult, error) {
	result := InsertQuestionsResult{}
	if len(questions) == 0 {
		return result, nil
	}

	// 第 1 层：批内去重。
	seen := map[string]struct{}{}
	deduped := make([]model.Question, 0, len(questions))
	for _, question := range questions {
		key := question.DedupeKey
		if key == "" {
			key = CanonicalHash(question.Content)
		}
		if _, exists := seen[key]; exists {
			result.Skipped++
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, question)
	}

	// 第 2 层：应用层先查库中已有的 DedupeKey。
	existing, err := s.ExistingDedupeKeys(ctx, datasetID, keysOf(deduped))
	if err != nil {
		return result, err
	}

	for _, question := range deduped {
		canonical := question.CanonicalHash
		if canonical == "" {
			canonical = CanonicalHash(question.Content)
		}
		dedupeKey := question.DedupeKey
		if dedupeKey == "" {
			dedupeKey = canonical
		}
		if _, exists := existing[dedupeKey]; exists {
			// 该内容（或其近重复变体）已在库中。
			result.Skipped++
			continue
		}
		difficulty := question.Difficulty
		if difficulty == "" {
			difficulty = "medium"
		}
		difficultyScore := question.DifficultyScore
		if difficultyScore <= 0 {
			difficultyScore = 2
		}
		source := question.Source
		if source == "" {
			source = "ai"
		}

		tag, err := s.db.Exec(ctx, `
      INSERT INTO questions (dataset_id, domain_id, direction_domain_id, content, canonical_hash,
                             dedupe_key, difficulty, difficulty_score, source, status)
      VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'generated')
      ON CONFLICT (dataset_id, canonical_hash) DO NOTHING`,
			datasetID,
			question.DomainID,
			question.DirectionDomainID,
			question.Content,
			canonical,
			dedupeKey,
			difficulty,
			difficultyScore,
			source,
		)
		if err != nil {
			return result, err
		}
		if tag.RowsAffected() == 0 {
			// 第 3 层命中：该 canonical_hash 在库中已存在（可能来自并发写入）。
			result.Skipped++
			continue
		}
		result.Inserted++
	}
	return result, nil
}

// keysOf 提取问题列表的 DedupeKey（空值时回退到 CanonicalHash）。
func keysOf(questions []model.Question) []string {
	keys := make([]string, 0, len(questions))
	for _, question := range questions {
		key := question.DedupeKey
		if key == "" {
			key = CanonicalHash(question.Content)
		}
		keys = append(keys, key)
	}
	return keys
}

// ExistingDedupeKeys 返回库中已存在的 DedupeKey 集合（只查传入的键，避免全表扫描）。
func (s *QuestionStoreV2) ExistingDedupeKeys(ctx context.Context, datasetID int64, keys []string) (map[string]struct{}, error) {
	existing := map[string]struct{}{}
	if len(keys) == 0 {
		return existing, nil
	}

	rows, err := s.db.Query(ctx, `
    SELECT dedupe_key FROM questions
    WHERE dataset_id = $1 AND dedupe_key = ANY($2)`, datasetID, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		existing[key] = struct{}{}
	}
	return existing, rows.Err()
}

// ListQuestions 查询数据集下全部问题，含方向名（level=2 domain 优先）。
func (s *QuestionStoreV2) ListQuestions(ctx context.Context, datasetID int64) ([]model.Question, error) {
	rows, err := s.db.Query(ctx, `
    SELECT q.id, q.dataset_id, q.domain_id, COALESCE(direction.name, domain.name, ''), q.direction_domain_id,
           q.content, q.canonical_hash, q.dedupe_key, q.difficulty, q.difficulty_score,
           q.source, q.cleaning_status, q.status, q.created_at, q.updated_at
    FROM questions q
    LEFT JOIN domains direction ON direction.id = q.direction_domain_id
    LEFT JOIN domains domain ON domain.id = q.domain_id
    WHERE q.dataset_id = $1
    ORDER BY q.direction_domain_id ASC, q.id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.Question{}
	for rows.Next() {
		var item model.Question
		if err := rows.Scan(&item.ID, &item.DatasetID, &item.DomainID, &item.DomainName, &item.DirectionDomainID,
			&item.Content, &item.CanonicalHash, &item.DedupeKey, &item.Difficulty, &item.DifficultyScore,
			&item.Source, &item.CleaningStatus, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DifficultyStats 统计数据集内问题的难度分布。
func (s *QuestionStoreV2) DifficultyStats(ctx context.Context, datasetID int64) (model.DifficultyStats, error) {
	rows, err := s.db.Query(ctx, `
    SELECT difficulty, COUNT(*) FROM questions
    WHERE dataset_id = $1
    GROUP BY difficulty`, datasetID)
	if err != nil {
		return model.DifficultyStats{}, err
	}
	defer rows.Close()

	stats := model.DifficultyStats{Levels: map[string]int{}}
	for rows.Next() {
		var level string
		var count int
		if err := rows.Scan(&level, &count); err != nil {
			return model.DifficultyStats{}, err
		}
		stats.Levels[level] = count
		stats.Total += count
	}
	if err := rows.Err(); err != nil {
		return model.DifficultyStats{}, err
	}
	// 三档恒定出现在响应里，前端无需处理缺档。
	for _, level := range []string{"easy", "medium", "hard"} {
		if _, exists := stats.Levels[level]; !exists {
			stats.Levels[level] = 0
		}
	}
	return stats, nil
}

// ListDirections 读取数据集下的方向（level=2 domain）及其长链标准步骤，
// 供问题生成构造 DirectionContext。
//
// 长链标准步骤来自 L2 的 chain_standards / chain_standard_versions：
// steps 存在版本表，需按 chain_standards.current_version 取当前版本。
// 该表由另一个 lane 负责写入，这里只读，缺失时返回空步骤
// （生成器会退化为不注入思考框架，不影响问题生成）。
func (s *QuestionStoreV2) ListDirections(ctx context.Context, datasetID int64) ([]DirectionRow, error) {
	rows, err := s.db.Query(ctx, `
    SELECT d.id, d.name, COALESCE(csv.steps, '[]'::jsonb)
    FROM domains d
    LEFT JOIN chain_standards cs
      ON cs.domain_id = d.id AND cs.dataset_id = d.dataset_id
    LEFT JOIN chain_standard_versions csv
      ON csv.standard_id = cs.id AND csv.version = cs.current_version
    WHERE d.dataset_id = $1 AND d.level = 2
    ORDER BY d.id ASC`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []DirectionRow{}
	for rows.Next() {
		var item DirectionRow
		var stepsPayload []byte
		if err := rows.Scan(&item.DomainID, &item.DomainName, &stepsPayload); err != nil {
			return nil, err
		}
		if len(stepsPayload) > 0 {
			if err := json.Unmarshal(stepsPayload, &item.ChainSteps); err != nil {
				return nil, fmt.Errorf("decode chain steps for domain %d: %w", item.DomainID, err)
			}
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// CountQuestions 返回数据集内的问题总数。
func (s *QuestionStoreV2) CountQuestions(ctx context.Context, datasetID int64) (int, error) {
	var count int
	err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM questions WHERE dataset_id = $1`, datasetID).Scan(&count)
	return count, err
}
