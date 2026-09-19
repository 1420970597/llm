package main

import "fmt"

// batchPersist 累积整批记录，并保证「整批只落库一次」。
//
// 背景（issue #5）：internal/store 的 ReasoningStore.Insert / RewardStore.Insert
// 语义是「整批完成 + 推进数据集状态」——它们按传入的这批记录统计 failed / partial /
// invalid，并据此把 datasets.status 写成终态。若在逐题循环内调用，第一次迭代就会用
// 「当次那一条」推算并写死整批终态，后续题目无论成败都改不回真实状态。
//
// 所以 worker 侧必须先收集完整批，再一次性交给 Insert。本类型把这个约束变成**结构性**
// 保证：调用方没有「每收集一条就写一次」的用法可用，只有 add 完再 flushOnce。
type batchPersist[T any] struct {
	items   []T
	flushed bool
}

// add 追加本批的一部分（通常是一道题产生的记录）。
func (b *batchPersist[T]) add(items ...T) {
	b.items = append(b.items, items...)
}

// len 返回已收集的记录数，用于日志与断言。
func (b *batchPersist[T]) len() int {
	return len(b.items)
}

// flushOnce 把整批记录交给 persist 恰好一次。
//
// 重复调用返回错误而不是静默重放：静默重放正是 issue #5 的形态（多次调用 Insert
// 反复推进状态）。这里让「多次调用」直接失败，防止将来有人把 flushOnce 挪回循环内
// 却看不出问题。
func (b *batchPersist[T]) flushOnce(persist func([]T) error) error {
	if b.flushed {
		return fmt.Errorf("整批记录只能落库一次：flushOnce 被重复调用（issue #5 的成因）")
	}
	b.flushed = true
	return persist(b.items)
}

// salvage 在整批跑不完时尽力保住已收集的记录，且**不推进数据集状态**。
//
// persist 必须传入 UpsertPartial（其 markGenerated=false）：这一批并没有跑完，
// 数据集状态由调用方按失败处理。若从未收集到任何记录，则不做任何写入。
func (b *batchPersist[T]) salvage(persist func([]T) error) error {
	if b.flushed || len(b.items) == 0 {
		return nil
	}
	return persist(b.items)
}
