package sentinel_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/platform/logging"
	"github.com/Runcoor/opendelo/internal/platform/secret"
	"github.com/Runcoor/opendelo/test/sentinel"
)

/*
 * 八个面的哨兵扫描之一：日志（REQ-NFR-002 AC1）。
 *
 * 这一面有两条互补的防线，分别对应两种泄漏方式：
 *   - 凭据出现在敏感 key 下 → 由 logging 的脱敏词表拦下；
 *   - 凭据被包在 secret.Value 里放在任意 key 下 → 由类型自身拦下。
 *
 * 其余七个面（Agent 上下文、环境变量、MCP 响应、临时文件、Console DOM、审批信息、
 * 调试输出）在对应能力落地的 Stage 补齐。
 */

// sensitiveKeys 覆盖脱敏词表的每个词以及真实会遇到的写法变体。
var sensitiveKeys = []string{
	"authorization", "Authorization", "cookie", "Set-Cookie",
	"token", "access_token", "api_key", "X-API-Key", "apiKey",
	"password", "db_password", "secret", "client_secret",
	"private_key", "PRIVATE_KEY", "credential",
}

var levels = map[string]func(*slog.Logger, context.Context, string, ...any){
	"debug": (*slog.Logger).DebugContext,
	"info":  (*slog.Logger).InfoContext,
	"warn":  (*slog.Logger).WarnContext,
	"error": (*slog.Logger).ErrorContext,
}

func TestLogging_SentinelUnderSensitiveKey_NeverReachesOutput(t *testing.T) {
	for levelName, log := range levels {
		t.Run(levelName, func(t *testing.T) {
			var buf bytes.Buffer
			logger := logging.New(logging.Options{Level: slog.LevelDebug, Writer: &buf})
			ctx := logging.WithOperationID(context.Background(), "op_sentinel")

			for _, key := range sensitiveKeys {
				for _, value := range sentinel.All() {
					log(logger, ctx, "扫描用日志", key, value)
				}
			}

			assertNoSentinel(t, buf.String())
		})
	}
}

func TestLogging_SentinelInSecretValue_NeverReachesOutput(t *testing.T) {
	// key 完全无害，只有类型在挡。
	var buf bytes.Buffer
	logger := logging.New(logging.Options{Level: slog.LevelDebug, Writer: &buf})

	for _, value := range sentinel.All() {
		held := secret.NewString(value)
		logger.Info("取用凭据", "value", held, "resource", "repo:owner/name")
		logger.Info("格式化路径", "detail", "注入前的值是 "+held.String())
	}

	assertNoSentinel(t, buf.String())
}

func TestLogging_SentinelIsActuallyPresentWhenNotProtected(t *testing.T) {
	// 反向对照：无害 key + 裸字符串确实会被原样记录。没有这条，上面两条用例
	// 可能只是因为日志根本没写出东西而通过。
	var buf bytes.Buffer
	logger := logging.New(logging.Options{Level: slog.LevelDebug, Writer: &buf})

	logger.Info("未受保护的写法", "note", sentinel.SentinelToken)

	if !strings.Contains(buf.String(), sentinel.SentinelToken) {
		t.Fatalf("对照用例没有记录到哨兵，说明扫描本身无效：%s", buf.String())
	}
}

func assertNoSentinel(t *testing.T, output string) {
	t.Helper()

	if output == "" {
		t.Fatal("没有任何日志输出，扫描等于没做")
	}
	for _, value := range sentinel.All() {
		if strings.Contains(output, value) {
			t.Errorf("输出中出现哨兵 %s：%s", value, output)
		}
	}
}
