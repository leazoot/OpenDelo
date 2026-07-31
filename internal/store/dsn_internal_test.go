package store

import (
	"net/url"
	"strings"
	"testing"
)

/*
 * DSN 拼装的用例。
 *
 * 这条路径上的错误只在运行期暴露，而且只在某些平台上暴露：驱动拿到的是一个字符串，
 * 编译期没有任何东西会检查它的形状。
 */

// TestDataSourceName_WindowsPath_IsAValidFileURI_Regression 守的是原始缺陷：
// `url.URL{Path: "C:\\x"}` 生成 `file://C:%5Cx`，SQLite 把 `C:` 读成 authority
// 并拒绝整个 DSN（"invalid uri authority"）。Windows 上因此连不上数据库，
// 而 Unix 上一切正常，所以本仓库的用例从来没碰到过它。
func TestDataSourceName_WindowsPath_IsAValidFileURI_Regression(t *testing.T) {
	dsn := dataSourceName(`C:\Users\someone\AppData\Roaming\opendelo\data\opendelo.db`, readPool)

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN 不是合法 URL：%v（%s）", err, dsn)
	}
	if parsed.Host != "" {
		t.Errorf("authority 为 %q，必须为空 —— 非空时 SQLite 拒绝整个 DSN（%s）", parsed.Host, dsn)
	}
	if want := "/C:/Users/someone/AppData/Roaming/opendelo/data/opendelo.db"; parsed.Path != want {
		t.Errorf("path 为 %q，期望 %q", parsed.Path, want)
	}
	if !strings.HasPrefix(dsn, "file:///") {
		t.Errorf("DSN 为 %q，期望以 file:/// 开头", dsn)
	}
}

func TestDataSourceName_UnixPath_IsUnchanged(t *testing.T) {
	dsn := dataSourceName("/home/someone/.config/opendelo/data/opendelo.db", readPool)

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DSN 不是合法 URL：%v（%s）", err, dsn)
	}
	if parsed.Host != "" {
		t.Errorf("authority 为 %q，必须为空", parsed.Host)
	}
	if want := "/home/someone/.config/opendelo/data/opendelo.db"; parsed.Path != want {
		t.Errorf("path 为 %q，期望 %q —— Unix 路径不该被改写", parsed.Path, want)
	}
}

// TestDataSourceName_CarriesEveryPragma 守的是「PRAGMA 通过 DSN 下发」这件事本身：
// 连接级 PRAGMA 漏掉一个，池新建连接时就少一条约束，而查询照常成功。
func TestDataSourceName_CarriesEveryPragma(t *testing.T) {
	for _, path := range []string{
		"/home/someone/.config/opendelo/data/opendelo.db",
		`C:\Users\someone\AppData\Roaming\opendelo\data\opendelo.db`,
	} {
		parsed, err := url.Parse(dataSourceName(path, writePool))
		if err != nil {
			t.Fatalf("DSN 不是合法 URL：%v", err)
		}
		pragmas := parsed.Query()["_pragma"]
		if len(pragmas) != len(connectionPragmas) {
			t.Fatalf("%s：带了 %d 条 PRAGMA，期望 %d", path, len(pragmas), len(connectionPragmas))
		}
		for _, want := range connectionPragmas {
			if !contains(pragmas, want) {
				t.Errorf("%s：DSN 缺少 PRAGMA %q", path, want)
			}
		}
		if got := parsed.Query().Get("_txlock"); got != "immediate" {
			t.Errorf("%s：_txlock 为 %q，写池必须是 immediate", path, got)
		}
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
