// Package localvault 实现本地加密保险库，是唯一存放凭据密文的场景。
//
// Argon2id 派生密钥 + XChaCha20-Poly1305 加密，独立主密码，自动锁定，内存清零。
// 解锁失败信息不区分「密码错误」与「Vault 不存在」，避免枚举。
package localvault
