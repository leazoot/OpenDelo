package orchestration_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/Runcoor/opendelo/internal/adapter/github"
	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/orchestration"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/store/repo"
	"github.com/Runcoor/opendelo/test/fixtures"
)

/*
 * 装配决策输入的用例。
 *
 * Decide 的行为由三个接入面各自的契约用例覆盖 —— 它们跑的是真数据库、真 Pipeline、
 * 真 Adapter，那才测得到「输入装齐了没有」。本文件只守构造这一步：
 * **少一项依赖就不许构造出来**。少了会怎样在运行期看不出来 ——
 * 一个装不齐输入的编排照常给出结论，只是那个结论按不完整的事实算出来。
 */

func TestNew_MissingAnyDependency_IsRefused(t *testing.T) {
	database := fixtures.MigratedDB(t)
	adapter, err := github.New(github.Options{})
	if err != nil {
		t.Fatalf("构造 Adapter 失败：%v", err)
	}
	registry, err := adapters.New(adapter)
	if err != nil {
		t.Fatalf("构造注册表失败：%v", err)
	}

	complete := orchestration.Submissions{
		Pipeline:   fixtures.NewGateway(t).Services.Pipeline,
		Identities: repo.NewIdentities(database), Agents: repo.NewAgents(database),
		Devices: repo.NewDevices(database), Declarations: repo.NewServiceAdapters(database),
		Registry: registry, Previews: stillPreviews{},
		Requests: repo.NewCapabilityRequests(database), Arrivals: &recordingArrivals{},
		Clock:  clock.NewFixed(fixtures.Instant),
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
	if _, err := orchestration.New(complete); err != nil {
		t.Fatalf("依赖齐全却构造失败：%v", err)
	}

	// 逐项抹掉。反向对照（上面那条）不可省：没有它，一个「永远返回错误」的
	// 构造函数也能让下面全部通过。
	blanked := map[string]func(*orchestration.Submissions){
		"决策链路":         func(s *orchestration.Submissions) { s.Pipeline = nil },
		"身份仓储":         func(s *orchestration.Submissions) { s.Identities = nil },
		"Agent 仓储":     func(s *orchestration.Submissions) { s.Agents = nil },
		"设备仓储":         func(s *orchestration.Submissions) { s.Devices = nil },
		"Adapter 声明仓储": func(s *orchestration.Submissions) { s.Declarations = nil },
		"Adapter 注册表":  func(s *orchestration.Submissions) { s.Registry = nil },
		"查勘入口":         func(s *orchestration.Submissions) { s.Previews = nil },
		"能力请求仓储":       func(s *orchestration.Submissions) { s.Requests = nil },
		"到达通知":         func(s *orchestration.Submissions) { s.Arrivals = nil },
		"时钟":           func(s *orchestration.Submissions) { s.Clock = nil },
		"日志":           func(s *orchestration.Submissions) { s.Logger = nil },
	}
	for name, blank := range blanked {
		t.Run(name, func(t *testing.T) {
			incomplete := complete
			blank(&incomplete)
			if _, err := orchestration.New(incomplete); !apperr.Is(err, apperr.CodeInternal) {
				t.Errorf("缺少%s却构造成功了（%v）", name, err)
			}
		})
	}
}

// stillPreviews 是一个从不发出任何请求的查勘入口。
//
// 本文件只守「少一项依赖就不许构造」，查勘真的会做什么由 preview_test.go
// 用真实 Adapter 与本地 fake 服务证明。
type stillPreviews struct{}

func (stillPreviews) Preview(
	context.Context, adapters.ExchangeRequest,
) (adapters.PreviewOutput, error) {
	return adapters.PreviewOutput{}, nil
}
