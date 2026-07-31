package cli_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Runcoor/opendelo/internal/cli"
	"github.com/Runcoor/opendelo/internal/platform/clock"
)

const testVersion = "1.2.3-test"

var startedAt = time.Date(2026, 7, 28, 9, 15, 30, 123_000_000, time.UTC)

// result 是一次命令执行的全部可观察产物。
type result struct {
	code   int
	stdout string
	stderr string
}

func (r result) output() string { return r.stdout + r.stderr }

func execute(t *testing.T, ctx context.Context, args ...string) result {
	t.Helper()

	var stdout, stderr bytes.Buffer
	code := cli.Run(ctx, cli.Options{
		Args:    args,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Version: testVersion,
		Clock:   clock.NewFixed(startedAt),
	})
	return result{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func TestRun_Version_PrintsVersionAndSucceeds(t *testing.T) {
	got := execute(t, t.Context(), "--version")

	if got.code != cli.ExitOK {
		t.Errorf("退出码为 %d，期望 %d", got.code, cli.ExitOK)
	}
	if strings.TrimSpace(got.stdout) != testVersion {
		t.Errorf("stdout 为 %q，期望 %q", got.stdout, testVersion)
	}
}

func TestRun_UnknownCommand_FailsAndSuggestsUsage(t *testing.T) {
	got := execute(t, t.Context(), "delete-everything")

	if got.code == cli.ExitOK {
		t.Error("未知命令的退出码是 0")
	}
	if !strings.Contains(got.stderr, "delete-everything") {
		t.Errorf("stderr 未指出是哪个命令：%q", got.stderr)
	}
	if !strings.Contains(got.stderr, "opendelo <命令>") {
		t.Errorf("stderr 未给出用法：%q", got.stderr)
	}
	if got.stdout != "" {
		t.Errorf("失败时向 stdout 写了内容：%q", got.stdout)
	}
}

func TestRun_EveryCommandHasHelp(t *testing.T) {
	// REQ-CLI-001 AC1。用法走 stdout：它是正常输出，
	// `opendelo init --help > doc.txt` 得拿得到内容。
	for _, command := range []string{"init", "start", "status"} {
		for _, flag := range []string{"-h", "--help"} {
			t.Run(command+" "+flag, func(t *testing.T) {
				got := execute(t, t.Context(), command, flag)

				if got.code != cli.ExitOK {
					t.Errorf("退出码为 %d，期望 %d", got.code, cli.ExitOK)
				}
				if !strings.Contains(got.stdout, "opendelo "+command) {
					t.Errorf("stdout 里没有该命令的用法：%q", got.stdout)
				}
				if !strings.Contains(got.stdout, "-config-dir") {
					t.Errorf("stdout 里没有列出参数：%q", got.stdout)
				}
				if got.stderr != "" {
					t.Errorf("用法被写到了 stderr：%q", got.stderr)
				}
			})
		}
	}
}

func TestRun_NoArguments_PrintsUsage(t *testing.T) {
	got := execute(t, t.Context())

	if got.code != cli.ExitOK {
		t.Errorf("退出码为 %d，期望 %d", got.code, cli.ExitOK)
	}
	for _, command := range []string{"init", "start", "status", "version"} {
		if !strings.Contains(got.stdout, command) {
			t.Errorf("用法里没有列出 %s：%q", command, got.stdout)
		}
	}
}

func TestRun_UnknownFlag_FailsWithoutRunningTheCommand(t *testing.T) {
	got := execute(t, t.Context(), "status", "--no-such-flag")

	if got.code == cli.ExitOK {
		t.Error("非法参数的退出码是 0")
	}
	if !strings.Contains(got.output(), "no-such-flag") {
		t.Errorf("输出未指出是哪个参数：%q", got.output())
	}
}
