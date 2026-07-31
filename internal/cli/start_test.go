package cli_test

import (
	"strings"
	"testing"

	"github.com/Runcoor/opendelo/internal/cli"
)

func TestStart_InvalidPort_IsRejectedBeforeAnythingBinds(t *testing.T) {
	got := execute(t, t.Context(), "start", "--config-dir", newConfigDir(t), "--web-api-port", "70000")

	if got.code == cli.ExitOK {
		t.Fatal("端口越界时退出码是 0")
	}
	if !strings.Contains(got.stderr, "web_api_port") {
		t.Errorf("stderr 未指出是哪个设置：%q", got.stderr)
	}
}

func TestStart_NonLoopbackWithoutExplicitConsent_IsRejected(t *testing.T) {
	// 监听范围是安全边界。
	got := execute(t, t.Context(), "start", "--config-dir", newConfigDir(t), "--listen-address", "0.0.0.0")

	if got.code == cli.ExitOK {
		t.Fatal("非回环监听未经显式确认就被接受了")
	}
	if !strings.Contains(got.stderr, "allow_non_loopback") {
		t.Errorf("stderr 未说明如何显式确认：%q", got.stderr)
	}
}

func TestStart_SamePortForTwoFaces_IsRejected(t *testing.T) {
	// 三个接入面的认证互相独立，共用端口会把这条边界抹掉。
	got := execute(t, t.Context(), "start", "--config-dir", newConfigDir(t), "--web-api-port", "8788")

	if got.code == cli.ExitOK {
		t.Fatal("Web API 与 Agent Proxy 共用端口时退出码是 0")
	}
	if !strings.Contains(got.stderr, "8788") {
		t.Errorf("stderr 未指出冲突的端口：%q", got.stderr)
	}
}
