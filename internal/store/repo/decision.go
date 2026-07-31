package repo

import (
	"context"

	"github.com/Runcoor/opendelo/internal/core/decision"
	"github.com/Runcoor/opendelo/internal/core/matcher"
	"github.com/Runcoor/opendelo/internal/core/risk"
	"github.com/Runcoor/opendelo/internal/store"
	"github.com/Runcoor/opendelo/internal/store/queries"
)

// Decisions 是 decision.DecisionRepository 的 SQLite 实现。
//
// 只有写入与读取。没有 Update 与 Delete 不是遗漏：决策是账本的事实，
// 事后修改会让「当时为什么放行」这句话失去意义。
type Decisions struct {
	read  *queries.Queries
	write *queries.Queries
}

var _ decision.DecisionRepository = (*Decisions)(nil)

// NewDecisions 绑定到已迁移的数据库。
func NewDecisions(db *store.DB) *Decisions {
	return &Decisions{read: queries.New(db.Reader()), write: queries.New(db.Writer())}
}

func (d *Decisions) CreateDecision(
	ctx context.Context, record decision.Decision,
) (decision.Decision, error) {
	row, err := d.write.CreateDecision(ctx, queries.CreateDecisionParams{
		ID:                  record.ID,
		CapabilityRequestID: record.CapabilityRequestID,
		Verdict:             string(record.Verdict),
		RiskLevel:           string(record.RiskLevel),
		RiskFactors:         record.RiskFactors,
		IdentityID:          optionalText(record.IdentityID),
		MatchLevel:          optionalText(string(record.MatchLevel)),
		ResolvedScope:       record.ResolvedScope,
		ApprovalRequirement: string(record.ApprovalRequirement),
		ReasonCode:          record.ReasonCode,
		TrustMemoryID:       optionalText(record.TrustMemoryID),
		CreatedAt:           encodeTime(record.CreatedAt),
	})
	if err != nil {
		return decision.Decision{}, writeError(err, "写入决策 "+record.ID+" 失败")
	}
	return toDecision(row)
}

func (d *Decisions) DecisionByID(ctx context.Context, id string) (decision.Decision, error) {
	row, err := d.read.GetDecisionByID(ctx, id)
	if err != nil {
		return decision.Decision{}, readError(err, "读取决策 "+id+" 失败")
	}
	return toDecision(row)
}

func (d *Decisions) DecisionByRequestID(
	ctx context.Context, requestID string,
) (decision.Decision, error) {
	row, err := d.read.GetDecisionByCapabilityRequestID(ctx, requestID)
	if err != nil {
		return decision.Decision{}, readError(err, "读取能力请求 "+requestID+" 的决策失败")
	}
	return toDecision(row)
}

func toDecision(row queries.Decision) (decision.Decision, error) {
	createdAt, err := decodeTime("decisions.created_at", row.CreatedAt)
	if err != nil {
		return decision.Decision{}, err
	}

	return decision.Decision{
		ID:                  row.ID,
		CapabilityRequestID: row.CapabilityRequestID,
		Verdict:             decision.Verdict(row.Verdict),
		RiskLevel:           risk.Level(row.RiskLevel),
		RiskFactors:         row.RiskFactors,
		IdentityID:          row.IdentityID.String,
		MatchLevel:          matcher.MatchLevel(row.MatchLevel.String),
		ResolvedScope:       row.ResolvedScope,
		ApprovalRequirement: decision.ApprovalRequirement(row.ApprovalRequirement),
		ReasonCode:          row.ReasonCode,
		TrustMemoryID:       row.TrustMemoryID.String,
		CreatedAt:           createdAt,
	}, nil
}
