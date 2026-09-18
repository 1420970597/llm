package main

import "github.com/jackc/pgx/v5/pgxpool"

// db 暴露 worker 进程的连接池，供 lane 在 RegisterJobHandler 里构造自己领域的
// store，而无需修改 apps/worker/main.go 或 registry.go。
func (jc *jobContext) db() *pgxpool.Pool {
	return jc.prompts.DB()
}
