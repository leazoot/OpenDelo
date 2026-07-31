package localvault

import (
	"bytes"
	"testing"
)

/*
 * 白盒用例：解出来的条目在用完后被清零这件事，从外部行为上验证不了 ——
 * 那些字节是局部变量，调用返回后就不可达了。只能直接看清零函数本身。
 */

func TestZeroEntries_ClearsEveryValue(t *testing.T) {
	entries := map[string][]byte{
		"github/token":     []byte("SENTINEL_TOKEN_d3adb33f_DO_NOT_LEAK"),
		"cloudflare/token": []byte("SENTINEL_APIKEY_5e9b27_DO_NOT_LEAK"),
	}
	// 留一份引用：清零必须作用在底层数组上，而不是换一个新切片。
	held := entries["github/token"]

	zeroEntries(entries)

	for reference, value := range entries {
		if !bytes.Equal(value, make([]byte, len(value))) {
			t.Errorf("%s 的值没有被清零：%q", reference, value)
		}
	}
	if !bytes.Equal(held, make([]byte, len(held))) {
		t.Errorf("清零没有作用在底层数组上：%q", held)
	}
}

func TestZeroEntries_HandlesEmptyMap(t *testing.T) {
	// 反向对照兼边界：空表与 nil 不该 panic，也不该让上面那条断言恒成立。
	zeroEntries(map[string][]byte{})
	zeroEntries(nil)

	entries := map[string][]byte{"a": []byte("x")}
	if bytes.Equal(entries["a"], make([]byte, 1)) {
		t.Fatal("用例数据本身就是零，这条断言没有意义")
	}
	zeroEntries(entries)
	if !bytes.Equal(entries["a"], make([]byte, 1)) {
		t.Error("单条目的表没有被清零")
	}
}
