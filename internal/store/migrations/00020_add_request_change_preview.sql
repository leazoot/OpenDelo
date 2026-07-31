-- 00020 · Record the pre-execution preview on a capability request.
--
-- Change: add change_preview to capability_requests. It holds the JSON array of
-- {resource, field, before, after} that the adapter read from the service before
-- anyone approved, so the Access Folio can answer "what is this value right now"
-- (REQ-APPROVAL-001 AC4). Until now the folio had to say the current value had
-- never been queried, because nothing on the decision path ever called Preview.
--
-- The column is nullable on purpose. NULL means "not queried" — an empty array
-- would mean "queried, nothing to show", and the folio says different things for
-- the two. Neither is invented from the request body.
--
-- Breaking: no. Existing rows keep NULL, which reads back as the state they were
-- already in. No data is rewritten and no constraint is tightened.
--
-- Rollback: Down drops the column. The preview is a cache of a value that lives
-- in the external service, not a record of what happened here — what happened is
-- in audit_events, and that table is untouched.

-- +goose Up
ALTER TABLE capability_requests
    ADD COLUMN change_preview TEXT CHECK (change_preview IS NULL OR json_valid(change_preview));

-- +goose Down
ALTER TABLE capability_requests DROP COLUMN change_preview;
