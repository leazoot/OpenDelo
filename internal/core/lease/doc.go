// Package lease 负责 Lease 的签发、计量、到期与撤销。
//
// expires_at 恒非空，不存在永久授权；Scope 一经签发不可修改，只能缩短有效期。
package lease
