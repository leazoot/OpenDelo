// Package pipeline 编排决策全链路，是整条链路唯一的放行出口。
//
// 顶层 recover panic 并转为拒绝 + error 级审计事件。审计写入是执行的前置条件。
package pipeline
