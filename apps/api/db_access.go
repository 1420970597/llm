package main

import "github.com/jackc/pgx/v5/pgxpool"

// db 暴露 API 进程的连接池，供 lane 在 RegisterRoutes / RegisterDatasetRouter
// 里构造自己领域的 store，而无需修改 main.go。
func (app *application) db() *pgxpool.Pool {
	return app.store.DB()
}
