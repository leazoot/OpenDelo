// Package matcher 按五级顺序把请求匹配到唯一 Identity。
//
// 存在多个候选时必须上抛 ambiguous，不得任选其一。
package matcher
