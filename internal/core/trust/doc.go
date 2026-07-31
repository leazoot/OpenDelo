// Package trust 负责 Trust Memory 的生成与失效。
//
// 生成时执行 Scope 收敛校验：资源、操作、时间、Agent、项目、身份、环境中任一维度扩大即不生成。
package trust
