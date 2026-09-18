package store

import "github.com/jackc/pgx/v5/pgxpool"

// DB 暴露底层连接池，供各 lane 构造自己领域的 store。
//
// 设计说明：lane 禁止修改 apps/api/main.go 与 apps/worker/registry.go，
// 因此无法从 application / jobContext 拿到连接池。这里通过已有的
// AdminStore 暴露同一份池，保证全进程共用一个池，不产生重复连接。
func (s *AdminStore) DB() *pgxpool.Pool {
	return s.db
}
