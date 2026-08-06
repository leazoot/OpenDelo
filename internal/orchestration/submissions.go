package orchestration

import (
	"context"
	"encoding/json"
	"log/slog"

	adapters "github.com/Runcoor/opendelo/internal/adapter/registry"
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/intent"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/pipeline"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
	"github.com/Runcoor/opendelo/internal/platform/clock"
)

/*
 * 一条已落库的能力请求怎样进入决策链路。
 *
 * 三个接入面提交请求的方式各不相同 —— MCP 是工具调用，Web API 是 JSON 请求体，
 * Proxy 干脆不走这条路（它只认已签发的 Lease）。但**装配决策输入的办法只有一个**，
 * 就在这里：能力声明的性质、能力映射表、候选身份、Agent 与设备的信任等级。
 *
 * 写成一处而不是每个面各写一遍，理由不是省代码：`pipeline.Inputs` 少填一项不会
 * 报错，只会让风险算得偏低 —— 少了 `Facts.Destructive`，一次删除操作在风险引擎
 * 眼里就是一次普通的写。两份实现里迟早有一份漏掉其中一项，而漏掉的那一份
 * 在所有用例下都照常返回结果。
 *
 * 它落在组装根是依赖方向逼出来的：要同时看得见 `core/pipeline` 与
 * `adapter/registry`，而这两个包谁也不能 import 谁。
 */

// maxServiceIdentities 是一次匹配最多考虑的候选身份数。
//
// 与分页上限一致：不做无界查询。同一个服务下
// 配到两百个身份时该报错，而不是逐条扫下去。
const maxServiceIdentities = 200

// maxDeclarations 是一次装配最多考虑的 Adapter 声明数。
//
// 给个上限而不是无界查询。与分页上限一致：
// 一台机器上配到 200 个 Adapter 已经远超本产品的设想。
const maxDeclarations = 200

// changePreviews 在执行前查出旧值。由 adapter/registry 的 Exchange 实现。
//
// 定义成端口而不是直接持有 *adapters.Exchange：本包已经 import 了 registry，
// 但把整个 Exchange 拿进来意味着这里也能 Send —— 而这条路径上没有任何东西
// 应该发出一次**写**请求。接口里只有 Preview，越界因此是编译错误。
type changePreviews interface {
	Preview(ctx context.Context, request adapters.ExchangeRequest) (adapters.PreviewOutput, error)
}

// serviceIdentities 列出某个服务下的候选身份。
type serviceIdentities interface {
	IdentitiesForService(ctx context.Context, service string, limit int) ([]matcher.Identity, error)
}

// agentRecords 读取发起请求的 Agent。
//
// 只取 AgentByID 一个方法：认证已经在接入面完成，这里要的是信任等级与设备，
// 而不是再认一次。
type agentRecords interface {
	AgentByID(ctx context.Context, id string) (agentauth.Agent, error)
}

// deviceRecords 读取 Agent 所在的设备。
type deviceRecords interface {
	DeviceByID(ctx context.Context, id string) (agentauth.Device, error)
}

// arrivals 把一次刚做完的决策通知给已经打开的 Console（REQ-API-002 的 arrival 事件）。
//
// 定义成端口而不是直接持有事件流：那条流属于 Web API 那一面，而这里是
// 三个面共用的决策路径。落在这里是因为「缝前什么时候看得见」不该取决于请求
// 从哪个面进来 —— 原先只有 Web API 自己广播，Agent 走 MCP 或 Proxy 时缝前是静的，
// 而 Gate 的列表明确不轮询，于是那些请求在刷新页面之前根本不出现。
//
// 没有返回值：广播不出去的后果是界面晚一点刷新，不能拿一次真实的授权
// 去换一次界面更新。
type arrivals interface {
	Announce(ctx context.Context, result pipeline.Result)
}

// Submissions 把一条已落库的能力请求推进决策链路。
type Submissions struct {
	Pipeline     *pipeline.Pipeline
	Identities   serviceIdentities
	Agents       agentRecords
	Devices      deviceRecords
	Declarations adapters.DeclarationRepository
	Registry     *adapters.Registry
	// Previews 与 Requests 服务执行前的查勘：查出来的旧值落在请求那一行上。
	Previews changePreviews
	Requests pipeline.CapabilityRequestRepository
	// Arrivals 把结论通知给已打开的 Console。
	Arrivals arrivals
	Clock    clock.Clock
	// Logger 记查勘失败。查勘不是决策的一部分，失败只写日志 ——
	// 但**不能什么都不写**：那样一次每回都失败的查勘会安静地表现成
	// 「这个服务没有旧值可看」。
	Logger *slog.Logger
}

// New 校验依赖并构造。
//
// 缺任何一项都拒绝构造：一个装不齐输入的编排会照常给出结论，而那个结论
// 是按不完整的事实算出来的。
func New(built Submissions) (*Submissions, error) {
	missing := map[string]bool{
		"决策链路":         built.Pipeline == nil,
		"身份仓储":         built.Identities == nil,
		"Agent 仓储":     built.Agents == nil,
		"设备仓储":         built.Devices == nil,
		"Adapter 声明仓储": built.Declarations == nil,
		"Adapter 注册表":  built.Registry == nil,
		"查勘入口":         built.Previews == nil,
		"能力请求仓储":       built.Requests == nil,
		"到达通知":         built.Arrivals == nil,
		"时钟":           built.Clock == nil,
		"日志":           built.Logger == nil,
	}
	for name, absent := range missing {
		if absent {
			return nil, apperr.New(apperr.CodeInternal).WithDetail("请求编排缺少" + name)
		}
	}
	return &built, nil
}

// Decide 装配决策输入并跑完链路。
//
// 加载失败与「加载出来是空的」是两回事：前者返回错误，后者照常往下走。
// 服务下一个身份都没有、声明表里一条都没有，都是决策引擎该给出拒绝的情况，
// 在这里提前拦掉会让账本上少一条记录（REQ-AUDIT-001 AC1：无未审计路径）。
func (s *Submissions) Decide(
	ctx context.Context, request pipeline.CapabilityRequest,
) (pipeline.Result, error) {
	inputs, err := s.inputsFor(ctx, request)
	if err != nil {
		return pipeline.Result{}, err
	}
	result, err := s.Pipeline.Handle(ctx, inputs)
	if err != nil {
		return pipeline.Result{}, err
	}
	s.previewChange(ctx, result)
	// 查勘之后才通知：卷宗上的旧值是这一步查出来的，先播出去等于让
	// 界面先收到一份没有旧值的。
	s.Arrivals.Announce(ctx, result)
	return result, nil
}

// previewChange 在请求停下来等人时查出旧值（REQ-APPROVAL-001 AC4）。
//
// 四条限定各自的落点：
//
//   - **身份已匹配之后**：这一步在 Handle 之后，而 require_approval 这个结论
//     只有在身份匹配成功后才可能出现（匹配不出来的请求已经被拒了）。查勘因此
//     不可能发生在「还不知道该用谁的凭据」的时候。
//   - **只对已声明为只读的操作发出**：由 adapter/registry 按能力声明核对。
//   - **失败不阻塞决策**：结论已经写下、账本已经记了，这里没有返回值。
//   - **走既有的白名单与超时**：请求由 Adapter 自己按声明构造，本包给不出路径。
//
// **这里只有一道闸：结论是不是「停下来等人」。** 其余结论都没有卷宗要看，而查勘
// 是一次真实的出站请求，为一条已经拒掉的请求发出去没有任何人会读到它。
//
// 曾经在这里多加一道「有期望变更才查」：它看着像是在挡读操作，实际挡掉的是
// **删除** —— 删除没有期望变更，而它恰恰是最需要先看清当前值的那一种。
// 有没有旧值可查由 Adapter 的 PreviewSource 回答，那是唯一知道答案的地方。
func (s *Submissions) previewChange(ctx context.Context, result pipeline.Result) {
	if result.Decision.Verdict != decision.VerdictRequireApproval {
		return
	}

	preview, err := s.Previews.Preview(ctx, adapters.ExchangeRequest{
		Service:     result.Request.Service,
		Operation:   result.Request.Operation,
		IdentityID:  result.Decision.IdentityID,
		Resource:    scopeResource(result.Decision.ResolvedScope),
		Body:        []byte(result.Request.DesiredChange),
		OperationID: result.Request.OperationID,
	})
	if err != nil {
		s.Logger.WarnContext(ctx, "执行前查勘失败，卷宗上不显示旧值",
			"operation_id", result.Request.OperationID,
			"service", result.Request.Service,
			"operation", result.Request.Operation,
			"error", err.Error())
		return
	}
	if len(preview.Changes) == 0 {
		return
	}

	encoded, err := json.Marshal(preview.Changes)
	if err != nil {
		s.Logger.WarnContext(ctx, "查勘结果无法序列化，卷宗上不显示旧值",
			"operation_id", result.Request.OperationID, "error", err.Error())
		return
	}
	if err = s.Requests.SaveChangePreview(ctx, result.Request.ID, string(encoded),
		pipeline.StatusAwaitingApproval, s.Clock.Now()); err != nil {
		s.Logger.WarnContext(ctx, "查勘结果没有写进请求，卷宗上不显示旧值",
			"operation_id", result.Request.OperationID, "error", err.Error())
	}
}

// scopeResource 取出收敛后的 Scope 里的资源维度。
//
// 用 Scope 而不是请求里原样带来的 resource：查勘要打向的正是**这次授权覆盖的
// 那个资源**，而 Scope 是它唯一确定的写法（请求里的字段已经被意图解析过滤过一遍）。
// 解析不出来时返回 nil，Adapter 会因为路径占位符补不齐而拒绝构造请求。
func scopeResource(resolved string) map[string]string {
	var scope struct {
		Resource map[string]string `json:"resource"`
	}
	if err := json.Unmarshal([]byte(resolved), &scope); err != nil {
		return nil
	}
	return scope.Resource
}

func (s *Submissions) inputsFor(
	ctx context.Context, request pipeline.CapabilityRequest,
) (pipeline.Inputs, error) {
	// 注册表里没有这个操作时**不返回错误**，而是折成一个阻断交给决策引擎。
	// 直接返回错误的话这次拒绝不会留在账本上，而 REQ-AUDIT-001 AC1 要求
	// 无未审计路径。性质上的声明取零值 —— 反正结论已经是拒绝。
	capability, declared := s.Registry.Capability(request.Service, request.Operation)
	var blockers []decision.Blocker
	if declared != nil {
		blockers = append(blockers, decision.BlockerCapabilityNotOffered)
	}

	agent, err := s.Agents.AgentByID(ctx, request.AgentID)
	if err != nil {
		return pipeline.Inputs{}, err
	}
	device, err := s.Devices.DeviceByID(ctx, agent.DeviceID)
	if err != nil {
		return pipeline.Inputs{}, err
	}

	declarations, err := s.Declarations.EnabledDeclarations(ctx, maxDeclarations)
	if err != nil {
		return pipeline.Inputs{}, err
	}
	catalog, err := intent.NewCatalog(declarations)
	if err != nil {
		return pipeline.Inputs{}, err
	}
	identities, err := s.Identities.IdentitiesForService(
		ctx, request.Service, maxServiceIdentities)
	if err != nil {
		return pipeline.Inputs{}, err
	}

	// 查不到工具名说明数据库里的声明不认得这个操作。传空串下去：解析会以
	// capability_not_offered 拒绝，链路照常记一条审计。在这里直接返回错误反而
	// 会让这次拒绝不留痕迹。
	tool, _ := catalog.ToolFor(request.Service, request.Operation)

	return pipeline.Inputs{
		Request: request,
		Call: intent.Call{
			Tool:                tool,
			Resource:            request.Resource,
			DesiredChange:       request.DesiredChange,
			IdentityEnvironment: soleEnvironment(identities),
		},
		Catalog:     catalog,
		Identities:  identities,
		AgentTrust:  agent.TrustLevel,
		DeviceTrust: device.TrustStatus,
		Facts:       factsOf(capability),
		Blockers:    blockers,
	}, nil
}

// soleEnvironment 在服务下只有一个候选身份时返回它的环境标记。
//
// 环境判定发生在意图解析里，而身份要到下一步才匹配得出来 —— 偏偏 core/scope
// 要求两者一致。候选只有一个时匹配结果已经确定，把它的标记提前给出来是如实的。
//
// 候选不止一个时**不猜**：此时环境按资源命名规则判定，判不出就按生产处理，
// 与最终匹配到的身份不符则 Scope 收敛失败、请求被拒。那是就高不就低的一侧。
func soleEnvironment(identities []matcher.Identity) matcher.Environment {
	if len(identities) != 1 {
		return ""
	}
	return identities[0].Environment
}

// factsOf 取出 Adapter 对这个操作性质的声明。
//
// 风险等级不在这里算 —— 那是 core/risk 的事。
// 这里只是把声明搬过去。
func factsOf(capability adapters.Capability) pipeline.OperationFacts {
	return pipeline.OperationFacts{
		Destructive:           capability.Nature.Destructive,
		PermissionChange:      capability.Nature.PermissionChange,
		SecretAccess:          capability.Nature.SecretAccess,
		Billing:               capability.Nature.Billing,
		ExternalCommunication: capability.Nature.ExternalCommunication,
	}
}
