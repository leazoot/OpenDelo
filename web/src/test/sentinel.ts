/*
 * 凭据哨兵。
 *
 * 与 Go 侧 `test/sentinel` 的取值保持一致：Console DOM 是八个面之一，
 * 两边用同一个字符串，一次扫描就能覆盖同一条链路的两端。
 *
 * 这个值全局唯一且不会自然出现在任何真实数据里。
 */
export const SENTINEL_TOKEN = 'SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK'
