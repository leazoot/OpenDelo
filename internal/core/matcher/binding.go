package matcher

import "time"

// BindingKind 是显式绑定的种类（REQ-IDENT-002 的前两级）。
type BindingKind string

const (
	// BindingWorkspace 是用户为某个项目建立的绑定。
	BindingWorkspace BindingKind = "workspace"
	// BindingResource 是用户为某个资源建立的绑定。
	BindingResource BindingKind = "resource"
)

// Binding 是用户在 Identities 页面显式建立的绑定（REQ-IDENT-003）。
//
// 它与 Trust Memory 的区别在于来源：绑定是用户直接指定的，记忆是从历史选择里
// 学来的。匹配顺序因此把绑定排在记忆之前 —— 用户说过的话优先于系统学到的。
type Binding struct {
	ID      string
	Kind    BindingKind
	Service string
	// WorkspaceID 在 Kind 为 workspace 时非空。
	WorkspaceID string
	// ResourceKey 在 Kind 为 resource 时非空，取值与 intent.Intent.ResourceKey 一致。
	ResourceKey string
	IdentityID  string
	CreatedAt   time.Time
}
