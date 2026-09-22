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
