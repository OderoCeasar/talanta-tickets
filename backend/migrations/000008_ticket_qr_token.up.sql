-- The signed QR text of a ticket ("TLT1.<payload>.<signature>", docs/TICKET_FORMAT.md) is
-- stored when the ticket is issued, not signed again on every read.
--
-- Why: tickets record the key that signed them (key_id) so the signing key can be rotated.
-- Once a key is retired the server keeps only its public half, which can check an old
-- ticket but cannot produce its signature again. A ticket must still be shown to its owner
-- after a rotation, so what was signed at issue is kept.
--
-- No default, like the columns added in 000005: nothing has written to tickets yet, and if
-- that is wrong this migration should stop rather than invent a token nobody signed.
ALTER TABLE tickets
    ADD COLUMN qr_token TEXT NOT NULL
        CONSTRAINT tickets_qr_token_check CHECK (qr_token ~ '^TLT1\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$');
