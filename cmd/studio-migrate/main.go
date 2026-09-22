// Command studio-migrate 是旧数据迁移的**只读盘点与映射规划**工具（Issue #160 T30）。
//
// 用法：
//
//	studio-migrate -dsn "postgres://..." -report artifacts/studio-migrate/report.json
//	studio-migrate -summary          # 只打印摘要，不落盘
//
// dry-run 由**数据库**保证：本工具的查询全部跑在 `BEGIN READ ONLY` 事务里，
// 任何写操作都会被 Postgres 拒绝。因此没有 `-apply` 开关 ——
// T30 的原文要求就是「dry-run 不写业务数据、不调用模型」，而
// 「提供一个写开关再要求别人别按」是把保证建立在人的记性上。
// 真实导入属于 T31（幂等导入），它会有独立的、带游标与对账的路径。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/1420970597/llm/internal/legacy"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN（缺省依次读 LLM_MIGRATE_DSN / LLM_TEST_POSTGRES_DSN / DATABASE_URL）")
	reportPath := flag.String("report", "artifacts/studio-migrate/report.json", "报告落盘路径（空字符串表示不落盘）")
	summaryOnly := flag.Bool("summary", false, "只打印摘要，不写报告文件")
	sampleLimit := flag.Int("sample-limit", 5, "每个决策保留的示例 ID 数")
	timeout := flag.Duration("timeout", 5*time.Minute, "整体超时")

	// T31 的幂等导入。刻意用 -apply 显式开启写入：
	// 默认行为是「只打印计划」，因为一次误执行的导入会在新库里留下
	// 一批看起来正常的样本，而它们与真实运行出来的内容无法区分。
	importDataset := flag.Int64("import-dataset", 0, "导入该 dataset（不带 -apply 时只打印计划）")
	targetProject := flag.Int64("target-project", 0, "显式指定目标项目（缺省按 legacy_dataset_id 反查或新建）")
	workspaceID := flag.Int64("workspace", 0, "目标工作区（缺省用默认工作区）")
	actorID := flag.Int64("actor", 0, "执行导入的用户 ID（写入 created_by）")
	apply := flag.Bool("apply", false, "真正写入（默认 dry-run）")
	resume := flag.Bool("resume", true, "从台账游标续跑；false 表示从头走一遍（内容 hash 仍保证不重复）")
	flag.Parse()

	resolved := *dsn
	if resolved == "" {
		for _, key := range []string{"LLM_MIGRATE_DSN", "LLM_TEST_POSTGRES_DSN", "DATABASE_URL"} {
			if value := os.Getenv(key); value != "" {
				resolved = value
				break
			}
		}
	}
	if resolved == "" {
		fail("缺少 DSN：请用 -dsn 或设置 LLM_MIGRATE_DSN / LLM_TEST_POSTGRES_DSN / DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, resolved)
	if err != nil {
		fail("连接数据库失败：" + err.Error())
	}
	defer pool.Close()

	// T31：导入分支。dry-run 时不写任何业务数据。
	if *importDataset > 0 {
		runImport(ctx, pool, importOptions{
			DatasetID: *importDataset, WorkspaceID: *workspaceID, TargetProjectID: *targetProject,
			ActorID: *actorID, Apply: *apply, Resume: *resume,
		})
		return
	}

	report, err := legacy.Inventory(ctx, pool, legacy.Options{SampleLimit: *sampleLimit})
	if err != nil {
		fail("盘点失败：" + err.Error())
	}

	fmt.Print(report.Summary())

	if !*summaryOnly {
		if err := legacy.WriteReport(report, *reportPath); err != nil {
			fail("写入报告失败：" + err.Error())
		}
		fmt.Printf("报告已写入 %s\n", *reportPath)
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "studio-migrate: "+message)
	os.Exit(1)
}

// importOptions 是 CLI 侧的导入参数。
type importOptions struct {
	DatasetID       int64
	WorkspaceID     int64
	TargetProjectID int64
	ActorID         int64
	Apply           bool
	Resume          bool
}

// runImport 执行（或只计划）一次导入。
//
// 默认是 dry-run：一次误执行的导入会在新库里留下一批看起来正常的样本，
// 而它们与真实运行出来的内容无法区分 —— 那是「事后无法回退」的操作，
// 因此必须由人显式加 -apply。
func runImport(ctx context.Context, pool *pgxpool.Pool, options importOptions) {
	if !options.Apply {
		fmt.Println("dry-run（未加 -apply）：只产出计划与对账水位，不写任何业务数据")
	}
	if options.ActorID <= 0 {
		fail("-apply 需要一个 -actor（导入要写入 created_by 与审计）")
	}

	deps := legacy.NewImportDeps(pool)

	summary, err := legacy.ImportDataset(ctx, deps, legacy.ImportOptions{
		DatasetID:       options.DatasetID,
		TargetProjectID: options.TargetProjectID,
		WorkspaceID:     options.WorkspaceID,
		ActorID:         options.ActorID,
		DryRun:          !options.Apply,
		Resume:          options.Resume,
	})
	if err != nil {
		// 即使失败也要把已经得到的对账/计数打出来：运维需要知道停在哪一步。
		printImportSummary(summary)
		fail("导入失败：" + err.Error())
	}
	printImportSummary(summary)
	if summary.Status == "failed" {
		os.Exit(1)
	}
}

func printImportSummary(summary legacy.ImportSummary) {
	fmt.Printf("dataset=%d project=%d batch=%d status=%s replay=%v dryRun=%v\n",
		summary.DatasetID, summary.ProjectID, summary.BatchID, summary.Status, summary.Replay, summary.DryRun)
	fmt.Printf("计数：源 %d / 导入 %d / 跳过(已存在) %d / 跳过(无内容) %d / 失败 %d\n",
		summary.Counts.SourceItems, summary.Counts.ImportedVersions,
		summary.Counts.SkippedExisting, summary.Counts.SkippedNoContent, summary.Counts.FailedItems)
	fmt.Printf("对账：问题 %d→%d，SFT %d，样本 %d，内容版本 %d\n",
		summary.Before.Questions, summary.After.Questions, summary.After.SFTRecords,
		summary.After.Samples, summary.After.Versions)
	if summary.ContentHash != "" {
		fmt.Printf("内容摘要：%s\n", summary.ContentHash)
	}
	for _, note := range summary.Notes {
		fmt.Printf("  · %s\n", note)
	}
	for index, failure := range summary.Failures {
		if index >= 10 {
			fmt.Printf("  · …还有 %d 条失败（完整列表在台账的 failures 里）\n", len(summary.Failures)-index)
			break
		}
		fmt.Printf("  · 失败 source=%d：%s\n", failure.SourceID, failure.Reason)
	}
}
