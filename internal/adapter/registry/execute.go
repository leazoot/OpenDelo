package registry

import (
	"context"
	"encoding/json"
	"net/url"

	"github.com/Runcoor/opendelo/internal/platform/secret"
)

/*
 * 统一的执行契约。
 *
 * 四个 Adapter 各有一个 Execute，形状互不相同：generichttp 多一个 Query，
 * model 多 Budget 与 Spent 并且额外返回用量。这在各自的包里是对的 —— 那些字段
 * 只有它们用得上。但组装根需要「拿到服务名就能执行」，而它**不能**碰这些形状：
 * `secret.Value` 只允许出现在 credential 与 adapter 两个包的签名中
 * （架构测试强制），所以做分发的那段代码
 * 只能待在 adapter 之下，也就是这里。
 *
 * 统一的办法是取并集而不是取交集：ExecuteInput 带上四个 Adapter 各自需要的字段，
 * 用不上的那个 Adapter 忽略它。取交集的话，model 的预算就得从别处传进去，
 * 而「预算从哪来」一旦有第二条路径，超预算就会从一个可以静态排除的问题
 * 变成一个要靠测试去追的问题。
 */

// ModelBudget 是模型调用的花费上限（REQ-ADAPTER-004）。
//
// 放在这里而不是引用 model 包的同名类型：那个包 import 本包，反过来就成环了。
// 两者的字段必须保持一致，model 侧的适配方法是唯一的转换处。
type ModelBudget struct {
	MaxCostMicros int64
	MaxRequests   int
}

// ModelUsage 是模型调用已经花掉的部分。字段含义同 model.Usage。
type ModelUsage struct {
	Requests     int
	InputTokens  int
	OutputTokens int
	CostMicros   int64
	Estimated    bool
}

// ExecuteInput 是一次执行的统一输入。
//
// Credential 是明文，只活到请求发出为止；调用方负责 Zero
type ExecuteInput struct {
	// Operation 是被声明过的操作名。未声明的操作在这里就被各 Adapter 拒绝。
	Operation string
	// Resource 是资源维度的取值，例如 owner、repo。model 用不上。
	Resource map[string]string
	// Query 是查询参数，只有 generichttp 用得上。
	Query url.Values
	// Input 是请求体。
	Input json.RawMessage
	// Budget 与 Spent 只有 model 用得上：其余 Adapter 的花费不按 token 计。
	Budget ModelBudget
	Spent  ModelUsage
	// Credential 是凭据明文。不需要凭据的操作可以是零值。
	Credential secret.Value
	// OperationID 随结果与错误一起回到账本。
	OperationID string
}

// ExecuteOutput 是一次执行的统一结果。
//
// Usage 只在模型 Adapter 上非零。它与 Result 分开，是因为两者的去向不同：
// Result 会返回给 Agent，Usage 只写进 Lease 与审计
// —— 混在一起迟早有人把用量也发给 Agent。
type ExecuteOutput struct {
	Result Result
	Usage  ModelUsage
}

// Executor 是「能被组装根驱动」的 Adapter。
//
// 嵌入 Adapter 而不是另起一套：一个能执行却没声明能力的类型不该存在，
// 嵌进来之后它在类型上就构造不出来了。
type Executor interface {
	Adapter
	// ExecuteCapability 执行一项已声明的操作。
	//
	// 实现必须在做任何事之前确认该操作已被声明：拿不到声明就没有方法、
	// 没有路径、没有风险标签，请求本身构造不出来（REQ-ADAPTER-001 AC2）。
	ExecuteCapability(ctx context.Context, input ExecuteInput) (ExecuteOutput, error)
}
