package exporter

import (
	"fmt"
	"time"
)

// Request 一次导出请求的完整参数。
//
// 为什么需要它：worker 的任务载荷（jobPayload）只有 type/datasetId/retry
// 三个字段，且该文件属于冻结契约不可修改，格式与映射无法随队列消息传递。
// 因此 API 在入队前把 Request 写入 Redis，worker 出队后按数据集 ID 取回。
type Request struct {
	Format    string         `json:"format"`
	MappingID int64          `json:"mappingId"`
	Filters   map[string]any `json:"filters"`
}

// RequestTTL 待执行请求的存活时间，避免请求积压时无限占用 Redis。
const RequestTTL = time.Hour

// RequestKey 待执行导出请求在 Redis 中的键。
//
// API 与 worker 共用本函数，防止两侧键格式漂移导致 worker 取不到请求。
func RequestKey(datasetID int64) string {
	return fmt.Sprintf("export_request:%d", datasetID)
}
