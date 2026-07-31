package model

import "github.com/Runcoor/opendelo/internal/adapter/registry"

/*
 * 模型服务的能力声明（REQ-ADAPTER-004、PRD §18.3、假设 A-07）。
 *
 * 只有两件事被声明：**发起一次推理**与**列出可用模型**。
 * 账单、成员、Key 管理、组织设置**一项都没有声明** —— 未声明的操作
 * 在注册表里查不到，也就构造不出请求（REQ-ADAPTER-001 AC2）。
 * 这不是「以后再加」，是这个 Adapter 存在的前提：模型服务的凭据往往
 * 同时能改计费与签发新 Key，而 Agent 要的只是推理。
 */

// Provider 是模型服务厂商。两家的端点与请求形状不同，但受同一套约束。
type Provider string

const (
	// ProviderOpenAI 对应 OpenAI 的 Chat Completions。
	ProviderOpenAI Provider = "openai"
	// ProviderAnthropic 对应 Anthropic 的 Messages。
	ProviderAnthropic Provider = "anthropic"
)

// 两项能力（REQ-ADAPTER-004）。
const (
	OpCreateCompletion = "create_completion"
	OpReadModels       = "read_models"
)

const (
	schemaNoInput = `{"type":"object","additionalProperties":false}`

	schemaCompletion = `{"type":"object","required":["model","messages","max_tokens"],` +
		`"properties":{"model":{"type":"string"},"max_tokens":{"type":"integer"},` +
		`"messages":{"type":"array","items":{"type":"object"}},` +
		`"temperature":{"type":"number"}},"additionalProperties":false}`
)

// endpoints 是每家厂商的端点白名单。
//
// 一家一张表：把它们合成一张再按厂商挑，等于让「哪个端点属于哪家」
// 变成运行期判断，而它本来是编译期就定死的。
var endpoints = map[Provider]struct {
	completion string
	models     string
}{
	ProviderOpenAI:    {completion: "/chat/completions", models: "/models"},
	ProviderAnthropic: {completion: "/messages", models: "/models"},
}

// completionResponseFields 是推理响应允许返回的字段。
//
// 两家的形状不同，取并集：OpenAI 用 choices，Anthropic 用 content。
// 白名单之外的一切不返回，包括厂商将来新增的字段。
var completionResponseFields = []string{
	"id", "model", "choices", "content", "stop_reason", "finish_reason", "usage", "role",
}

// capabilities 返回该厂商的两项声明。
func capabilities(provider Provider) []registry.Capability {
	endpoint := endpoints[provider]

	return []registry.Capability{
		{
			Operation:    OpCreateCompletion,
			InputSchema:  schemaCompletion,
			MinimumScope: registry.MinimumScope{ResourceKeys: []string{"model"}, RequiresAccount: true},
			// 推理会花钱、会把上下文送出本机，但它不改变外部服务里的任何资源。
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           endpoint.completion,
			RedactionRules: []string{},
			ResponseFields: completionResponseFields,
			// 发出去的上下文收不回来，但也没有需要还原的资源状态。
			Rollback: registry.RollbackAutomatic,
			// 同一段提示重复发两次会产生两次计费，不能重试。
			Idempotency: registry.NonIdempotent,
			// 内容送到厂商那里就离开了本机（PRD §10.5 的对外通信因子）。
			Nature: registry.Nature{ExternalCommunication: true},
		},
		{
			Operation:      OpReadModels,
			InputSchema:    schemaNoInput,
			MinimumScope:   registry.MinimumScope{ResourceKeys: []string{"account"}, RequiresAccount: true},
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           endpoint.models,
			RedactionRules: []string{},
			ResponseFields: []string{"data", "id", "object", "owned_by", "created"},
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.Idempotent,
		},
	}
}

// price 是一个模型的单价，单位是每千 token 的微元（百万分之一元）。
//
// 写死在代码里而不是配置：价目表变了要有人来改这张表并跑一次测试，
// 比让一个过期的配置文件悄悄把估算算低了好。
type price struct {
	inputPerKilo  int64
	outputPerKilo int64
}

// prices 是已知模型的价目表。
//
// **不在表里的模型一律拒绝**：费用估算不出来就等于预算管不住，
// 而「算不出来就先跑一次」正是 Fail Closed 要挡的那种写法。
var prices = map[string]price{
	"gpt-4o":                    {inputPerKilo: 2500, outputPerKilo: 10000},
	"gpt-4o-mini":               {inputPerKilo: 150, outputPerKilo: 600},
	"claude-opus-4":             {inputPerKilo: 15000, outputPerKilo: 75000},
	"claude-sonnet-4":           {inputPerKilo: 3000, outputPerKilo: 15000},
	"claude-haiku-4-5-20251001": {inputPerKilo: 800, outputPerKilo: 4000},
}
