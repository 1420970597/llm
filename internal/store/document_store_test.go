package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/1420970597/llm/internal/model"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 本文件验证 Issue #160 T04 的版本化文档不变量。
//
// 为什么必须连真实 Postgres：核心断言全部关于**数据库约束与并发**——
//   - 并发保存恰有一个成功（靠 UNIQUE (document_id, version)，不是应用层判断）；
//   - 跨项目引用被复合外键拒绝；
//   - 旧版本只读且历史保留；
//   - expectedRevision 不匹配时不写任何行（含不写版本、不写审计）。
// 纯内存测试无法证明任何一条。
//
// 未设置 LLM_TEST_POSTGRES_DSN 时跳过（T32 会检查这些测试不是 Skip）。

// documentFixture 是一个项目 + 两个编辑者。
type documentFixture struct {
	pool      *pgxpool.Pool
	projectID int64
	editorA   int64
	editorB   int64
	documents *DocumentStore
	projects  *ProjectStore
	workspace int64
}

func newDocumentFixture(t *testing.T) documentFixture {
	t.Helper()
	pool := newStudioTestPool(t)
	ctx := context.Background()

	suffix := fmt.Sprintf("%d-%s", os.Getpid(), t.Name())
	workspaceID := seedAuthzWorkspace(t, pool, suffix)
	editorA := seedStudioUser(t, pool, "editor-a-"+suffix)
	editorB := seedStudioUser(t, pool, "editor-b-"+suffix)

	projects := NewProjectStore(pool)
	project, err := projects.CreateProject(ctx, workspaceID, editorA, validProjectInput("版本化文档项目"))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}

	return documentFixture{
		pool: pool, projectID: project.ID,
		editorA: editorA, editorB: editorB,
		documents: NewDocumentStore(pool), projects: projects, workspace: workspaceID,
	}
}

// standardPayload 返回一份通过校验的标准文档（供引用与被引用测试使用）。
func standardPayload(title string) model.StandardPayload {
	return model.StandardPayload{
		SchemaVersion: model.SchemaVersionFor(model.KindStandard),
		Steps: []model.StandardStep{
			{ID: "s1", Title: title, Detail: "先识别约束", Checkpoint: "列出至少两条约束", Order: 0},
			{ID: "s2", Title: "推理", Detail: "逐步推导", Checkpoint: "每步可追溯", Order: 1},
		},
	}
}

// coveragePayload 返回一份通过校验的覆盖文档。
func coveragePayload() model.CoveragePayload {
	ratios := []model.DifficultyRatio{
		{Difficulty: "easy", Ratio: 30},
		{Difficulty: "medium", Ratio: 50},
		{Difficulty: "hard", Ratio: 20},
	}
	return model.CoveragePayload{
		SchemaVersion: model.SchemaVersionFor(model.KindCoverage),
		Domains: []model.CoverageDomain{{
			StableID: "cold-chain",
			Name:     "冷链",
			Directions: []model.CoverageDirection{{
				StableID: "cold-chain.temperature", Name: "温控异常", Quota: 12,
				DifficultyRatios: ratios, Source: "manual",
			}},
		}},
	}
}

// blueprintPayload 返回一份引用给定覆盖/标准版本行的蓝图（version 行为 0 时留空）。
func blueprintPayload(coverageVersionID, standardVersionID int64) model.BlueprintPayload {
	payload := model.BlueprintPayload{SchemaVersion: model.SchemaVersionFor(model.KindBlueprint)}
	payload.Nodes.Coverage.CoverageVersionID = coverageVersionID
	payload.Nodes.Standard.StandardVersionID = standardVersionID
	payload.Nodes.Generation.SchemaVersion = model.SampleSchemaSFT
	payload.Nodes.Generation.Concurrency = 8
	payload.Nodes.Generation.MaxTokens = 4096
	return payload
}

// TestDocumentVersioningAppendsAndKeepsHistory 覆盖 §2.2 的核心：
// 保存产生新版本、旧版本仍可读、头记录指向最新版。
func TestDocumentVersioningAppendsAndKeepsHistory(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	document, first, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "初版标准", Payload: standardPayload("识别约束")})
	if err != nil {
		t.Fatalf("save first version: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("首个版本号必须是 1，实际 %d", first.Version)
	}
	if document.CurrentVersion != 1 || document.RowVersion != 2 {
		t.Fatalf("保存后头记录必须指向 v1 且 row_version 递增到 2，实际 current=%d row=%d",
			document.CurrentVersion, document.RowVersion)
	}

	_, second, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{
			ExpectedRevision: document.RowVersion,
			ChangeReason:     "补充边界检查点",
			Payload:          standardPayload("识别约束并列出例外"),
		})
	if err != nil {
		t.Fatalf("save second version: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("第二个版本号必须是 2，实际 %d", second.Version)
	}
	if second.ContentHash == first.ContentHash {
		t.Fatal("两次不同内容的版本必须有不同的 content hash")
	}

	// 旧版本必须仍可读（§2.2 验收项「旧版本可读/比较/复制」）。
	reloaded, err := fixture.documents.GetVersion(ctx, document.ID, 1)
	if err != nil {
		t.Fatalf("GetVersion(v1): %v", err)
	}
	if reloaded.ContentHash != first.ContentHash {
		t.Fatalf("旧版本内容不得被覆盖：hash %s vs %s", reloaded.ContentHash, first.ContentHash)
	}
	if reloaded.ChangeReason != "初版标准" {
		t.Fatalf("旧版本的理由必须保留，实际 %q", reloaded.ChangeReason)
	}

	versions, err := fixture.documents.ListVersions(ctx, fixture.projectID, model.KindStandard, "main", 10)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("历史必须保留两个版本，实际 %d 个", len(versions))
	}
}

// TestDocumentExpectedRevisionConflictWritesNothing 覆盖 T04 验收项：
// 两个编辑者并发保存一成功一冲突，且**失败方不写任何行**。
//
// 「不写任何行」包括不写版本行与审计行：留下一条孤儿审计会让
// 「审计记录了这次变更」与「内容并未改变」互相矛盾。
func TestDocumentExpectedRevisionConflictWritesNothing(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	document, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "初版", Payload: standardPayload("识别约束")})
	if err != nil {
		t.Fatalf("save base: %v", err)
	}
	baseRevision := document.RowVersion

	if _, err := fixture.pool.Exec(ctx, `DELETE FROM audit_logs WHERE project_id = $1`, fixture.projectID); err != nil {
		t.Fatalf("clear audit: %v", err)
	}

	// 编辑者 A 先用 baseRevision 保存成功。
	if _, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{
			ExpectedRevision: baseRevision,
			ChangeReason:     "A 的修改",
			Payload:          standardPayload("A 版本"),
		}); err != nil {
		t.Fatalf("A 的保存必须成功: %v", err)
	}

	// 编辑者 B 仍用同一个（已过期的）baseRevision 保存 → 必须冲突。
	_, _, err = fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorB,
		SaveDocumentVersionInput{
			ExpectedRevision: baseRevision,
			ChangeReason:     "B 的修改",
			Payload:          standardPayload("B 版本"),
		})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("过期的 expectedRevision 必须返回 ErrRevisionConflict，实际: %v", err)
	}

	// 冲突方不得留下版本行。
	var versionCount int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM document_versions v
    JOIN versioned_documents d ON d.id = v.document_id
    WHERE d.project_id = $1 AND d.kind = 'standard'`, fixture.projectID).Scan(&versionCount); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if versionCount != 2 {
		t.Fatalf("冲突的保存不得写入版本行：期望 2（初版 + A），实际 %d", versionCount)
	}

	// 冲突方不得留下审计。
	entries, err := NewAuthzStore(fixture.pool).ListProjectAudit(ctx, fixture.projectID, time.Time{}, 0, 50)
	if err != nil {
		t.Fatalf("ListProjectAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("只有成功的保存才应留下审计：期望 1 条，实际 %d 条", len(entries))
	}
	if entries[0].ActorUserID == nil || *entries[0].ActorUserID != fixture.editorA {
		t.Fatalf("审计的 actor 必须是成功的编辑者 A，实际 %+v", entries[0].ActorUserID)
	}
}

// TestDocumentConcurrentSaveResolvesToRevisionConflict 是**确定性**的并发守卫。
//
// 为什么需要它（而不是只靠真实并发测试）：真实并发的胜负依赖调度时序，
// 实测在 16 goroutine 下旧实现经常恰好不撞上而让测试偶发通过 ——
// 那是「没被触发的竞态」，不是被守住的缺陷。这里用未提交事务把
// 「两个保存之间的窗口」变成必然事件。
//
// 手法（沿用真实写入路径，不用旁路 SQL 造数据）：
//
//	A: BEGIN; 锁住文档头（SELECT ... FOR UPDATE）并推进 row_version
//	B: SaveVersion(expectedRevision=<旧值>)   ← 阻塞在头记录的行锁上
//	A: COMMIT                                  ← B 重新读到**新** row_version
//
// 机制说明：SaveVersion 第一步就对文档头 `SELECT ... FOR UPDATE`，
// 因此同一个逻辑文档的保存是**串行**的 —— B 在 A 提交后重新读到
// row_version 已经递增，于是乐观锁检查失败。这就是「并发保存恰有一个成功」
// 的确定性来源；数据库的 UNIQUE (document_id, version) 是它的兜底。
//
// 断言重点不是「B 失败」，而是 **B 失败的方式**：必须是 ErrRevisionConflict
// （handler 据此回 409 +「请重新加载」），而不是原始的 23505 驱动错误
// （那会被当成 500，用户看到「服务暂时不可用」而不知道该刷新重试）。
func TestDocumentConcurrentSaveResolvesToRevisionConflict(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	document, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "初版", Payload: standardPayload("识别约束")})
	if err != nil {
		t.Fatalf("save base: %v", err)
	}
	staleRevision := document.RowVersion

	// 连接 A：锁住文档头并推进 row_version，先不提交。
	blocker, err := fixture.pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire blocker: %v", err)
	}
	defer blocker.Release()
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker tx: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var lockedID int64
	if err := tx.QueryRow(ctx, `
    SELECT id FROM versioned_documents WHERE id = $1 FOR UPDATE`,
		document.ID).Scan(&lockedID); err != nil {
		t.Fatalf("blocker lock head: %v", err)
	}
	if _, err := tx.Exec(ctx, `
    UPDATE versioned_documents SET row_version = row_version + 1 WHERE id = $1`,
		document.ID); err != nil {
		t.Fatalf("blocker bump row_version: %v", err)
	}

	// 连接 B：用**过期**的 revision 保存。它会阻塞在头记录的行锁上。
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorB,
			SaveDocumentVersionInput{
				ExpectedRevision: staleRevision,
				ChangeReason:     "B 的修改",
				Payload:          standardPayload("B 版本"),
			})
		done <- outcome{err: err}
	}()

	// 给 B 足够时间走到「正阻塞在头记录行锁」的位置，再让 A 提交。
	select {
	case result := <-done:
		t.Fatalf("B 不应在 A 提交前完成（说明行锁屏障未生效）：err=%v", result.err)
	case <-time.After(400 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit blocker tx: %v", err)
	}

	var result outcome
	select {
	case result = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("B 在 A 提交后仍未返回（可能死锁）")
	}
	if !errors.Is(result.err, ErrRevisionConflict) {
		t.Fatalf("并发保存必须收敛为「版本已变化」（ErrRevisionConflict），实际: %v", result.err)
	}

	// B 不得留下任何版本行：冲突的保存必须是「什么都没发生」。
	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM document_versions WHERE document_id = $1`, document.ID).Scan(&count); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if count != 1 {
		t.Fatalf("冲突的保存不得写入版本行：期望 1（仅初版），实际 %d", count)
	}
}

// TestDocumentConcurrentSavesExactlyOneSucceeds 是同一条不变量在**真实并发**下的验证。
//
// 与上面的屏障测试互补：屏障测试证明「冲突被解析成正确的错误类型」，
// 这里证明「16 个 goroutine 同时保存时恰好一个成功」—— 也就是没有丢更新。
func TestDocumentConcurrentSavesExactlyOneSucceeds(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	document, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "初版", Payload: standardPayload("识别约束")})
	if err != nil {
		t.Fatalf("save base: %v", err)
	}

	const goroutines = 16
	type outcome struct {
		index   int
		version int
		err     error
	}
	results := make(chan outcome, goroutines)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, version, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
				SaveDocumentVersionInput{
					ExpectedRevision: document.RowVersion,
					ChangeReason:     fmt.Sprintf("并发编辑 %d", idx),
					Payload:          standardPayload(fmt.Sprintf("版本 %d", idx)),
				})
			results <- outcome{index: idx, version: version.Version, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	succeeded := 0
	for result := range results {
		switch {
		case result.err == nil:
			succeeded++
			if result.version != 2 {
				t.Fatalf("成功的保存必须是版本 2，实际 %d", result.version)
			}
		case errors.Is(result.err, ErrRevisionConflict):
			// 期望路径：乐观锁拦下。
		default:
			t.Fatalf("goroutine %d 的失败原因必须是 ErrRevisionConflict，实际: %v", result.index, result.err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("16 个并发保存必须恰好一个成功（否则是丢更新），实际成功 %d 个", succeeded)
	}

	versions, err := fixture.documents.ListVersions(ctx, fixture.projectID, model.KindStandard, "main", 100)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("并发后必须恰好有 2 个版本，实际 %d 个", len(versions))
	}
}

// TestDocumentReferencesMustBeSameProject 覆盖 §4.1 验收项「跨项目引用被拒绝」。
//
// 这是 T04 最重要的数据完整性断言之一：蓝图的映射/标准节点若能引用
// **别的项目**的版本，就会在发布时把别的项目的内容打进本次交付 ——
// 而那正是「串项目」这一类最严重的数据错误。
//
// 手法：建第二个项目与它的标准版本，然后让第一个项目的蓝图引用它。
// 复合外键（迁移 0024）必须在数据库层拒绝。
func TestDocumentReferencesMustBeSameProject(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	// 本项目自己的标准版本 —— 引用它必须成功。
	_, ownStandard, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "本项目标准", Payload: standardPayload("本项目")})
	if err != nil {
		t.Fatalf("save own standard: %v", err)
	}
	if _, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindBlueprint, fixture.editorA,
		SaveDocumentVersionInput{
			ChangeReason: "引用本项目标准",
			Payload:      blueprintPayload(0, ownStandard.ID),
		}); err != nil {
		t.Fatalf("同项目引用必须成功: %v", err)
	}

	// 另一个项目的标准版本。
	otherProject, err := fixture.projects.CreateProject(ctx, fixture.workspace, fixture.editorA, validProjectInput("另一个项目"))
	if err != nil {
		t.Fatalf("create other project: %v", err)
	}
	_, foreignStandard, err := fixture.documents.SaveVersion(ctx, otherProject.ID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "别的项目标准", Payload: standardPayload("别的项目")})
	if err != nil {
		t.Fatalf("save foreign standard: %v", err)
	}

	// 跨项目引用必须被拒绝，且必须是**字段级错误**（handler 据此返回 422 而不是 500）。
	//
	// 这里必须带上**当前** revision：上一次蓝图保存已经推进了 row_version，
	// 沿用旧值会先撞上乐观锁，于是测到的是 ErrRevisionConflict ——
	// 那会让这条测试在「引用检查被删掉」时依然通过（假绿）。
	currentBlueprint, err := fixture.documents.GetDocument(ctx, fixture.projectID, model.KindBlueprint, "")
	if err != nil {
		t.Fatalf("GetDocument(blueprint): %v", err)
	}
	_, _, err = fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindBlueprint, fixture.editorA,
		SaveDocumentVersionInput{
			ExpectedRevision: currentBlueprint.RowVersion,
			ChangeReason:     "试图引用别的项目",
			Payload:          blueprintPayload(0, foreignStandard.ID),
		})
	if err == nil {
		t.Fatal("跨项目引用必须被拒绝（否则发布时会把别的项目内容打进本次交付）")
	}
	if _, ok := model.HasFieldErrors(err); !ok {
		t.Fatalf("跨项目引用必须返回字段级错误（handler 据此返回 422），实际: %v", err)
	}

	// 反证：数据库层的边表确实没有留下跨项目引用。
	var crossProject int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM document_version_references
    WHERE project_id = $1 AND target_version_id = $2`,
		fixture.projectID, foreignStandard.ID).Scan(&crossProject); err != nil {
		t.Fatalf("count cross-project refs: %v", err)
	}
	if crossProject != 0 {
		t.Fatalf("不得留下跨项目引用边，实际 %d 条", crossProject)
	}
}

// TestDocumentUniquenessOfVersionNumberIsEnforcedByDatabase 直接对数据库断言
// 「同一文档同一版本号只能有一行」。
//
// 为什么单独一条：这条唯一约束是并发收敛的**唯一**机制（应用层读-改-写挡不住）。
// 若它被误删或误改成普通索引，上面的并发测试可能因调度而偶发通过。
func TestDocumentUniquenessOfVersionNumberIsEnforcedByDatabase(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	document, version, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindMapping, fixture.editorA,
		SaveDocumentVersionInput{
			ChangeReason: "初版映射",
			Payload: model.MappingPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindMapping),
				Format:        model.ExportFormatJSONL,
				Fields: []model.MappingField{
					{TargetField: "question", SourceField: "question", Required: true},
					{TargetField: "reasoning", SourceField: "reasoning", Required: true},
					{TargetField: "answer", SourceField: "answer", Required: true},
				},
			},
		})
	if err != nil {
		t.Fatalf("save mapping: %v", err)
	}

	_, err = fixture.pool.Exec(ctx, `
    INSERT INTO document_versions
      (document_id, version, project_id, kind, schema_version, payload, content_hash)
    VALUES ($1, $2, $3, 'mapping', 'mapping.v1', '{}'::jsonb, 'dup')`,
		document.ID, version.Version, fixture.projectID)
	if err == nil {
		t.Fatal("重复的 (document_id, version) 必须被唯一约束拒绝（这是并发收敛的唯一机制）")
	}
	if !IsUniqueViolation(err) {
		t.Fatalf("期望唯一约束冲突（SQLSTATE 23505），实际: %v", err)
	}
}

// TestDocumentPayloadValidationRejectsInvalid 覆盖 T04 验收项：
// 「配比总和、并发 1-32、schema 不合法返回字段错误」。
func TestDocumentPayloadValidationRejectsInvalid(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	cases := []struct {
		name    string
		kind    model.DocumentKind
		payload any
		field   string
	}{
		{
			name: "并发为 0",
			kind: model.KindBlueprint,
			payload: func() model.BlueprintPayload {
				p := blueprintPayload(0, 0)
				p.Nodes.Generation.Concurrency = 0
				return p
			}(),
			field: "nodes.generation.concurrency",
		},
		{
			name: "并发为 33",
			kind: model.KindBlueprint,
			payload: func() model.BlueprintPayload {
				p := blueprintPayload(0, 0)
				p.Nodes.Generation.Concurrency = 33
				return p
			}(),
			field: "nodes.generation.concurrency",
		},
		{
			name: "评估权重之和不为 1",
			kind: model.KindBlueprint,
			payload: func() model.BlueprintPayload {
				p := blueprintPayload(0, 0)
				p.Nodes.Evaluation.Weights = map[string]float64{"logic": 0.5, "fact": 0.2}
				return p
			}(),
			field: "nodes.evaluation.weights",
		},
		{
			name: "schemaVersion 与类型不符",
			kind: model.KindBlueprint,
			payload: func() model.BlueprintPayload {
				p := blueprintPayload(0, 0)
				p.SchemaVersion = "blueprint.v99"
				return p
			}(),
			field: "schemaVersion",
		},
		{
			name: "难度配比之和不是 100",
			kind: model.KindCoverage,
			payload: func() model.CoveragePayload {
				p := coveragePayload()
				p.Domains[0].Directions[0].DifficultyRatios = []model.DifficultyRatio{
					{Difficulty: "easy", Ratio: 30},
					{Difficulty: "hard", Ratio: 20},
				}
				return p
			}(),
			field: "domains[0].directions[0].difficultyRatios",
		},
		{
			name: "覆盖方向缺少稳定 ID",
			kind: model.KindCoverage,
			payload: func() model.CoveragePayload {
				p := coveragePayload()
				p.Domains[0].Directions[0].StableID = ""
				return p
			}(),
			field: "domains[0].directions[0].stableId",
		},
		{
			name: "标准步骤缺少检查点",
			kind: model.KindStandard,
			payload: model.StandardPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindStandard),
				Steps: []model.StandardStep{
					{ID: "s1", Title: "只有标题", Detail: "无检查点", Order: 0},
				},
			},
			field: "steps[0].checkpoint",
		},
		{
			name: "标准步骤为空",
			kind: model.KindStandard,
			payload: model.StandardPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindStandard),
				Steps:         []model.StandardStep{},
			},
			field: "steps",
		},
		{
			name: "规则 regex 非法",
			kind: model.KindQualityPolicy,
			payload: model.QualityPolicyPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindQualityPolicy),
				Rules: []model.QualityRule{{
					ID: "r1", Name: "括号未闭合", MatchType: model.RuleMatchRegex,
					Expression: "(", Field: "answer", Severity: model.RuleSeverityError,
					SuggestedAction: model.RuleActionReview,
				}},
			},
			field: "rules[0].expression",
		},
		{
			name: "规则建议动作是自动隔离",
			kind: model.KindQualityPolicy,
			payload: model.QualityPolicyPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindQualityPolicy),
				Rules: []model.QualityRule{{
					ID: "r1", Name: "自动隔离", MatchType: model.RuleMatchContains,
					Expression: "机密", Field: "answer", Severity: model.RuleSeverityError,
					SuggestedAction: "auto_quarantine",
				}},
			},
			field: "rules[0].suggestedAction",
		},
		{
			name: "映射重复目标字段",
			kind: model.KindMapping,
			payload: model.MappingPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindMapping),
				Format:        model.ExportFormatJSONL,
				Fields: []model.MappingField{
					{TargetField: "answer", SourceField: "a"},
					{TargetField: "answer", SourceField: "b"},
				},
			},
			field: "fields[1].targetField",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, testCase.kind, fixture.editorA,
				SaveDocumentVersionInput{ChangeReason: "校验测试", Payload: testCase.payload})
			if err == nil {
				t.Fatal("非法 payload 必须被拒绝")
			}
			fieldErrors, ok := model.HasFieldErrors(err)
			if !ok {
				t.Fatalf("必须是字段级错误（handler 据此返回 422 + fieldErrors），实际: %v", err)
			}
			found := false
			for _, fieldError := range fieldErrors {
				if fieldError.Field == testCase.field {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("必须指出字段 %q，实际 %+v", testCase.field, fieldErrors)
			}
		})
	}

	// 非法 payload 不得落库：全部被拒的请求不应留下任何版本。
	var count int
	if err := fixture.pool.QueryRow(ctx, `
    SELECT COUNT(*) FROM document_versions WHERE project_id = $1`, fixture.projectID).Scan(&count); err != nil {
		t.Fatalf("count versions: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法 payload 不得落库，实际留下 %d 个版本", count)
	}
}

// TestDocumentDeletingDraftDirectionKeepsReferencedVersion 覆盖 T04 验收项
// 「删除草稿方向不破坏被引用的版本」。
//
// 语义：版本**不可变**，因此「删除一个方向」是通过保存新版本实现的，
// 而被旧版本引用过的内容仍然在旧版本里可读。这正是「稳定 ID」的用途 ——
// 引用不靠数组下标，因此新版本里删掉一个方向不会影响旧版本的解读。
func TestDocumentDeletingDraftDirectionKeepsReferencedVersion(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	document, v1, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindCoverage, fixture.editorA,
		SaveDocumentVersionInput{ChangeReason: "两个方向", Payload: coveragePayload()})
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}

	// v2：删掉唯一的草稿方向（改为另一个方向）。
	trimmed := coveragePayload()
	trimmed.Domains[0].Directions = []model.CoverageDirection{{
		StableID: "cold-chain.humidity", Name: "湿度异常", Quota: 6, Source: "manual",
	}}
	_, v2, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindCoverage, fixture.editorA,
		SaveDocumentVersionInput{
			ExpectedRevision: document.RowVersion,
			ChangeReason:     "移除温控方向，改为湿度",
			Payload:          trimmed,
		})
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}

	// 旧版本必须仍能读到被删除的方向（历史不能被新版本改写）。
	reloaded, err := fixture.documents.GetVersion(ctx, document.ID, v1.Version)
	if err != nil {
		t.Fatalf("GetVersion(v1): %v", err)
	}
	var decoded model.CoveragePayload
	if err := json.Unmarshal(reloaded.Payload, &decoded); err != nil {
		t.Fatalf("decode v1 payload: %v", err)
	}
	if len(decoded.Domains[0].Directions) != 1 ||
		decoded.Domains[0].Directions[0].StableID != "cold-chain.temperature" {
		t.Fatalf("旧版本必须保留被删除的方向，实际 %+v", decoded.Domains[0].Directions)
	}

	// 新版本里那个方向确实不在了。
	newest, err := fixture.documents.GetVersion(ctx, document.ID, v2.Version)
	if err != nil {
		t.Fatalf("GetVersion(v2): %v", err)
	}
	var decodedNew model.CoveragePayload
	if err := json.Unmarshal(newest.Payload, &decodedNew); err != nil {
		t.Fatalf("decode v2 payload: %v", err)
	}
	if decodedNew.Domains[0].Directions[0].StableID != "cold-chain.humidity" {
		t.Fatalf("新版本必须是修改后的方向，实际 %+v", decodedNew.Domains[0].Directions)
	}
	// 稳定 ID 保持不变：引用它的东西（切片、配额统计）不会因为改名而失联。
	if decodedNew.Domains[0].StableID != decoded.Domains[0].StableID {
		t.Fatalf("领域的稳定 ID 必须跨版本保持：%q vs %q",
			decoded.Domains[0].StableID, decodedNew.Domains[0].StableID)
	}
}

// TestDocumentLogicalIDsAreIndependent 覆盖「同一项目可维护多条方案线」。
//
// 用途：契约 §2.2 的 logicalId 让「主方案」与「实验方案」各自版本化。
// 若两个 logicalId 共享版本序列，实验方案的保存会把主方案推进到 v4 ——
// 而批次快照引用的是版本号，于是「引用主方案 v3」会指到实验方案的内容。
func TestDocumentLogicalIDsAreIndependent(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	mainDocument, mainVersion, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{LogicalID: "main", ChangeReason: "主方案", Payload: standardPayload("主方案")})
	if err != nil {
		t.Fatalf("save main: %v", err)
	}
	experimentDocument, experimentVersion, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindStandard, fixture.editorA,
		SaveDocumentVersionInput{LogicalID: "experiment", ChangeReason: "实验方案", Payload: standardPayload("实验方案")})
	if err != nil {
		t.Fatalf("save experiment: %v", err)
	}

	if mainDocument.ID == experimentDocument.ID {
		t.Fatal("不同 logicalId 必须是不同的文档头（否则版本序列会互相推进）")
	}
	if experimentVersion.Version != 1 || mainVersion.Version != 1 {
		t.Fatalf("两个逻辑文档的版本序列必须独立：main=%d experiment=%d",
			mainVersion.Version, experimentVersion.Version)
	}

	mainVersions, err := fixture.documents.ListVersions(ctx, fixture.projectID, model.KindStandard, "main", 10)
	if err != nil {
		t.Fatalf("ListVersions(main): %v", err)
	}
	if len(mainVersions) != 1 || mainVersions[0].ContentHash != mainVersion.ContentHash {
		t.Fatalf("main 的历史必须只含自己的版本，实际 %d 条", len(mainVersions))
	}
}

// TestDocumentDefaultsToMainLogicalID 覆盖契约 §2.2 的默认值。
func TestDocumentDefaultsToMainLogicalID(t *testing.T) {
	fixture := newDocumentFixture(t)
	ctx := context.Background()

	if _, _, err := fixture.documents.SaveVersion(ctx, fixture.projectID, model.KindMapping, fixture.editorA,
		SaveDocumentVersionInput{
			ChangeReason: "未指定 logicalId",
			Payload: model.MappingPayload{
				SchemaVersion: model.SchemaVersionFor(model.KindMapping),
				Format:        model.ExportFormatJSONL,
				Fields:        []model.MappingField{{TargetField: "question", SourceField: "question"}},
			},
		}); err != nil {
		t.Fatalf("save: %v", err)
	}

	document, err := fixture.documents.GetDocument(ctx, fixture.projectID, model.KindMapping, "")
	if err != nil {
		t.Fatalf("GetDocument(空 logicalId): %v", err)
	}
	if document.LogicalID != DefaultLogicalID {
		t.Fatalf("未指定 logicalId 时必须落到 %q，实际 %q", DefaultLogicalID, document.LogicalID)
	}
}

// TestContentHashIsStableAcrossKeyOrder 覆盖「内容 hash 不随 JSON 键顺序变化」。
//
// 这条性质是批次快照「内容一致」检查的前提：如果 hash 依赖键顺序，
// 那么一次纯序列化差异就会被判定为「内容变了」，让检查失去意义。
func TestContentHashIsStableAcrossKeyOrder(t *testing.T) {
	first := map[string]any{"b": 1, "a": 2, "c": map[string]any{"y": 1, "x": 2}}
	second := map[string]any{"a": 2, "c": map[string]any{"x": 2, "y": 1}, "b": 1}

	hashA, err := model.ContentHash(first)
	if err != nil {
		t.Fatalf("hash first: %v", err)
	}
	hashB, err := model.ContentHash(second)
	if err != nil {
		t.Fatalf("hash second: %v", err)
	}
	if hashA != hashB {
		t.Fatalf("键顺序不同的同一内容必须得到同一 hash：%s vs %s", hashA, hashB)
	}

	// 整数值的浮点表示必须归一化：用户把 1 改成 1.0 不应被判为内容变化。
	intHash, err := model.ContentHash(map[string]any{"n": 1})
	if err != nil {
		t.Fatalf("hash int: %v", err)
	}
	floatHash, err := model.ContentHash(map[string]any{"n": 1.0})
	if err != nil {
		t.Fatalf("hash float: %v", err)
	}
	if intHash != floatHash {
		t.Fatalf("1 与 1.0 必须得到同一 hash：%s vs %s", intHash, floatHash)
	}

	// 真正不同的内容必须得到不同 hash（否则检查永远是「通过」）。
	otherHash, err := model.ContentHash(map[string]any{"n": 2})
	if err != nil {
		t.Fatalf("hash other: %v", err)
	}
	if otherHash == intHash {
		t.Fatal("不同内容必须得到不同 hash")
	}
}
