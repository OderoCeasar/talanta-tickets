-- name: CreateOTPCode :one
INSERT INTO otp_codes (phone_e164, code_hash, expires_at, request_ip)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetLatestLiveOTPCode :one
-- Only the newest code counts: asking for a second code retires the first.
SELECT * FROM otp_codes
WHERE phone_e164 = $1 AND consumed_at IS NULL AND expires_at > now()
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: IncrementOTPAttempts :one
UPDATE otp_codes SET attempts = attempts + 1 WHERE id = $1 RETURNING attempts;

-- name: ConsumeOTPCode :execrows
-- Zero rows means the code was already used or has lapsed; two requests racing with the same
-- code cannot both win.
UPDATE otp_codes SET consumed_at = now()
WHERE id = $1 AND consumed_at IS NULL AND expires_at > now();

-- name: CountOTPCodesForPhoneSince :one
SELECT count(*) FROM otp_codes WHERE phone_e164 = $1 AND created_at >= sqlc.arg(since);

-- name: CountOTPCodesForIPSince :one
SELECT count(*) FROM otp_codes WHERE request_ip = $1 AND created_at >= sqlc.arg(since);

-- name: DeleteOTPCodesCreatedBefore :execrows
DELETE FROM otp_codes WHERE created_at < sqlc.arg(before);
