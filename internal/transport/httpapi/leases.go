package httpapi

import (
	"net/http"
	"time"

	"github.com/Runcoor/opendelo/internal/core/lease"
	"github.com/Runcoor/opendelo/internal/platform/apperr"
)

/*
 * Lease API：生效中的授权、提前到期、收回（PRD §27、REQ-LEASE-002/003）。
 *
 * 这三个端点合起来只能让授权变少：列出、缩短、收回。没有延长，也没有
 * 修改范围 —— core/lease 的 Manager 根本没有那样的方法（REQ-LEASE-004 AC1）。
 */

// shortenBody 是 POST /v1/leases/:id/shorten 的请求体。
type shortenBody struct {
	// ExpiresAt 是新的到期时刻，必须早于原有时刻（REQ-LEASE-002 AC3）。
	ExpiresAt string `json:"expires_at"`
}

// listLeases 返回生效中的 Lease（缝内侧的 Active leases）。
func (e *endpoints) listLeases(w http.ResponseWriter, r *http.Request) {
	limit, err := limitFrom(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	active, err := e.services.Leases.Active(r.Context(), limit)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	caller := callerFrom(r.Context())
	items := make([]LeaseView, 0, len(active))
	for _, issued := range active {
		if !caller.maySee(issued.AgentID) {
			continue
		}
		items = append(items, leaseView(issued))
	}
	writeJSON(w, r, e.logger, http.StatusOK, listEnvelope[LeaseView]{Items: items})
}

// shorten 提前一条 Lease 的到期时刻。
func (e *endpoints) shorten(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	var body shortenBody
	if err = decodeBody(r, &body); err != nil {
		e.fail(w, r, err)
		return
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		e.fail(w, r, apperr.Wrap(apperr.CodeInvalidRequest, err).
			WithDetail("expires_at 必须是 RFC3339 时刻"))
		return
	}

	if _, err = e.visibleLease(r, id); err != nil {
		e.fail(w, r, err)
		return
	}

	shortened, err := e.services.Leases.Shorten(r.Context(), id, expiresAt.UTC())
	if err != nil {
		e.fail(w, r, err)
		return
	}
	view := leaseView(shortened)
	e.publish(r, EventLease, view)
	writeJSON(w, r, e.logger, http.StatusOK, view)
}

// revoke 收回一条 Lease（REQ-LEASE-002）。
func (e *endpoints) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		e.fail(w, r, err)
		return
	}

	if _, err = e.visibleLease(r, id); err != nil {
		e.fail(w, r, err)
		return
	}

	revoked, err := e.services.Leases.Revoke(r.Context(), id)
	if err != nil {
		e.fail(w, r, err)
		return
	}
	view := leaseView(revoked)
	e.publish(r, EventLease, view)
	writeJSON(w, r, e.logger, http.StatusOK, view)
}

// visibleLease 读取一条 Lease，调用方看不到时与「不存在」给出同一个答复。
func (e *endpoints) visibleLease(r *http.Request, id string) (lease.Lease, error) {
	issued, err := e.services.Leases.ByID(r.Context(), id)
	if err != nil {
		return lease.Lease{}, err
	}
	if !callerFrom(r.Context()).maySee(issued.AgentID) {
		return lease.Lease{}, notFound("Lease", id)
	}
	return issued, nil
}
