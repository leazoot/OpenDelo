/*
 * 哨兵值。
 *
 * **与 `test/sentinel/values.go` 必须逐字一致**，由
 * `test/sentinel/e2e_values_test.go` 静态核对：两边各写一份而没人对账的话，
 * E2E 扫的就不是 Gateway 真正取到的那个值，八个面全绿也说明不了任何事。
 *
 * 这些是编造的字符串，不是任何真实服务的凭据。
 */

export const sentinelToken = 'SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK'
export const sentinelPassword = 'SENTINEL_PASSWORD_8c1f04_DO_NOT_LEAK'
export const sentinelAPIKey = 'SENTINEL_APIKEY_5e9b27_DO_NOT_LEAK'
export const sentinelPrivateKey = 'SENTINEL_PRIVATEKEY_1a6d80_DO_NOT_LEAK'

/** allSentinels 是全部哨兵值，供逐个扫描各个面。 */
export const allSentinels: readonly string[] = [
  sentinelToken,
  sentinelPassword,
  sentinelAPIKey,
  sentinelPrivateKey,
]
