package cli

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Runcoor/opendelo/internal/cli/gatewayclient"
)

/*
 * opendelo connections / leases / audit（PRD §7.3 的 P1 命令）。
 *
 * 三条都是只读的，都只做一件事：把网关返回的视图排成列。
 *
 * 显示的字段全部来自 API 的响应视图，本文件不做任何二次计算 ——
 * 「还剩几次」「过期没有」这类判断在网关侧已经有了答案，在这里再算一遍
 * 就会出现两个可能不一致的答案。
 *
 * 凭据不在这条路上：三个视图里没有任何字段能放下一份明文（REQ-CRED-001）。
 */

// emptyCell 让空字段在表格里可见。留空会让「没有值」与「上一列串位」看起来一样。
const emptyCell = "—"

func runConnections(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("connections", "列出已连接的身份", options)
	configDir := configDirFlag(set)
	limit := set.Int("limit", gatewayclient.DefaultLimit, "最多列出多少条")

	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}

	address, sessionToken, err := gatewayEndpoint(*configDir, options)
	if err != nil {
		return err
	}

	identities, err := gatewayclient.Identities(ctx, address, sessionToken, *limit)
	if err != nil {
		return err
	}
	if len(identities.Items) == 0 {
		fmt.Fprintln(options.Stdout, "还没有连接任何身份。用 opendelo connect 建立一个。")
		return nil
	}

	table := newTable(options.Stdout, "ID", "服务", "账号", "环境", "默认", "状态")
	for _, identity := range identities.Items {
		writeRow(table, identity.ID, identity.Service, identity.AccountLabel,
			identity.Environment, yesNo(identity.IsDefault), identity.Status)
	}
	return finish(table, options.Stdout, identities.NextCursor)
}

func runLeases(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("leases", "列出生效中的 Lease", options)
	configDir := configDirFlag(set)
	limit := set.Int("limit", gatewayclient.DefaultLimit, "最多列出多少条")

	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}

	address, sessionToken, err := gatewayEndpoint(*configDir, options)
	if err != nil {
		return err
	}

	leases, err := gatewayclient.Leases(ctx, address, sessionToken, *limit)
	if err != nil {
		return err
	}
	if len(leases.Items) == 0 {
		fmt.Fprintln(options.Stdout, "当前没有生效中的 Lease。")
		return nil
	}

	table := newTable(options.Stdout, "ID", "服务", "状态", "已用", "到期")
	for _, issued := range leases.Items {
		writeRow(table, issued.ID, issued.Service, issued.Status,
			usage(issued.UsedRequests, issued.RequestLimit), issued.ExpiresAt)
	}
	return finish(table, options.Stdout, leases.NextCursor)
}

func runAudit(ctx context.Context, args []string, options Options) error {
	set := newFlagSet("audit", "列出账本记录，按时间倒序", options)
	configDir := configDirFlag(set)
	limit := set.Int("limit", gatewayclient.DefaultLimit, "最多列出多少条")
	agent := set.String("agent", "", "只看某个 Agent（与 --service 二选一）")
	service := set.String("service", "", "只看某个服务（与 --agent 二选一）")

	proceed, err := parse(set, args, options)
	if err != nil || !proceed {
		return err
	}

	address, sessionToken, err := gatewayEndpoint(*configDir, options)
	if err != nil {
		return err
	}

	events, err := gatewayclient.AuditEvents(ctx, address, sessionToken,
		gatewayclient.AuditFilter{AgentID: *agent, Service: *service, Limit: *limit})
	if err != nil {
		return err
	}
	if len(events.Items) == 0 {
		fmt.Fprintln(options.Stdout, "账本里还没有符合条件的记录。")
		return nil
	}

	table := newTable(options.Stdout, "时间", "类型", "服务", "操作", "结果", "OPERATION ID")
	for _, event := range events.Items {
		writeRow(table, event.CreatedAt, event.Type, event.Service,
			event.Operation, event.Outcome, event.OperationID)
	}
	return finish(table, options.Stdout, events.NextCursor)
}

// newTable 起一个对齐的表格。
func newTable(out io.Writer, headers ...string) *tabwriter.Writer {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	writeRow(table, headers...)
	return table
}

func writeRow(table *tabwriter.Writer, cells ...string) {
	filled := make([]string, len(cells))
	for index, cell := range cells {
		filled[index] = cell
		if strings.TrimSpace(cell) == "" {
			filled[index] = emptyCell
		}
	}
	fmt.Fprintln(table, strings.Join(filled, "\t"))
}

// finish 刷出表格，并在还有下一页时说明这一点。
//
// 不说的话，用户看到的是一份看起来完整、实际被截断的清单 ——
// 那比少列几条更糟。
func finish(table *tabwriter.Writer, out io.Writer, nextCursor string) error {
	if err := table.Flush(); err != nil {
		return err
	}
	if nextCursor != "" {
		fmt.Fprintln(out, "\n还有更多记录，用 --limit 调整条数。")
	}
	return nil
}

// usage 把「已用次数 / 上限」排成一格。上限为空表示不限次数。
func usage(used int, limit *int) string {
	if limit == nil {
		return strconv.Itoa(used) + "/不限"
	}
	return strconv.Itoa(used) + "/" + strconv.Itoa(*limit)
}

func yesNo(value bool) string {
	if value {
		return "是"
	}
	return "否"
}
