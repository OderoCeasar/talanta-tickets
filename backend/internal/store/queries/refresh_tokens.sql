-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetRefreshTokenByHash :one
SELECT * FROM refresh_tokens WHERE token_hash = $1;

-- name: MarkRefreshTokenUsed :execrows
-- Rotation claims the token. Zero rows means it was already used, revoked or has expired; if
-- it was already used the caller must revoke the family, because one of the two presenters
-- is a thief.
UPDATE refresh_tokens SET used_at = now()
WHERE id = $1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > now();

-- name: RevokeRefreshTokenFamily :execrows
UPDATE refresh_tokens SET revoked_at = now()
WHERE family_id = $1 AND revoked_at IS NULL;

-- name: RevokeRefreshTokensForUser :execrows
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: DeleteRefreshTokensExpiredBefore :execrows
DELETE FROM refresh_tokens WHERE expires_at < sqlc.arg(before);

-- name: LockRefreshTokenFamily :exec
-- Serialises every change to one sign-in's token chain: rotation, reuse detection and logout.
-- Without it a rotation could insert the next token just after a reuse was detected and the
-- family revoked, leaving a live token in a revoked family. Transaction-level advisory lock
-- keyed by the family id; released at commit or rollback.
SELECT pg_advisory_xact_lock(hashtextextended('talanta:refresh-family:' || sqlc.arg(family_id)::uuid::text, 0));

-- name: RevokeOtherRefreshTokenFamilies :execrows
-- Password change: sign out every other session of the user, keep the calling one.
UPDATE refresh_tokens SET revoked_at = now()
WHERE user_id = sqlc.arg(user_id) AND family_id <> sqlc.arg(keep_family_id) AND revoked_at IS NULL;
