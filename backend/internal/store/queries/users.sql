-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByPhone :one
SELECT * FROM users WHERE phone_e164 = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE lower(email) = lower(sqlc.arg(email)::text);

-- name: UpsertFanByPhone :one
-- First OTP login creates the fan; later logins return the same row. The no-op update is what
-- makes RETURNING give back an existing row, and it leaves role and name alone so a gate
-- steward signing in by phone is not turned back into a fan.
INSERT INTO users (phone_e164, role)
VALUES ($1, 'FAN')
ON CONFLICT (phone_e164) DO UPDATE SET phone_e164 = EXCLUDED.phone_e164
RETURNING *;

-- name: CreateUser :one
INSERT INTO users (phone_e164, email, name, role, password_hash, totp_secret, totp_enabled)
VALUES (
    sqlc.narg(phone_e164), sqlc.narg(email), sqlc.narg(name), sqlc.arg(role),
    sqlc.narg(password_hash), sqlc.narg(totp_secret), sqlc.arg(totp_enabled)
)
RETURNING *;

-- name: UpdateUserName :one
UPDATE users SET name = sqlc.narg(name) WHERE id = sqlc.arg(id) RETURNING *;

-- name: SetUserPasswordHash :execrows
UPDATE users SET password_hash = sqlc.arg(password_hash) WHERE id = sqlc.arg(id);

-- name: SetUserTOTP :execrows
UPDATE users
SET totp_secret = sqlc.narg(totp_secret), totp_enabled = sqlc.arg(totp_enabled)
WHERE id = sqlc.arg(id);

-- name: CreateStaffUser :one
-- An organiser or admin: email and password, no phone. TOTP is off until the user turns it on.
INSERT INTO users (email, name, role, password_hash)
VALUES (lower(sqlc.arg(email)::text), sqlc.narg(name), sqlc.arg(role), sqlc.arg(password_hash)::text)
RETURNING *;

-- name: CountAdminUsers :one
-- Guards against removing or demoting the last admin.
SELECT count(*) FROM users WHERE role = 'ADMIN';

-- name: LockAdminBootstrap :exec
-- Serialises "is there an admin yet? no: create one" across API instances starting together.
-- A transaction-level advisory lock: it is released at commit or rollback, so a crashed
-- instance cannot leave it held. The key is fixed and used by nothing else.
SELECT pg_advisory_xact_lock(hashtextextended('talanta:bootstrap-admin', 0));

-- name: ListStaffUsers :many
-- Organisers and admins by name, then id so the order is total. Keyset paging: the caller
-- passes the last row's sort name and id.
SELECT id, name, email, role, totp_enabled, created_at,
       lower(coalesce(name, ''))::text AS sort_name
FROM users
WHERE role IN ('ORGANISER', 'ADMIN')
  AND (sqlc.narg(role)::text IS NULL OR role = sqlc.narg(role)::text)
  AND (sqlc.narg(after_id)::bigint IS NULL
       OR (lower(coalesce(name, '')), id) > (sqlc.narg(after_name)::text, sqlc.narg(after_id)::bigint))
ORDER BY lower(coalesce(name, '')), id
LIMIT sqlc.arg(page_size);

-- name: SetPendingTOTPSecret :execrows
-- Setup: store a secret that is not in force yet. No row means TOTP is already on, and the
-- live secret must not be replaced from under it.
UPDATE users SET totp_secret = sqlc.arg(totp_secret)::text
WHERE id = sqlc.arg(id) AND NOT totp_enabled;

-- name: EnableTOTP :execrows
-- Turns TOTP on only for the secret the code was checked against. No row means the secret
-- changed (setup was run again) or TOTP is already on.
UPDATE users SET totp_enabled = TRUE
WHERE id = sqlc.arg(id) AND NOT totp_enabled AND totp_secret = sqlc.arg(totp_secret)::text;
