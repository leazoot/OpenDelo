// Package approval 管理审批项的生命周期、强认证挂钩点与超时。
//
// 高风险审批不提供「今后自动允许」选项。决策端点幂等：重复调用返回首次结果。
package approval
