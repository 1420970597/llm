package main

import (
	"errors"
	"testing"
)

// 这组测试锁定 issue #5 的结构性约束：**整批记录只允许落库一次**。
//
// 背景：internal/store 的 Insert 语义是「整批完成 + 推进数据集状态」。原实现在逐题
// 循环内调用它，于是第一条记录写完就把 datasets.status 写成了终态。
// batchPersist 把「先收齐、再一次落库」变成类型层面的唯一用法。
//
// 如果将来有人把 flushOnce 挪回循环内，TestBatchPersistFlushOnceRejectsSecondCall
// 会立刻失败。

func TestBatchPersistFlushOnceCallsPersistExactlyOnce(t *testing.T) {
	batch := &batchPersist[string]{}
	batch.add("a")
	batch.add("b", "c")

	calls := 0
	var received []string
	err := batch.flushOnce(func(items []string) error {
		calls++
		received = items
		return nil
	})
	if err != nil {
		t.Fatalf("首次 flushOnce 不应报错，实际 %v", err)
	}
	if calls != 1 {
		t.Fatalf("persist 应恰好被调用 1 次，实际 %d", calls)
	}
	if len(received) != 3 {
		t.Fatalf("persist 应收到整批 3 条，实际 %d：%v", len(received), received)
	}
	if batch.len() != 3 {
		t.Fatalf("len() 应为 3，实际 %d", batch.len())
	}
}

// 这是本 lane 的核心回归守卫：重复落库必须报错，而不是静默重放。
//
// 「静默重放」正是 issue #5 的形态——循环里反复调用 Insert，每次都推进一次状态。
// 若把 flushOnce 挪回循环内，第二次迭代就会命中这条断言。
func TestBatchPersistFlushOnceRejectsSecondCall(t *testing.T) {
	batch := &batchPersist[int]{}
	batch.add(1)

	calls := 0
	persist := func([]int) error {
		calls++
		return nil
	}

	if err := batch.flushOnce(persist); err != nil {
		t.Fatalf("首次 flushOnce 不应报错，实际 %v", err)
	}
	err := batch.flushOnce(persist)
	if err == nil {
		t.Fatal("第二次 flushOnce 必须报错：整批记录只能落库一次（issue #5 的成因）")
	}
	if calls != 1 {
		t.Fatalf("报错时也不能再调用 persist，实际调用 %d 次", calls)
	}
}

// 整批跑不完时，salvage 只保住已收集的记录，且必须由调用方用不推进状态的方式落库。
func TestBatchPersistSalvagePersistsCollectedItemsWithoutMarkingComplete(t *testing.T) {
	batch := &batchPersist[int]{}
	batch.add(1, 2)

	salvaged := 0
	var salvagedItems []int
	if err := batch.salvage(func(items []int) error {
		salvaged++
		salvagedItems = items
		return nil
	}); err != nil {
		t.Fatalf("salvage 不应报错，实际 %v", err)
	}
	if salvaged != 1 || len(salvagedItems) != 2 {
		t.Fatalf("salvage 应落库已收集的 2 条，实际 calls=%d items=%v", salvaged, salvagedItems)
	}
	// salvage 之后整批并未完成，仍允许最终 flushOnce（数据集状态由调用方按失败处理，
	// 但记录本身可以被随后的成功重跑覆盖）。
	if err := batch.flushOnce(func([]int) error { return nil }); err != nil {
		t.Fatalf("salvage 不应把整批标记为已完成，flushOnce 仍应可用，实际 %v", err)
	}
}

// 没有任何记录可挽救时，salvage 不应产生写入（避免写出空批次却推进状态）。
func TestBatchPersistSalvageNoOpWhenNothingCollected(t *testing.T) {
	batch := &batchPersist[int]{}

	calls := 0
	if err := batch.salvage(func([]int) error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("salvage 不应报错，实际 %v", err)
	}
	if calls != 0 {
		t.Fatalf("空批次不应触发写入，实际调用 %d 次", calls)
	}
}

// flushOnce 之后 salvage 必须变成空操作：整批已经落库，再写一次就会重复推进状态。
func TestBatchPersistSalvageNoOpAfterFlush(t *testing.T) {
	batch := &batchPersist[int]{}
	batch.add(1)
	if err := batch.flushOnce(func([]int) error { return nil }); err != nil {
		t.Fatalf("flushOnce 不应报错，实际 %v", err)
	}

	calls := 0
	if err := batch.salvage(func([]int) error {
		calls++
		return nil
	}); err != nil {
		t.Fatalf("salvage 不应报错，实际 %v", err)
	}
	if calls != 0 {
		t.Fatalf("flush 之后 salvage 不应再写入，实际调用 %d 次", calls)
	}
}

// persist 自身失败时必须向上传递，且不把整批标记为已完成。
func TestBatchPersistPropagatesPersistError(t *testing.T) {
	batch := &batchPersist[int]{}
	batch.add(1)

	sentinel := errors.New("对象存储不可用")
	err := batch.flushOnce(func([]int) error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("应透出 persist 的错误，实际 %v", err)
	}
}
