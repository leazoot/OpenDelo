package sentinel

// 哨兵值全局唯一且不会自然出现在任何输出里。
// 任何一面的扫描命中其中之一，即说明凭据泄漏。
//
// 这些是编造的字符串，不是任何真实服务的凭据。
const (
	// SentinelToken 用于通用令牌位（Authorization、session、access token）。
	SentinelToken = "SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"
	// SentinelPassword 用于主密码与数据库密码位。
	SentinelPassword = "SENTINEL_PASSWORD_8c1f04_DO_NOT_LEAK"
	// SentinelAPIKey 用于模型服务与第三方 API Key 位。
	SentinelAPIKey = "SENTINEL_APIKEY_5e9b27_DO_NOT_LEAK"
	// SentinelPrivateKey 用于私钥与设备密钥位。
	SentinelPrivateKey = "SENTINEL_PRIVATEKEY_1a6d80_DO_NOT_LEAK"
)

// All 返回全部哨兵值，供逐个扫描各个面。
func All() []string {
	return []string{SentinelToken, SentinelPassword, SentinelAPIKey, SentinelPrivateKey}
}
