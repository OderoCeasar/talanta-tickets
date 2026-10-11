-- name: CreateInvitation :one
-- Fails on invitations_live_email_key when the address already has a live invitation.
INSERT INTO invitations (email, role, token_hash, invited_by, expires_at)
VALUES (lower(sqlc.arg(email)::text), sqlc.arg(role), sqlc.arg(token_hash), sqlc.arg(invited_by), sqlc.arg(expires_at))
RETURNING *;

-- name: GetLiveInvitationByTokenHash :one
SELECT * FROM invitations
WHERE token_hash = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: ListInvitations :many
SELECT * FROM invitations
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_size) OFFSET sqlc.arg(page_offset);

-- name: CountInvitations :one
SELECT count(*) FROM invitations;

-- name: MarkInvitationAccepted :execrows
-- Zero rows means it was already accepted, revoked or has expired; two people opening the
-- same link cannot both create an account.
UPDATE invitations SET accepted_at = now()
WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: RevokeInvitation :execrows
UPDATE invitations SET revoked_at = now()
WHERE id = $1 AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: RevokeLiveInvitationsForEmail :execrows
-- Clears the way for a fresh invitation to an address whose old one lapsed or was lost.
UPDATE invitations SET revoked_at = now()
WHERE email = lower(sqlc.arg(email)::text) AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: GetInvitationByID :one
SELECT * FROM invitations WHERE id = $1;

-- name: GetInvitationByTokenHash :one
-- Any state: the caller tells expired (410) from revoked or accepted (404).
SELECT * FROM invitations WHERE token_hash = $1;

-- name: GetInvitationByTokenHashForUpdate :one
-- Locks the invitation row for the length of the accept transaction, so two people (or one
-- double click) accepting the same link run one after the other: the second sees
-- accepted_at set and creates nothing.
SELECT * FROM invitations WHERE token_hash = $1 FOR UPDATE;

-- name: GetOpenInvitationByEmail :one
-- The one invitation for an address that is neither accepted nor revoked (it may have lapsed).
SELECT * FROM invitations
WHERE email = lower(sqlc.arg(email)::text) AND accepted_at IS NULL AND revoked_at IS NULL;

-- name: ReissueInvitation :one
-- Resend: a new token and a new expiry on the same row, so the old link stops working. No
-- row means it was accepted or revoked in the meantime.
UPDATE invitations
SET token_hash = sqlc.arg(token_hash), expires_at = sqlc.arg(expires_at)
WHERE id = sqlc.arg(id) AND accepted_at IS NULL AND revoked_at IS NULL
RETURNING *;

-- name: ListInvitationsPage :many
-- Newest first by id (ids rise with creation), keyset paged. The status is not stored; the
-- CASE mirrors domain.InvitationStatusAt and must change with it.
SELECT sqlc.embed(i), u.name AS inviter_name, u.email AS inviter_email
FROM invitations i
JOIN users u ON u.id = i.invited_by
WHERE (sqlc.narg(before_id)::bigint IS NULL OR i.id < sqlc.narg(before_id)::bigint)
  AND (sqlc.narg(status)::text IS NULL OR sqlc.narg(status)::text = CASE
          WHEN i.accepted_at IS NOT NULL THEN 'ACCEPTED'
          WHEN i.revoked_at IS NOT NULL THEN 'REVOKED'
          WHEN i.expires_at <= sqlc.arg(now)::timestamptz THEN 'EXPIRED'
          ELSE 'PENDING'
      END)
ORDER BY i.id DESC
LIMIT sqlc.arg(page_size);
