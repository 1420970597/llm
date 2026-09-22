#!/usr/bin/env sh
# T32 集成门禁：在**真实 Postgres** 上跑完整测试，并证明必需的集成测试真的执行了。
#
# 为什么需要它：这个仓库里大量集成测试在缺 DSN 时会 `t.Skip`。于是
# `go test ./...` 全绿可能只证明「编译通过 + 单元测试通过」，
# 而迁移、并发冻结、授权矩阵、GRPO 质量路径**一条都没跑**。
# T32 的验收项原文就是「检查必须执行的测试不是 Skip」。
#
# 用法（必须由 scripts/go-test-postgres.sh 提供 DSN 与 golang 容器）：
#   ./scripts/go-test-postgres.sh sh scripts/check-integration-tests.sh
#
# 判定规则：
#   * 任何一个**必需**测试没有 `--- PASS:` 行 → 失败（被 Skip、被改名、
#     被 build tag 排除、或整个包编译失败都会命中）；
#   * 其它 Skip 只统计并打印，不影响退出码（例如需要浏览器/真实 provider 的
#     测试本来就不该在这里跑）；
#   * 失败时打印测试输出尾部，便于在 CI 日志里直接定因。
set -eu

if [ -z "${LLM_TEST_POSTGRES_DSN:-}" ]; then
  echo "::error::LLM_TEST_POSTGRES_DSN 未设置：请通过 scripts/go-test-postgres.sh 调用本脚本"
  exit 1
fi

# 必需执行的集成测试（跨包）。每个都对应一条产品规则：
#
#   migrate     迁移一一对应 / 幂等 / 从 0021 夹具升级 / 关键约束存在
#   store       实验创建即冻结、缺分不是 0、续跑不覆盖、发布候选门槛、
#               并发冻结检测、RESTRICT 保护分母
#   studio      GRPO 确定性维度与模型裁判分离（T24）
#   api         createDataset 的 provider/storage 前置校验（真实 DB 才有意义）
required="TestRunAppliesAllMigrationsAndIsIdempotent
TestCreateExperimentFreezesSnapshot
TestMissingScoreIsNotZero
TestResumeDoesNotOverwriteScoredItems
TestCreateExperimentRejectsSelfJudgingAndGRPO
TestExperimentItemsProtectSampleVersions
TestCreateCandidatePassesCleanGate
TestCreateCandidateBlocksPendingReview
TestFreezeReleaseDetectsConcurrentRevisionChange
TestFreezeReleaseIsIdempotentAndStartsBuilding
TestRunGRPOExperimentSeparatesLocalAndJudgeDimensions
TestCreateDatasetProviderGateIntegration
TestInsertQuestionsSkipsExactDuplicates
TestDifficultyStatsAlwaysIncludesThreeLevels
TestListDirectionsReadsCurrentChainStandardVersion
TestInventoryPlansLegacyDatasets
TestInventoryIsReadOnly
TestInventoryReportsStaleDatabase
TestInventorySourceHasNoWriteOrModelCalls
TestLoadStudioHealthCountsFactsAndKeepsUnknownCostSeparate
TestImportDatasetIsIdempotent
TestImportSkipsQuestionsWithoutContent
TestImportRefusesDatasetWithoutOwner
TestImportRefusesNameConflict
TestImportDryRunWritesNothing
TestLegacyDatasetProjectMapping
TestRecipeLifecycle
TestRecipePrivateVisibility
TestRecipeCopyWritesDocumentsInOneTransaction
TestRecipeCopyRejectsDraftAndTargetMismatch
TestRecipeUpgradeDoesNotAffectCopiedProject
TestRecipeDuplicateNameRejected"

echo "[integration] go test ./... -v -count=1"
output_file="$(mktemp)"
# 用 -count=1 关闭测试缓存：缓存命中时不打印 `--- PASS`，会让下面的断言误判。
if ! go test ./... -v -count=1 >"$output_file" 2>&1; then
  echo "::error::go test 失败"
  tail -n 120 "$output_file"
  rm -f "$output_file"
  exit 1
fi

missing=0
for name in $required; do
  if ! grep -qE -- "--- PASS: ${name} " "$output_file"; then
    echo "::error::必需集成测试未执行（可能被 Skip 或改名）：${name}"
    missing=1
  fi
done

pass_count=$(grep -cE -- "--- PASS: " "$output_file" || true)
skip_count=$(grep -cE -- "--- SKIP: " "$output_file" || true)
echo "[integration] PASS=${pass_count} SKIP=${skip_count}（必需 ${missing} 项缺失）"

# 打印被跳过的测试名，便于人工确认它们都是「需要真实 provider/浏览器」的那类。
if [ "$skip_count" != "0" ]; then
  grep -E -- "--- SKIP: " "$output_file" | sed 's/^/  [skip] /'
fi

if [ "$missing" != "0" ]; then
  echo "::error::存在未执行的必需集成测试：这一轮绿色不能作为 T32 的证据"
  rm -f "$output_file"
  exit 1
fi

rm -f "$output_file"
echo "[integration] OK：全部必需集成测试在真实 Postgres 上执行并通过"
