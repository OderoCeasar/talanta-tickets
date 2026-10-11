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

-- name: GetLatestOpenOTPCode :one
-- The newest code that has not been used or retired, whether or not it has run out of time,
-- so verify can tell "expired" from "never asked".
SELECT * FROM otp_codes
WHERE phone_e164 = $1 AND consumed_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: RetireOTPCodesForPhone :execrows
-- Asking for a new code kills every earlier one for the phone, so only one code is ever
-- guessable at a time. consumed_at doubles as "retired".
UPDATE otp_codes SET consumed_at = now()
WHERE phone_e164 = $1 AND consumed_at IS NULL;

-- name: ClaimOTPAttempt :one
-- Counts one verify attempt, right or wrong, before the code is compared. No row means the
-- attempts are used up (or the code is gone); parallel guesses cannot get more than
-- max_attempts tries between them because the count and the check are one statement.
UPDATE otp_codes SET attempts = attempts + 1
WHERE id = sqlc.arg(id) AND consumed_at IS NULL AND attempts < sqlc.arg(max_attempts)::int
RETURNING attempts;
