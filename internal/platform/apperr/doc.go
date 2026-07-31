// Package apperr 定义统一错误类型与封闭的错误码枚举。
//
// 包装信息中不得含凭据或请求正文；返回给外部的错误经脱敏但保留 operation_id。
package apperr
