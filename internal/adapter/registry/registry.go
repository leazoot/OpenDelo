package registry

import (
	"sort"
	"strings"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Adapter 注册表（REQ-ADAPTER-001、ADR-009）。
 *
 * Adapter 在编译期注册：动态加载会成为绕过风险声明的通道，一个能在运行时装进来的
 * Adapter 也就能在运行时改掉自己的风险标签。
 *
 * 注册表只回答两个问题：这个服务由谁负责，以及这个操作被声明过没有。
 * **它不决定是否允许** —— 那是 core/decision 的事。
 */

// Adapter 是一个服务适配器的注册契约。
type Adapter interface {
	// Service 是这个 Adapter 负责的服务名，与 Scope 的 service 维度对应。
	Service() string
	// Kind 是种类，用于 Console 展示与配置。
	Kind() Kind
	// Capabilities 是全部被声明的操作。未出现在这里的操作无法被调用。
	Capabilities() []Capability
}

// Registry 是已注册的 Adapter 集合。
type Registry struct {
	adapters     map[string]Adapter
	capabilities map[string]map[string]Capability
}

// New 注册全部 Adapter，任何一项声明不合法都返回错误。
//
// 调用方是进程启动路径：返回错误即启动失败（Fail Fast，AC1）。
// 不提供「跳过有问题的 Adapter 继续启动」的选项 —— 那是静默降级。
func New(adapters ...Adapter) (*Registry, error) {
	registry := &Registry{
		adapters:     make(map[string]Adapter, len(adapters)),
		capabilities: make(map[string]map[string]Capability, len(adapters)),
	}

	for _, adapter := range adapters {
		if err := registry.add(adapter); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *Registry) add(adapter Adapter) error {
	if adapter == nil {
		return declarationError("注册了一个空的 Adapter")
	}

	service := strings.TrimSpace(adapter.Service())
	if service == "" {
		return declarationError("Adapter 没有声明服务名")
	}
	if _, exists := r.adapters[service]; exists {
		return declarationError("服务 " + service + " 被注册了两次")
	}
	if !validKind(adapter.Kind()) {
		return declarationError(service + " 声明的种类认不出来：" + string(adapter.Kind()))
	}

	declared := adapter.Capabilities()
	if len(declared) == 0 {
		return declarationError(service + " 没有声明任何操作")
	}

	operations := make(map[string]Capability, len(declared))
	for _, capability := range declared {
		if err := capability.Validate(); err != nil {
			return err
		}
		if _, exists := operations[capability.Operation]; exists {
			return declarationError(service + " 把操作 " + capability.Operation + " 声明了两次")
		}
		operations[capability.Operation] = capability
	}

	r.adapters[service] = adapter
	r.capabilities[service] = operations
	return nil
}

// Adapter 返回负责该服务的 Adapter。
//
// 查不到即「无法确定服务」，按 Fail Closed 一律拒绝
func (r *Registry) Adapter(service string) (Adapter, error) {
	adapter, found := r.adapters[service]
	if !found {
		return nil, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("没有 Adapter 负责服务 " + service)
	}
	return adapter, nil
}

// Capability 返回该服务下某个操作的声明。
//
// 这是 REQ-ADAPTER-001 AC2「未声明的操作无法被调用」的落点：调用方拿不到声明
// 就没有请求方法、没有路径、没有风险标签，请求构造不出来。
func (r *Registry) Capability(service, operation string) (Capability, error) {
	operations, found := r.capabilities[service]
	if !found {
		return Capability{}, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail("没有 Adapter 负责服务 " + service)
	}

	capability, found := operations[operation]
	if !found {
		return Capability{}, apperr.New(apperr.CodeCapabilityNotOffered).
			WithDetail(service + " 没有声明操作 " + operation)
	}
	return capability, nil
}

// Operations 按字典序返回某个服务下**全部被声明过的**操作名。
//
// 服务不存在时返回空切片而不是错误：调用方是审批页面的「仍然关闭的权限」
// 那一段（REQ-APPROVAL-001 AC4），它要回答的是「这次放行之外还有什么没给」。
// 认不出的服务在那里的答案是「一条也说不出来」，而不是让整个卷宗读不出来。
func (r *Registry) Operations(service string) []string {
	declared := r.capabilities[service]
	operations := make([]string, 0, len(declared))
	for operation := range declared {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	return operations
}

// Services 按字典序返回全部已注册的服务名。
func (r *Registry) Services() []string {
	services := make([]string, 0, len(r.adapters))
	for service := range r.adapters {
		services = append(services, service)
	}
	sort.Strings(services)
	return services
}

func validKind(kind Kind) bool {
	switch kind {
	case KindGitHub, KindCloudflare, KindModel, KindGenericHTTP:
		return true
	default:
		return false
	}
}
