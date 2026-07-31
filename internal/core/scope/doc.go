// Package scope 推导单次授权的最小 Scope，覆盖十个维度。
//
// 任一维度无法确定即拒绝。请求中携带的越权字段被忽略并记审计。
//
// 本包只收敛范围，不判断是否允许 —— 那是 core/decision 的独占职责
package scope
