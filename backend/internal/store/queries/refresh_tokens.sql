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
