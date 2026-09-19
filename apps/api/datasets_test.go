package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// 本文件覆盖 issue #58：创建数据集必须拒绝不存在的 providerId。
//
// 缺陷原文：POST /api/v1/datasets 带 providerId=999999 返回 201 且已落库，
// 失败被推迟到生成阶段（几分钟甚至几十分钟后）。
//
// 这里锁死修复后的三条语义：
//  1. 非 0 且不存在的 providerId -> 400 + "指定的 AI 服务不存在"；
//  2. providerId 为 0 仍然放行（列默认值就是 0，是既有的「暂不绑定」语义）；
//  3. 查库失败 -> 500（而不是 400），不能让用户以为是自己填错了 id。
//
// 与 apps/api 既有测试一致，不连数据库：把「取 provider 列表」作为参数注入，
// resolveDatasetProvider 的全部分支都能在内存里跑到。
//
// 真实 Postgres + 真实 handler（含「拒绝时不落库」与「合法时确实 201 落库」）
// 见 datasets_provider_integration_test.go；真实容器的端到端探测
// 见 test/l15_dataset_provider.py。

func testProviders() []model.ModelProvider {
	return []model.ModelProvider{
		{ID: 1, Name: "deepseek-v4.1-flash"},
		{ID: 7, Name: "judge-b"},
	}
}

func fixedList(providers []model.ModelProvider) func(context.Context) ([]model.ModelProvider, error) {
	return func(context.Context) ([]model.ModelProvider, error) { return providers, nil }
}

func failingList(err error) func(context.Context) ([]model.ModelProvider, error) {
	return func(context.Context) ([]model.ModelProvider, error) { return nil, err }
}

func TestResolveDatasetProvider(t *testing.T) {
	listErr := errors.New("pq: connection refused")
	cases := []struct {
		name       string
		providerID int64
		list       func(context.Context) ([]model.ModelProvider, error)
		wantStatus int
		wantMsg    string
	}{
		{
			name:       "存在的 provider 放行",
			providerID: 1,
			list:       fixedList(testProviders()),
			wantStatus: 0,
		},
		{
			// 只换 id、不换 fixture：命中与否必须取决于「是否在列表里」，不是位置。
			name:       "非首位的 provider 也能命中（防止只比对第一个）",
			providerID: 7,
			list:       fixedList(testProviders()),
			wantStatus: 0,
		},
		{
			name:       "不存在的 provider 拒绝",
			providerID: 999999,
			list:       fixedList(testProviders()),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "指定的 AI 服务不存在",
		},
		{
			name:       "providerId=0 表示暂不绑定，放行",
			providerID: 0,
			list:       fixedList(testProviders()),
			wantStatus: 0,
		},
		{
			name:       "providerId=0 在干净库（无 provider）下也必须放行",
			providerID: 0,
			list:       fixedList(nil),
			wantStatus: 0,
		},
		{
			name:       "全新部署（无 provider）时任何非 0 id 都被拒绝",
			providerID: 1,
			list:       fixedList(nil),
			wantStatus: http.StatusBadRequest,
			wantMsg:    "指定的 AI 服务不存在",
		},
		{
			name:       "查库失败报 500 而不是 400",
			providerID: 1,
			list:       failingList(listErr),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, err := resolveDatasetProvider(context.Background(), tc.providerID, tc.list)
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if tc.wantMsg == "" {
				if tc.wantStatus == http.StatusInternalServerError {
					// 查库失败必须原样带回 store 层错误，不能被换成哨兵。
					if !errors.Is(err, listErr) {
						t.Fatalf("error = %v, want the store error unchanged", err)
					}
					return
				}
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
				return
			}
			if !errors.Is(err, errProviderNotFound) {
				t.Fatalf("error = %v, want errProviderNotFound", err)
			}
			if err.Error() != tc.wantMsg {
				t.Fatalf("error 文案 = %q, want %q（契约 §1.1 冻结）", err.Error(), tc.wantMsg)
			}
		})
	}
}

// providerId=0 时不得触发任何查询：这类数据集允许在没有任何 provider 的
// 全新部署上创建，实现若先查库再判断 0，就会在干净环境里报 500。
func TestResolveDatasetProviderSkipsLookupForZero(t *testing.T) {
	called := false
	list := func(context.Context) ([]model.ModelProvider, error) {
		called = true
		return nil, nil
	}
	status, err := resolveDatasetProvider(context.Background(), 0, list)
	if status != 0 || err != nil {
		t.Fatalf("status = %d, err = %v; want 0, nil", status, err)
	}
	if called {
		t.Fatal("providerId=0 不应触发 provider 查询")
	}
}
