package matcher

import (
	"sort"

	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Credential Matcher：把一次请求匹配到唯一身份（PRD §10.3、REQ-IDENT-002）。
 *
 * 顺序严格为：明确的项目绑定 → 明确的资源绑定 → 历史选择 → 唯一可用身份 →
 * 用户手动选择。**任何一级出现多个候选就停下来问**，不往下一级找，也不挑默认的那个：
 * 往下找等于用一条更弱的依据覆盖用户已经表达过的意思，挑默认等于替用户做主。
 */

// Request 是一次匹配的目标。
type Request struct {
	Service     string
	WorkspaceID string
	// ResourceKey 与 intent.Intent.ResourceKey 一致，是资源绑定的比较依据。
	ResourceKey string
}

// Inputs 是匹配所需的已加载数据（ADR-003：core 不读数据库）。
//
// MemoryIdentityIDs 传的是身份主键而不是 trust.Memory：core/trust 已经导入本包
// 取 Environment，反向导入会成环。命中的记忆由调用方筛好后把身份取出来传进来。
type Inputs struct {
	WorkspaceBindings []Binding
	ResourceBindings  []Binding
	MemoryIdentityIDs []string
	// Identities 是该服务下的全部候选身份。
	Identities []Identity
	// ManualSelection 是用户在审批时当场选定的身份主键，空表示尚未选择。
	// 它只在前四级给不出唯一答案时才被采用（第五级）。
	ManualSelection string
}

// Result 是一次匹配的结论。
//
// Level 必须与结果一并写入 Decision 与审计事件（REQ-IDENT-002 AC3）：
// 只知道匹配到了哪个身份，无法解释为什么是它。
type Result struct {
	Identity Identity
	Level    MatchLevel

	// Ambiguous 为真表示存在多个可用身份，必须询问用户。此时 Identity 为零值，
	// Candidates 是要显示在审批页面上的全部候选（AC2）。
	Ambiguous  bool
	Candidates []Identity

	// NeedsReview 为真表示匹配到的身份被标记为「需要检查」（REQ-IDENT-004）。
	// 匹配本身成立，但自动授权要暂停，由决策链路转为人工确认。
	NeedsReview bool
}

// Match 按 REQ-IDENT-002 的五级顺序匹配身份。
//
// 服务下一个候选身份都没有时返回错误而不是空结果：匹配不到身份的请求执行不了，
// 让它带着零值往下走只会把失败推迟到取凭据的那一步（Fail Closed）。
func Match(request Request, inputs Inputs) (Result, error) {
	if request.Service == "" {
		return Result{}, apperr.New(apperr.CodeInvalidRequest).WithDetail("匹配请求没有服务名")
	}

	available, err := availableIdentities(request.Service, inputs.Identities)
	if err != nil {
		return Result{}, err
	}
	if len(available) == 0 {
		return Result{}, apperr.New(apperr.CodeCredentialNotAuthorized).
			WithDetail("服务 " + request.Service + " 下没有可用身份")
	}

	// 五级顺序在这里是一个字面上的列表：调换两项就是调换匹配顺序，
	// 对应的用例会失败。
	levels := []struct {
		level      MatchLevel
		candidates []string
	}{
		{MatchWorkspaceBinding, workspaceCandidates(request, inputs.WorkspaceBindings)},
		{MatchResourceBinding, resourceCandidates(request, inputs.ResourceBindings)},
		{MatchTrustMemory, inputs.MemoryIdentityIDs},
		{MatchSoleIdentity, identityIDs(available)},
	}

	for _, current := range levels {
		matched := selectIdentities(current.candidates, available)
		switch len(matched) {
		case 0:
			continue
		case 1:
			return hit(matched[0], current.level), nil
		default:
			return manualOrAmbiguous(matched, inputs.ManualSelection), nil
		}
	}

	// 走不到这里：最后一级用的就是 available，而它已经确认非空。
	return Result{}, apperr.New(apperr.CodeInternal).WithDetail("匹配在五级之后仍未得出结论")
}

func hit(identity Identity, level MatchLevel) Result {
	return Result{
		Identity:    identity,
		Level:       level,
		NeedsReview: identity.Status == StatusNeedsReview,
	}
}

// manualOrAmbiguous 是第五级：用户已经在审批里选定的话就用那一个，
// 否则把全部候选交出去让用户选。
func manualOrAmbiguous(candidates []Identity, selection string) Result {
	if selection != "" {
		for _, candidate := range candidates {
			if candidate.ID == selection {
				return hit(candidate, MatchManualSelection)
			}
		}
	}
	return Result{Ambiguous: true, Candidates: candidates}
}

// availableIdentities 校验候选身份都属于该服务，并按主键排序使结果稳定。
//
// 服务不符的身份返回错误而不是过滤掉：那说明调用方加载错了数据，
// 静静丢掉它会让「为什么没匹配上」变成一个查不出来的问题。
func availableIdentities(service string, identities []Identity) ([]Identity, error) {
	available := make([]Identity, 0, len(identities))
	for _, identity := range identities {
		if identity.Service != service {
			return nil, apperr.New(apperr.CodeInternal).
				WithDetail("候选身份 " + identity.ID + " 不属于服务 " + service)
		}
		if identity.ID == "" {
			return nil, apperr.New(apperr.CodeInternal).WithDetail("候选身份缺少主键")
		}
		available = append(available, identity)
	}
	return available, nil
}

func workspaceCandidates(request Request, bindings []Binding) []string {
	if request.WorkspaceID == "" {
		return nil
	}

	var matched []string
	for _, binding := range bindings {
		if binding.Kind != BindingWorkspace {
			continue
		}
		if binding.Service == request.Service && binding.WorkspaceID == request.WorkspaceID {
			matched = append(matched, binding.IdentityID)
		}
	}
	return matched
}

func resourceCandidates(request Request, bindings []Binding) []string {
	if request.ResourceKey == "" {
		return nil
	}

	var matched []string
	for _, binding := range bindings {
		if binding.Kind != BindingResource {
			continue
		}
		if binding.Service == request.Service && binding.ResourceKey == request.ResourceKey {
			matched = append(matched, binding.IdentityID)
		}
	}
	return matched
}

func identityIDs(identities []Identity) []string {
	ids := make([]string, 0, len(identities))
	for _, identity := range identities {
		ids = append(ids, identity.ID)
	}
	return ids
}

// selectIdentities 把身份主键换成身份本身，顺带去重并丢掉指向未知身份的引用。
//
// 指向未知身份的绑定或记忆不算候选：那条引用要么已失效，要么指向别的服务，
// 两种情况都不该让它参与匹配。全部候选都指向未知身份时返回空，匹配继续往下一级走 ——
// 这不是「多个候选」，而是「这一级什么也没说」。
func selectIdentities(candidateIDs []string, available []Identity) []Identity {
	if len(candidateIDs) == 0 {
		return nil
	}

	byID := make(map[string]Identity, len(available))
	for _, identity := range available {
		byID[identity.ID] = identity
	}

	seen := make(map[string]bool, len(candidateIDs))
	matched := make([]Identity, 0, len(candidateIDs))
	for _, id := range candidateIDs {
		identity, present := byID[id]
		if !present || seen[id] {
			continue
		}
		seen[id] = true
		matched = append(matched, identity)
	}

	sort.Slice(matched, func(left, right int) bool {
		return matched[left].ID < matched[right].ID
	})
	return matched
}
