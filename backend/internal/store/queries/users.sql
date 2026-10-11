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
