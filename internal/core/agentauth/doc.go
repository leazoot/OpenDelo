// Package agentauth 负责 Agent 注册、Session Key 签发与身份绑定校验。
//
// 禁止仅凭 Agent 名称识别身份；可执行文件哈希变化后相关 Trust Memory 全部失效。
package agentauth
