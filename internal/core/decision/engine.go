package decision

import (
	"github.com/Runcoor/opendelo/internal/core/agentauth"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/core/scope"
)

/*
 * Decision Engine：七个分支按固定顺序求值，命中即返回（PRD §10.6、REQ-DECIDE-001）。
 *
 * 顺序写成一张字面上的表。调换任意两行就是调换求值顺序，对应的用例会失败 ——
 * 这条性质是 AC1 明确要求的，因为顺序本身就是安全语义：把「低风险自动放行」
 * 提到「高风险要确认」之前，一个被算成低风险的删除操作就会被直接放过去。
 *
 * 表里只有一个 return，auto_allow 从哪一行来一眼可查。
 */

// Mode 是自动化等级（PRD §11、REQ-DECIDE-003）。
//
// 模式是决策链路的输入参数，不是三份独立实现（ADR-011）：它只改变两个自动放行
// 分支的附加条件，分支表本身不动。认不出的模式一律拒绝，不落到某个
// 「看起来温和」的默认值上。
type Mode string

const (
	// ModeCautious 是 PRD §11.1 的谨慎模式，适合首次使用。
	ModeCautious Mode = "cautious"
	// ModeBalanced 是 PRD §11.2 的平衡模式，也是默认值。
	ModeBalanced Mode = "balanced"
	// ModeAutomatic 是 PRD §11.3 的自动模式，适合高信任个人环境。
	ModeAutomatic Mode = "automatic"
)

// modes 按严格程度从紧到松排列。Modes 返回它的副本供用例逐条核对。
var modes = []Mode{ModeCautious, ModeBalanced, ModeAutomatic}

// Modes 返回三种自动化等级，顺序为谨慎 → 平衡 → 自动。
// ValidMode 报告这个自动化等级是不是三个取值之一。
//
// 认不出的模式在决策链路上会被当成策略引擎异常并拒绝（Fail Closed）；
// 导出它是为了让配置入口在写库之前就挡下来，而不是等到下一次请求才发现。
func ValidMode(mode Mode) bool {
	for _, known := range Modes() {
		if known == mode {
			return true
		}
	}
	return false
}

func Modes() []Mode {
	return append([]Mode(nil), modes...)
}

/*
 * 三种模式在两个自动放行分支上的附加条件（PRD §11）。
 *
 * 每种模式只能在平衡模式的基础上**收紧或放开这两行**，动不了禁止列表、
 * 高风险、范围扩大与身份歧义 —— 那四条在分支表里，与模式无关。
 * 「不允许关闭所有高风险保护」因此不是一条要记得执行的规则，而是结构上做不到。
 */

// modeRule 是一种模式的附加条件。
type modeRule struct {
	// lowRisk 是第五分支（低风险）的附加条件。
	lowRisk func(Input) bool
	// learnedMedium 是第六分支（中风险且命中已学习授权）的附加条件。
	learnedMedium func(Input) bool
	// doubleConfirmation 为真表示高风险的确认要做两次（PRD §11.1）。
	doubleConfirmation bool
}

// cautiousAutoAllow 是谨慎模式唯一允许自动放行的形状：只读、开关打开、
// 不是新服务、身份没变（PRD §11.1 的四条询问规则的补集）。
func cautiousAutoAllow(i Input) bool {
	return !i.Write && i.ReadOnlyAutoAllow && !i.NewService && !i.IdentityChanged
}

var modeRules = map[Mode]modeRule{
	// 谨慎：所有新服务首次询问 · 所有写操作询问 · 身份变化询问 · 高风险二次确认 ·
	// 只读操作可选择自动允许（独立开关，默认关闭）。
	ModeCautious: {
		lowRisk:            cautiousAutoAllow,
		learnedMedium:      cautiousAutoAllow,
		doubleConfirmation: true,
	},

	// 平衡：低风险**读取**自动允许 · 完全匹配 Trust Memory 时自动允许。
	ModeBalanced: {
		lowRisk:       func(i Input) bool { return !i.Write },
		learnedMedium: func(Input) bool { return true },
	},

	// 自动：低风险自动执行（读写皆可），但新身份或新资源仍然询问。
	//
	// 写在「不是写操作**或**身份与资源都不是新的」而不是直接放开，是为了保住单调性：
	// 平衡模式放行的一切，自动模式必须也放行（REQ-DECIDE-003 AC6）。
	// 第六分支不需要额外条件 —— 一条覆盖本次 Scope 的已学习授权里就含着身份与资源，
	// 新身份或新资源根本不可能命中。
	ModeAutomatic: {
		lowRisk:       func(i Input) bool { return !i.Write || (!i.NewIdentity && !i.NewResource) },
		learnedMedium: func(Input) bool { return true },
	},
}

// Reason 是决策结论的原因码。
//
// 用码而不是句子：Console 按码做中英文，账本导出后也不会锁死语言
// （与 decisions.reason_code 一致）。每个结论都有原因（REQ-DECIDE-001 AC3）。
type Reason string

const (
	// ReasonFailClosed 不确定就拒绝，具体哪一种见 Outcome.Blocker。
	ReasonFailClosed Reason = "fail_closed"
	// ReasonForbidden 落在禁止列表里，具体哪一类见 Outcome.Forbidden。
	ReasonForbidden Reason = "forbidden"
	// ReasonHighRisk 高风险始终要人确认。
	ReasonHighRisk Reason = "high_risk"
	// ReasonBeyondLearnedScope 范围超出已学习的授权。
	ReasonBeyondLearnedScope Reason = "beyond_learned_scope"
	// ReasonIdentityAmbiguous 身份匹配不唯一，由用户当场选。
	ReasonIdentityAmbiguous Reason = "identity_ambiguous"
	// ReasonLowRisk 低风险，且自动化等级允许直接放行。
	ReasonLowRisk Reason = "low_risk"
	// ReasonTrustMemoryMatch 中风险且完全落在一条已学习的授权之内。
	ReasonTrustMemoryMatch Reason = "trust_memory_match"
	// ReasonActiveLease 完全落在一条**已经签发**的授权之内。
	//
	// 与 trust_memory_match 的区别在于依据：那一条靠的是用户学下来的规则，
	// 这一条靠的是用户当场为这个确切范围签出的那份授权本身。
	ReasonActiveLease Reason = "active_lease"
	// ReasonRequiresConfirmation 是默认分支：走到这里的一律要人确认。
	ReasonRequiresConfirmation Reason = "requires_confirmation"
)

// Grant 是一条已学习的授权（Trust Memory 收敛出的范围）。
type Grant struct {
	MemoryID string
	Scope    scope.Scope
	// AlwaysAsk 为真表示这条记忆记下的是「命中也仍然询问」
	// （trust.BehaviorAlwaysAsk，REQ-APPROVAL-002 AC5）。
	//
	// 记忆的行为必须跟着范围一起进来。只带范围的话，一条用户特意收紧成
	// 「始终问我」的记忆与一条「今后自动允许」的记忆在这里长得一模一样，
	// 而前者会因为「命中了已学习的授权」被自动放行 —— 与用户的选择正好相反。
	AlwaysAsk bool
}

// ActiveLease 是一条已经签发、仍然生效的授权。
//
// 调用方只把「还活着」的传进来：过期、用满、已收回的一律不进（决策引擎不做 I/O，
// 判不了这些）。范围比较仍然逐维进行 —— 活着不等于罩得住这一次。
type ActiveLease struct {
	LeaseID string
	Scope   scope.Scope
}

// Input 是一次决策的全部输入。
//
// 这里没有任何「期望结果」类的字段：请求方影响得了的只有「对哪个资源做哪个操作」，
// 而那两项在 Intent 与 Scope 两步里已经被能力声明限死。
type Input struct {
	Mode Mode

	AgentID    string
	AgentTrust agentauth.TrustLevel
	// Write 为真表示写操作，与 risk.Factors.Write 同源。
	Write bool

	Match      matcher.Result
	Scope      scope.Result
	Assessment risk.Assessment

	// ReadOnlyAutoAllow 是谨慎模式下的「只读操作自动允许」开关，默认关闭
	// （PRD §11.1）。另两种模式不看它 —— 它们的只读放行不需要开关。
	ReadOnlyAutoAllow bool
	// NewService 表示这个服务第一次被该 Agent 使用（谨慎模式：新服务首次询问）。
	NewService bool
	// IdentityChanged 表示这次匹配到的身份与上次不同（谨慎模式：身份变化询问）。
	IdentityChanged bool
	// NewIdentity 表示这个身份第一次被该 Agent 使用（自动模式：新身份仍然询问）。
	NewIdentity bool
	// NewResource 表示这个资源第一次出现（自动模式：新资源仍然询问）。
	NewResource bool

	// Learned 是与本次请求相关、且仍然有效的已学习授权。
	// 为空表示这个组合从未被批准过。
	Learned []Grant

	// Active 是与本次请求相关、且仍然生效的**已签发**授权（Lease）。
	//
	// 「允许到任务结束」签的正是这样一条：它不生成记忆，因此在 Learned 里
	// 找不到。少了这一项，同一会话里下一次同样的调用会被再问一遍人，
	// 而用户明明已经为这个确切范围点过头了（R-39）。
	Active []ActiveLease

	// Blockers 是调用方已经发现的阻断情况（本包观察不到的那五种）。
	Blockers []Blocker
}

// Outcome 是决策的结论。
type Outcome struct {
	Verdict             Verdict
	ApprovalRequirement ApprovalRequirement
	Reason              Reason

	// Confirmations 是这次结论需要的人工确认次数：不需要人确认时为 0，
	// 通常为 1，谨慎模式下的高风险为 2（PRD §11.1「高风险二次确认」）。
	//
	// 它不是 approval_requirement 的另一种写法：后者说的是「确认要多强」
	// （是否走 Passkey），前者说的是「要确认几次」。两者独立。
	Confirmations int

	// Blocker 只在 Reason 为 fail_closed 时非空。
	Blocker Blocker
	// Forbidden 只在 Reason 为 forbidden 时非空。
	Forbidden Forbidden
	// MatchedMemoryID 是命中的已学习授权，未命中时为空。
	MatchedMemoryID string
	// MatchedLeaseID 是命中的已签发授权，未命中时为空。
	//
	// 调用方**必须**复用它而不是另签一条：为同一次请求签出第二条授权，
	// 等于把一次人工确认换成了两份权限。
	MatchedLeaseID string
}

// branch 是七个分支中的一行。
type branch struct {
	reason  Reason
	hits    func(Input, bool) bool
	verdict Verdict
}

// branches 是 PRD §10.6 的第二到第七个分支。第一个分支（禁止列表）在 Decide 里
// 单独求值，因为它还要报出是哪一类。
//
// 第二个参数是「这次请求允许被自动放行吗」：身份被标记需要检查、资源指向不止一个
// 目标、未确认的 Agent 发起写操作，三者都不改变风险等级，但都不该自动放行。
// 它们只关掉自动放行的门，让结论落到默认分支上，而不是新增第八个分支。
var branches = []branch{
	{ReasonHighRisk, func(i Input, _ bool) bool {
		return i.Assessment.Level == risk.LevelHigh
	}, VerdictRequireApproval},

	{ReasonBeyondLearnedScope, func(i Input, _ bool) bool {
		return len(i.Learned) > 0 && i.matchedGrant() == nil
	}, VerdictRequireApproval},

	{ReasonIdentityAmbiguous, func(i Input, _ bool) bool {
		return i.Match.Ambiguous
	}, VerdictRequireApproval},

	// 已经签发的授权罩得住这一次 —— 放在身份歧义之后、低风险之前。
	//
	// **在高风险之后**：高风险永远要人确认，那句话里没有例外分支
	// （REQ-DECIDE-003、不可协商约束第 3 条）。一条 Lease 是「这一次可以」，
	// 不是「以后都不必问」，拿它去豁免高风险等于开了第一个例外。
	//
	// **在超出已学范围之后**：用户特意收紧过的记忆仍然先说话，方向是多问一次。
	//
	// **在身份歧义之后**：身份定不下来时，这次请求的 Scope 里那一维本身就不可信，
	// 拿它去比对任何授权都是在比一个猜出来的值。
	//
	// 仍然要过 automatable 那道门：未确认的 Agent 发起写操作、资源指向不止一个
	// 目标、身份被标记需要检查 —— 这三件事与「有没有授权」无关，各自照旧拦下。
	{ReasonActiveLease, func(i Input, automatable bool) bool {
		return automatable && i.Assessment.Level != risk.LevelHigh && i.matchedLease() != nil
	}, VerdictAutoAllow},

	{ReasonLowRisk, func(i Input, automatable bool) bool {
		return automatable && i.Assessment.Level == risk.LevelLow && i.rule().lowRisk(i)
	}, VerdictAutoAllow},

	{ReasonTrustMemoryMatch, func(i Input, automatable bool) bool {
		return automatable && i.Assessment.Level == risk.LevelMedium &&
			i.matchedGrant() != nil && i.rule().learnedMedium(i)
	}, VerdictAutoAllow},
}

// Decide 按 PRD §10.6 的顺序求值并给出结论。
//
// 不返回 error：决策引擎自己出错也必须给出一个结论，而那个结论只能是拒绝
// （REQ-DECIDE-002 AC2）。panic 被顶层 recover 后同样落到拒绝上。
func Decide(input Input) (outcome Outcome) {
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = failClosed(BlockerPolicyEngineFailure)
		}
	}()

	if blocker, blocked := input.blocked(); blocked {
		return failClosed(blocker)
	}

	// 分支一：禁止列表。永久拒绝，不产生审批项（REQ-DECIDE-004 AC1）。
	if category, forbidden := classifyForbidden(
		input.Scope.Scope.Service, input.Scope.Scope.Operation,
	); forbidden {
		return Outcome{
			Verdict:             VerdictDeny,
			ApprovalRequirement: ApprovalNone,
			Reason:              ReasonForbidden,
			Forbidden:           category,
		}
	}

	automatable := input.automatable()
	for _, current := range branches {
		if !current.hits(input, automatable) {
			continue
		}
		return input.conclude(current.verdict, current.reason)
	}

	// 分支七：其他。默认是要人确认，不存在默认放行路径（AC2）。
	return input.conclude(VerdictRequireApproval, ReasonRequiresConfirmation)
}

// rule 取本次决策所用模式的附加条件。模式已在 blocked 里校验过。
func (i Input) rule() modeRule {
	return modeRules[i.Mode]
}

// blocked 汇总十种 Fail Closed 情况。
func (i Input) blocked() (Blocker, bool) {
	if _, known := modeRules[i.Mode]; !known {
		return BlockerPolicyEngineFailure, true
	}
	for _, reported := range i.Blockers {
		// 认不出的阻断按策略引擎异常处理：调用方报了一件本包读不懂的事，
		// 那本身就说明装配出了问题，比忽略它安全。
		if !validBlocker(reported) {
			return BlockerPolicyEngineFailure, true
		}
	}
	if len(i.Blockers) > 0 {
		return i.Blockers[0], true
	}
	return i.selfDetected()
}

// automatable 报告这次请求是否允许走自动放行。
//
// 三个条件都不改变风险等级，改变的是「能不能不问人」：
//   - 身份被标记需要检查：外部 Scope 变过，自动授权暂停（REQ-IDENT-004）。
//   - 资源指向不止一个目标：不得猜测高影响资源（REQ-INTENT-002 AC1）。
//   - 未确认的 Agent 发起写操作：REQ-AGENT-002 AC2 明确不得 auto_allow。
func (i Input) automatable() bool {
	switch {
	case i.Match.NeedsReview,
		i.Scope.Ambiguous,
		i.Write && i.AgentTrust == agentauth.TrustUnverified,
		i.asksAlways():
		return false
	default:
		return true
	}
}

// asksAlways 报告有没有一条覆盖本次 Scope 的记忆要求「命中也仍然询问」。
//
// 扫全部而不是只看 matchedGrant 命中的那一条：同一个组合下可以既有一条
// 「今后自动允许」又有一条「始终要求确认」，此时用户后说的那句话是收紧。
// 两句里取严的那一句，是这一段与 Learn Without Expanding 同一个方向。
func (i Input) asksAlways() bool {
	for _, grant := range i.Learned {
		if grant.AlwaysAsk && grant.Scope.CoversIgnoringWindow(i.Scope.Scope) {
			return true
		}
	}
	return false
}

// matchedGrant 返回覆盖本次 Scope 的那条已学习授权，没有则为 nil。
//
// 「完全匹配」用 scope.CoversIgnoringWindow 判定：九个维度逐一比较，
// 任何一维超出即不算命中 —— 这是 Learn Without Expanding 在决策侧的落点。
// 时间窗口不参与比较，因为已学习的授权是过去某一刻记下的，而这次请求的窗口
// 从「现在」起算，两者本来就对不上；记忆自己有没有到期由匹配它的那一步判定。
func (i Input) matchedGrant() *Grant {
	for index := range i.Learned {
		if i.Learned[index].Scope.CoversIgnoringWindow(i.Scope.Scope) {
			return &i.Learned[index]
		}
	}
	return nil
}

// matchedLease 找一条完全罩得住本次请求的已签发授权。
//
// 与 matchedGrant 同样用 CoversIgnoringWindow：十个维度里九个逐一比较，
// 任何一维超出即不算命中。时间窗口不参与 —— 授权到没到期由调用方在装配
// Active 时判定，这里再判一次就有了第二个答案。
//
// 命中不等于这次一定发得出去：真正的计量发生在执行前的那一句 Use 上，
// 那里会再拒一次已过期或已用满的。多一道判断，方向都是拒绝。
func (i Input) matchedLease() *ActiveLease {
	for index := range i.Active {
		if i.Active[index].Scope.CoversIgnoringWindow(i.Scope.Scope) {
			return &i.Active[index]
		}
	}
	return nil
}

// conclude 把结论补齐：确认强度与命中的记忆。
func (i Input) conclude(verdict Verdict, reason Reason) Outcome {
	outcome := Outcome{
		Verdict:             verdict,
		ApprovalRequirement: ApprovalNone,
		Reason:              reason,
	}
	if matched := i.matchedGrant(); matched != nil {
		outcome.MatchedMemoryID = matched.MemoryID
	}
	// 只在真的据此放行时才报出来：其余分支上「碰巧有一条 Lease 罩得住」
	// 不是这次结论的依据，报出去会让调用方以为可以复用它。
	if reason == ReasonActiveLease {
		if matched := i.matchedLease(); matched != nil {
			outcome.MatchedLeaseID = matched.LeaseID
		}
	}
	if verdict != VerdictRequireApproval {
		return outcome
	}

	// 高风险的确认要走强认证（REQ-APPROVAL-005），谨慎模式下还要确认两次。
	outcome.ApprovalRequirement = ApprovalStandard
	if i.Assessment.Level == risk.LevelHigh {
		outcome.ApprovalRequirement = ApprovalStrongAuth
	}
	outcome.Confirmations = RequiredConfirmations(i.Mode, i.Assessment.Level)
	return outcome
}

// RequiredConfirmations 报告这个模式与风险等级下放行需要几次人工确认。
//
// 导出是因为审批落地那一步要重算一次：决策记录里没有存这个数字，而
// 「谨慎模式的高风险要点两次头」这条规则只能有一个答案。
// 认不出的模式按最严格处理 —— 模式读不出来时少要一次确认就是悄悄放宽。
func RequiredConfirmations(mode Mode, level risk.Level) int {
	if level != risk.LevelHigh {
		return 1
	}
	rule, known := modeRules[mode]
	if !known || rule.doubleConfirmation {
		return 2
	}
	return 1
}

func failClosed(blocker Blocker) Outcome {
	return Outcome{
		Verdict:             VerdictDeny,
		ApprovalRequirement: ApprovalNone,
		Reason:              ReasonFailClosed,
		Blocker:             blocker,
	}
}
