// Package onepassword 通过 1Password CLI 取用凭据。
//
// 以参数数组调用外部进程，禁止 shell 拼接。CLI 不可用或输出无法解析时按 Fail Closed 拒绝。
package onepassword
