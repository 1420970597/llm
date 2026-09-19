package main

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/1420970597/llm/internal/model"
)

// storageList 构造一个返回固定列表/固定错误的 list 函数。
func storageList(profiles []model.StorageProfile, err error) func(context.Context) ([]model.StorageProfile, error) {
	return func(context.Context) ([]model.StorageProfile, error) {
		return profiles, err
	}
}

// TestResolveDatasetStorage 是 issue #83 的核心守卫：创建任务时必须拦住
// 「注定在答案阶段失败」的输入，并给出**用户能照着做**的提示。
//
// 每个用例都断言状态码与文案关键词，原因：
//   - 状态码是接口契约（400 = 用户的输入问题，500 = 服务端问题）；
//   - 文案是用户体验契约 —— issue 的本质是「用户不知道原因也不知道怎么修」，
//     所以「提示里必须说到去哪个页面配」比「返回了 400」更重要。
func TestResolveDatasetStorage(t *testing.T) {
	active := model.StorageProfile{ID: 1, Name: "默认结果存储", IsActive: true, IsDefault: true}
	inactive := model.StorageProfile{ID: 2, Name: "已停用存储", IsActive: false}

	cases := []struct {
		name          string
		storageID     int64
		profiles      []model.StorageProfile
		listErr       error
		wantStatus    int
		wantErrSubstr string
	}{
		{
			// 全新部署的真实形状：表为空 + 任务未绑定存储 → 必须拦住。
			name:          "表为空且未指定存储：拦住并引导去配置",
			storageID:     0,
			profiles:      nil,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "系统设置",
		},
		{
			name:          "只有停用的存储且未指定：同样拦住",
			storageID:     0,
			profiles:      []model.StorageProfile{inactive},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "系统设置",
		},
		{
			name:       "有可用存储且未指定：放行（走默认存储）",
			storageID:  0,
			profiles:   []model.StorageProfile{active},
			wantStatus: 0,
		},
		{
			name:       "指定的存储存在且可用：放行",
			storageID:  1,
			profiles:   []model.StorageProfile{active, inactive},
			wantStatus: 0,
		},
		{
			name:          "指定的存储不存在：拦住并提示重选",
			storageID:     999,
			profiles:      []model.StorageProfile{active},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "重新选择",
		},
		{
			name:          "指定的存储已停用：拦住并提示重选",
			storageID:     2,
			profiles:      []model.StorageProfile{active, inactive},
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "重新选择",
		},
		{
			// 查库失败不是用户的错：报 400 会让用户以为是自己填错了 id。
			name:       "查库失败：必须 500 而不是 400",
			storageID:  1,
			profiles:   nil,
			listErr:    errors.New("connection refused"),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			status, err := resolveDatasetStorage(
				context.Background(), testCase.storageID, storageList(testCase.profiles, testCase.listErr))

			if status != testCase.wantStatus {
				t.Fatalf("status = %d, 期望 %d (err=%v)", status, testCase.wantStatus, err)
			}
			if testCase.wantStatus == 0 {
				if err != nil {
					t.Fatalf("放行时不应返回 error，得到 %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("拦截时必须返回可读原因")
			}
			if testCase.wantErrSubstr != "" && !contains(err.Error(), testCase.wantErrSubstr) {
				t.Errorf("错误文案应含 %q（用户要能照着修），得到 %q", testCase.wantErrSubstr, err.Error())
			}
		})
	}
}

// TestResolveDatasetStorageNeverLeaksInternalEnglish 确认面向用户的提示里
// 不出现内部英文错误串。
//
// 这条直接对应 issue #83 的原始现象：用户只看到「系统同步中」，
// 而真实原因 `no rows in result set` 只出现在 worker 日志里。
// 反向也要成立：修好之后，**不该**把英文内部错误原样搬到界面上。
func TestResolveDatasetStorageNeverLeaksInternalEnglish(t *testing.T) {
	status, err := resolveDatasetStorage(context.Background(), 0, storageList(nil, nil))
	if status != http.StatusBadRequest || err == nil {
		t.Fatalf("空表应被拦住，status=%d err=%v", status, err)
	}
	for _, forbidden := range []string{"no rows", "pgx", "ErrNoRows", "result set"} {
		if contains(err.Error(), forbidden) {
			t.Errorf("用户可见文案不应包含内部英文串 %q，得到 %q", forbidden, err.Error())
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
