package cloudflare

import "github.com/Runcoor/opendelo/internal/adapter/registry"

/*
 * Cloudflare 的能力声明（REQ-ADAPTER-003、PRD §18.2）。
 *
 * 七个 MVP 能力域下的八个可执行操作（「查询 DNS」一个域下有列表与单条两个操作，
 * 单条查询是 AC1「改动前先查当前值」要走的那个被声明过的端点），
 * 五项高风险操作**只声明不执行**（本期范围）。
 *
 * 与 GitHub 那张表的差别在两处，都来自 REQ-ADAPTER-003：
 *
 *  1. **删除 DNS 记录本身就是 high**（AC2）——它在 MVP 能力里，但不可逆，
 *     每次都要人工确认。
 *  2. **改动前必须先查当前值**（AC1）：审批页面要展示旧值，否则用户是在
 *     对一个自己看不见的东西点同意。这一条由 Preview 实现，不是靠声明。
 */

// Service 是本 Adapter 负责的服务名。
const Service = "cloudflare"

// 七个 MVP 能力域下的八个可执行操作（REQ-ADAPTER-003）。
const (
	OpReadZone        = "read_zone"
	OpReadDNSRecords  = "read_dns_records"
	OpReadDNSRecord   = "read_dns_record"
	OpCreateDNSRecord = "create_dns_record"
	OpUpdateDNSRecord = "update_dns_record"
	OpDeleteDNSRecord = "delete_dns_record"
	OpReadTunnel      = "read_tunnel"
	OpPurgeCache      = "purge_cache"
)

// 五项高风险操作：本期只声明风险，不实现执行。
const (
	OpDeleteZone          = "delete_zone"
	OpManageToken         = "manage_token"
	OpManageMember        = "manage_member"
	OpBulkUpdateDNS       = "bulk_update_dns"
	OpUpdateSecurityRules = "update_security_rules"
)

const (
	schemaNoInput = `{"type":"object","additionalProperties":false}`

	schemaDNSRecord = `{"type":"object","required":["type","name","content"],` +
		`"properties":{"type":{"type":"string"},"name":{"type":"string"},` +
		`"content":{"type":"string"},"ttl":{"type":"integer"},` +
		`"proxied":{"type":"boolean"}},"additionalProperties":false}`

	schemaPurgeCache = `{"type":"object",` +
		`"properties":{"files":{"type":"array","items":{"type":"string"}},` +
		`"purge_everything":{"type":"boolean"}},"additionalProperties":false}`
)

// dnsResponseFields 是 DNS 记录允许返回的字段。
//
// 不含 result：Cloudflare 的信封由 Adapter 在脱敏前拆掉，白名单管的是
// 信封里那个对象的字段（见 adapter.go 的 unwrap）。
//
// 不返回 zone_name 之外的账户信息：Agent 需要的是「这条记录现在指向哪里」，
// 不是这个账号下还有什么。
var dnsResponseFields = []string{
	"id", "type", "name", "content", "ttl", "proxied", "zone_id", "zone_name",
}

func zoneScope(extra ...string) registry.MinimumScope {
	keys := append([]string{"zone_id"}, extra...)
	return registry.MinimumScope{ResourceKeys: keys, RequiresAccount: true}
}

// capabilities 是全部十二项声明。
func capabilities() []registry.Capability {
	return []registry.Capability{
		{
			Operation:      OpReadZone,
			InputSchema:    schemaNoInput,
			MinimumScope:   zoneScope(),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/zones/{zone_id}",
			RedactionRules: []string{},
			ResponseFields: []string{"id", "name", "status", "paused", "type", "name_servers"},
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.Idempotent,
		},
		{
			Operation:      OpReadDNSRecords,
			InputSchema:    schemaNoInput,
			MinimumScope:   zoneScope(),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/zones/{zone_id}/dns_records",
			RedactionRules: []string{},
			ResponseFields: dnsResponseFields,
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.Idempotent,
		},
		{
			// 单条查询：AC1 的「改动前先查当前值」要走一个**被声明过**的端点，
			// 否则 Preview 就成了绕过端点白名单的那条路。
			Operation:      OpReadDNSRecord,
			InputSchema:    schemaNoInput,
			MinimumScope:   zoneScope("record_id"),
			RiskLabel:      registry.RiskLabelLow,
			Method:         "GET",
			Path:           "/zones/{zone_id}/dns_records/{record_id}",
			RedactionRules: []string{},
			ResponseFields: dnsResponseFields,
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.Idempotent,
		},
		{
			Operation:      OpCreateDNSRecord,
			InputSchema:    schemaDNSRecord,
			MinimumScope:   zoneScope(),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           "/zones/{zone_id}/dns_records",
			RedactionRules: []string{},
			ResponseFields: dnsResponseFields,
			// 建出来的记录可以删掉，但要人去做。
			Rollback:    registry.RollbackManual,
			Idempotency: registry.NonIdempotent,
		},
		{
			Operation:      OpUpdateDNSRecord,
			InputSchema:    schemaDNSRecord,
			MinimumScope:   zoneScope("record_id"),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "PUT",
			Path:           "/zones/{zone_id}/dns_records/{record_id}",
			RedactionRules: []string{},
			ResponseFields: dnsResponseFields,
			// 旧值由 Preview 查出来并记进审批，改回去是照着旧值再改一次。
			Rollback: registry.RollbackManual,
			// 同一个请求体重复发出去，结果与发一次相同。
			Idempotency: registry.Idempotent,
		},
		{
			Operation:    OpDeleteDNSRecord,
			InputSchema:  schemaNoInput,
			MinimumScope: zoneScope("record_id"),
			// AC2：删除 DNS 记录不可逆，每次都要人工确认。
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "DELETE",
			Path:           "/zones/{zone_id}/dns_records/{record_id}",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			// 旧值由 Preview 记进审批，重建是照着它再创建一条。
			Rollback:    registry.RollbackManual,
			Nature:      registry.Nature{Destructive: true},
			Idempotency: registry.NonIdempotent,
		},
		{
			Operation:    OpReadTunnel,
			InputSchema:  schemaNoInput,
			MinimumScope: registry.MinimumScope{ResourceKeys: []string{"account_id", "tunnel_id"}, RequiresAccount: true},
			RiskLabel:    registry.RiskLabelLow,
			Method:       "GET",
			Path:         "/accounts/{account_id}/cfd_tunnel/{tunnel_id}",
			// Tunnel 的响应带着连接凭证字段，但 tunnel_secret 与 credentials_file
			// 归一化后分别命中全局词表的 secret 与 credential，不需要再点名一次。
			RedactionRules: []string{},
			ResponseFields: []string{"id", "name", "status", "created_at"},
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.Idempotent,
		},
		{
			Operation:      OpPurgeCache,
			InputSchema:    schemaPurgeCache,
			MinimumScope:   zoneScope(),
			RiskLabel:      registry.RiskLabelMedium,
			Method:         "POST",
			Path:           "/zones/{zone_id}/purge_cache",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			// 缓存会自己回填，不需要人去恢复什么。
			Rollback:    registry.RollbackAutomatic,
			Idempotency: registry.Idempotent,
		},

		// ——— 五项高风险：只声明，不执行 ———

		{
			Operation:      OpDeleteZone,
			InputSchema:    schemaNoInput,
			MinimumScope:   zoneScope(),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "DELETE",
			Path:           "/zones/{zone_id}",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			Rollback:       registry.RollbackNone,
			Nature:         registry.Nature{Destructive: true},
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpManageToken,
			InputSchema:    schemaNoInput,
			MinimumScope:   registry.MinimumScope{ResourceKeys: []string{"token_id"}, RequiresAccount: true},
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/user/tokens/{token_id}",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			Rollback:       registry.RollbackNone,
			Nature:         registry.Nature{SecretAccess: true},
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpManageMember,
			InputSchema:    schemaNoInput,
			MinimumScope:   registry.MinimumScope{ResourceKeys: []string{"account_id", "member_id"}, RequiresAccount: true},
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/accounts/{account_id}/members/{member_id}",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			Rollback:       registry.RollbackManual,
			Nature:         registry.Nature{PermissionChange: true},
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:    OpBulkUpdateDNS,
			InputSchema:  schemaNoInput,
			MinimumScope: zoneScope(),
			// AC3：一次动多条记录就是大范围修改，永远是 high。
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PATCH",
			Path:           "/zones/{zone_id}/dns_records",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			Rollback:       registry.RollbackNone,
			Idempotency:    registry.NonIdempotent,
		},
		{
			Operation:      OpUpdateSecurityRules,
			InputSchema:    schemaNoInput,
			MinimumScope:   zoneScope("rule_id"),
			RiskLabel:      registry.RiskLabelHigh,
			Method:         "PUT",
			Path:           "/zones/{zone_id}/firewall/rules/{rule_id}",
			RedactionRules: []string{},
			ResponseFields: []string{"id"},
			Rollback:       registry.RollbackManual,
			Nature:         registry.Nature{PermissionChange: true},
			Idempotency:    registry.NonIdempotent,
		},
	}
}

// executable 是本期实现了执行的八个操作。
var executable = map[string]bool{
	OpReadZone:        true,
	OpReadDNSRecords:  true,
	OpReadDNSRecord:   true,
	OpCreateDNSRecord: true,
	OpUpdateDNSRecord: true,
	OpDeleteDNSRecord: true,
	OpReadTunnel:      true,
	OpPurgeCache:      true,
}

// changesBefore 是执行前必须先查当前值的操作（AC1）。
//
// 只有这两项会改掉一条已经存在的记录。创建不需要 —— 改之前那条记录还不存在。
var changesBefore = map[string]bool{
	OpUpdateDNSRecord: true,
	OpDeleteDNSRecord: true,
}
