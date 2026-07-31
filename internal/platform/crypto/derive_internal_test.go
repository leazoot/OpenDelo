package crypto

import (
	"bytes"
	"testing"
)

/*
 * 白盒用例：盐进入密钥派生这件事，从外部行为上验证不了。
 *
 * 信封把盐算进 AEAD 的附加数据，所以「换一份盐」总会让解密失败 ——
 * 无论盐有没有真的参与派生。要证明它参与了，只能直接看派生函数。
 */

func TestDeriveKey_DifferentSaltsProduceDifferentKeys(t *testing.T) {
	secretPassword := []byte("correct horse battery staple")

	first := deriveKey(secretPassword, []byte("0123456789abcdef"), Default)
	second := deriveKey(secretPassword, []byte("fedcba9876543210"), Default)

	if bytes.Equal(first, second) {
		t.Fatal("换一份盐派生出了同一个密钥，盐没有进入密钥派生")
	}
	if len(first) != keyLength {
		t.Errorf("派生出的密钥长 %d 字节，期望 %d", len(first), keyLength)
	}
	if bytes.Equal(first, make([]byte, len(first))) {
		t.Error("派生出的密钥全是零")
	}
}

func TestDeriveKey_SameInputsProduceTheSameKey(t *testing.T) {
	// 反向对照：同样的输入必须派生出同样的密钥，否则上面那条断言恒成立。
	secretPassword := []byte("correct horse battery staple")
	salt := []byte("0123456789abcdef")

	if !bytes.Equal(deriveKey(secretPassword, salt, Default), deriveKey(secretPassword, salt, Default)) {
		t.Fatal("同样的输入派生出了不同的密钥")
	}
}

func TestDeriveKey_DifferentParamsProduceDifferentKeys(t *testing.T) {
	// 参数也必须真的进入派生：否则调高代价不会改变任何东西。
	secretPassword := []byte("correct horse battery staple")
	salt := []byte("0123456789abcdef")

	stronger := Params{MemoryKiB: MinMemoryKiB, Iterations: MinIterations + 1, Parallelism: MinParallelism}
	if bytes.Equal(deriveKey(secretPassword, salt, Default), deriveKey(secretPassword, salt, stronger)) {
		t.Fatal("换一组参数派生出了同一个密钥")
	}
}
