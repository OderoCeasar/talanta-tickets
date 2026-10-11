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
