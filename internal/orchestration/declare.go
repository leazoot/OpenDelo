package orchestration

import (
	"context"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
	"github.com/Runcoor/opendelo/internal/platform/ulid"
)

/*
 * 连接服务：把一个 Adapter 的编译期声明落进 `service_adapters`。
 *
 * Adapter 的代码在编译期注册（ADR-009），但决策链路读的是数据库里的声明 ——
 * 两者之间原本没有任何桥（R-24）。全新安装后那张表是空的，
 * 于是每一次调用都以 capability_not_offered 被拒：方向是安全的，
 * 但产品在真实安装上不可用。
 *
 * **落点是连接流程而不是启动。** 启动时把四个 Adapter 一并写进去并置为
 * enabled，等于替用户「连接」了四个他没配过凭据的服务 —— 那几个服务会出现在
 * 界面上、出现在工具清单里，而用户从没表示过要用它们。
 * 这里一次只声明一个：用户连接哪个服务的身份，就写哪一个。
 */

// Declarer 在用户连接某个服务时确保它的声明在库里。
type Declarer struct {
	registry     *adapters.Registry
	declarations adapters.DeclarationRepository
	clock        clock.Clock
	ids          *ulid.Generator
}

// DeclarerOptions 是 NewDeclarer 的输入，缺一不可。
type DeclarerOptions struct {
	Registry     *adapters.Registry
	Declarations adapters.DeclarationRepository
	Clock        clock.Clock
	IDs          *ulid.Generator
}

func NewDeclarer(options DeclarerOptions) (*Declarer, error) {
	missing := ""
	switch {
	case options.Registry == nil:
		missing = "Adapter 注册表"
	case options.Declarations == nil:
		missing = "声明仓储"
	case options.Clock == nil:
		missing = "时钟"
	case options.IDs == nil:
		missing = "ID 生成器"
	}
	if missing != "" {
		return nil, apperr.New(apperr.CodeInvalidConfiguration).
			WithDetail("连接服务缺少" + missing)
	}
	return &Declarer{
		registry:     options.Registry,
		declarations: options.Declarations,
		clock:        options.Clock,
		ids:          options.IDs,
	}, nil
}

// EnsureDeclared 保证这个服务的声明在库里且启用。
//
// 已经声明过就原样返回，不重写：声明是审计里历史请求的解释依据，
// 每次连接都覆盖一遍会让「当时允许的是什么」随最近一次连接而变。
// 需要更新声明内容时走 UpdateDeclaration，那是另一件事。
//
// 认不出的服务返回 not_found 而不是写一份空声明 —— 一个没有 Adapter 的服务
// 连上了也执行不了。
func (d *Declarer) EnsureDeclared(ctx context.Context, service string) error {
	if _, err := d.declarations.DeclarationByService(ctx, service); err == nil {
		return nil
	} else if !apperr.Is(err, apperr.CodeNotFound) {
		return err
	}

	declaration, err := d.declare(service)
	if err != nil {
		return err
	}

	if _, err := d.declarations.CreateDeclaration(ctx, declaration); err != nil {
		// 服务名上有唯一索引。两次并发连接同一个服务时，后到的那次撞上它 ——
		// 那说明另一次已经写好了，不是一次失败。
		if apperr.Is(err, apperr.CodeConflict) {
			return nil
		}
		return err
	}
	return nil
}

// declare 从编译期注册表推出一份可落库的声明。
//
// 内容全部来自 Adapter 自己的声明，没有一项是这里编出来的：
// 白名单取自各操作的方法与路径，兜底风险取最高的那一档，
// 脱敏规则是各操作声明的并集。
func (d *Declarer) declare(service string) (adapters.Declaration, error) {
	adapter, err := d.registry.Adapter(service)
	if err != nil {
		return adapters.Declaration{}, err
	}
	declared := adapter.Capabilities()

	capabilities, err := intent.EncodeCapabilities(service, declared)
	if err != nil {
		return adapters.Declaration{}, err
	}
	paths, err := intent.EncodePaths(declared)
	if err != nil {
		return adapters.Declaration{}, err
	}
	methods, err := intent.EncodeMethods(declared)
	if err != nil {
		return adapters.Declaration{}, err
	}
	redaction, err := intent.EncodeRedactionRules(declared)
	if err != nil {
		return adapters.Declaration{}, err
	}

	id, err := d.ids.NewID()
	if err != nil {
		return adapters.Declaration{}, err
	}
	now := d.clock.Now()

	return adapters.Declaration{
		ID:               id,
		Service:          service,
		Kind:             adapter.Kind(),
		DisplayName:      service,
		BaseURL:          adapter.BaseURL(),
		AuthScheme:       adapter.AuthScheme(),
		Capabilities:     capabilities,
		AllowedPaths:     paths,
		AllowedMethods:   methods,
		RedactionRules:   redaction,
		DefaultRiskLevel: intent.DefaultRiskOf(declared),
		Status:           adapters.StatusEnabled,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}
