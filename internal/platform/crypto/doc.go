// Package crypto 提供字段级加解密：Argon2id 密钥派生 + XChaCha20-Poly1305。
//
// 加密参数（salt、KDF 参数、算法标识）随密文一同存储，以支持后续升级。
package crypto
