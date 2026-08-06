package cli

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	credentials "github.com/Runcoor/opendelo/internal/credential/registry"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/config"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
	"github.com/Runcoor/opendelo/internal/store"
)

/*
 * 组装根登记了哪些凭据来源。
 *
 * 断言的是注册表的内容而不是某次请求的状态码：`op` 装没装、钥匙串锁没锁
 * 都是运行期状态，跟着它走的用例会在不同机器上给出不同答案。
 */

// TestAssembleServices_RegistersTheImplementedSources_Regression 守的是原始缺陷：
// 注册表建出来了，Register 一次都没调，于是 sources 恒为空，
// 每一次取用都以 provider_unavailable 被拒。
//
// 那是 Fail Closed 的正确结果，却让产品没有一条可用的凭据路径 ——
// 而且没有任何一条用例会因此变红：每个 Provider 自己的用例都过，
// 注册表的用例自己登记来源，只有真正跑起来的那个二进制是空的。
func TestAssembleServices_RegistersTheImplementedSources_Regression(t *testing.T) {
	services, err := assembleServices(temporaryDatabase(t), temporaryIDs(t), AssembleParams{
		Config: config.Default(),
		Clock:  fixedClock(),
	})
	if err != nil {
		t.Fatalf("装配服务失败：%v", err)
	}

	// local-vault 不在其中：它还没有实现 credentials.Source（缺 Kind / Available /
	// Fetch），明文录入的入口也不存在。
	expected := []credentials.ProviderKind{
		credentials.KindOnePassword,
		credentials.KindMacOSKeychain,
	}
	if got := services.Credentials.Kinds(); !reflect.DeepEqual(got, expected) {
		t.Errorf("已登记的来源为 %v，期望 %v", got, expected)
	}
}

// TestAssembleAdapters_DeclaresAtLeastOneService 与上一条同源：Adapter 那一侧
// 空着的话，连接身份时「这个服务有没有 Adapter」永远答不出「有」。
func TestAssembleAdapters_DeclaresAtLeastOneService(t *testing.T) {
	registry, err := assembleAdapters()
	if err != nil {
		t.Fatalf("装配 Adapter 失败：%v", err)
	}

	if len(registry.Services()) == 0 {
		t.Fatal("一个 Adapter 服务都没登记，任何服务的身份都连不上")
	}
}

// temporaryDatabase 在 t.TempDir() 里开一个库，绝不碰用户真实数据目录。
func temporaryDatabase(t *testing.T) *store.DB {
	t.Helper()

	database, err := store.Open(t.Context(), store.Options{
		Path: filepath.Join(t.TempDir(), store.FileName),
	})
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(); closeErr != nil {
			t.Errorf("关闭数据库失败：%v", closeErr)
		}
	})
	return database
}

func temporaryIDs(t *testing.T) *ulid.Generator {
	t.Helper()
	return ulid.New(fixedClock())
}

func fixedClock() clock.Clock {
	return clock.NewFixed(time.Date(2026, time.July, 27, 9, 0, 0, 0, time.UTC))
}
