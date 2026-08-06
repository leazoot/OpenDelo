// Package bench 是 REQ-NFR-001 中跨包指标的基准。
//
// 只放「一个包内测不了」的那几项：决策链路要把意图、匹配、收敛、风险、决策、
// 审批与账本一起跑完，任何一个包自己都给不出这个数。单包内能测的指标留在原地
// （Ledger 查询与记忆匹配在 internal/store，前端首屏与倒计时在 test/e2e）。
//
// 六项指标各自的落点见 docs/09_TEST_PLAN.md §10.7。
package bench
