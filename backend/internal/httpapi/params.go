package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// PathID reads a positive int64 path parameter such as {invitationId}. A value that is not
// one is a 400, not a 404: the URL is malformed, not merely pointing at nothing.
func PathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, name), 10, 64)
	if err != nil || id < 1 {
		return 0, domain.InvalidField(name, "id", "Must be a positive whole number.")
	}
	return id, nil
}

// PageLimit reads ?limit= (1 to 100, default 20).
func PageLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return domain.DefaultPageSize, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > domain.MaxPageSize {
		return 0, domain.InvalidField("limit", "range", fmt.Sprintf("Must be a whole number from 1 to %d.", domain.MaxPageSize))
	}
	return n, nil
}

// maxCursorLength is the contract's bound on ?cursor=.
const maxCursorLength = 256

// DecodeCursor reads ?cursor= into dst. It reports false when no cursor was sent (first
// page). Cursors are opaque to clients: base64url of a small JSON value that only the list
// endpoint that issued it understands.
func DecodeCursor(r *http.Request, dst any) (present bool, err error) {
	raw := r.URL.Query().Get("cursor")
	if raw == "" {
		if r.URL.Query().Has("cursor") {
			return false, errBadCursor
		}
		return false, nil
	}
	if len(raw) > maxCursorLength {
		return false, errBadCursor
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return false, errBadCursor
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return false, errBadCursor
	}
	return true, nil
}

var errBadCursor = domain.InvalidField("cursor", "cursor", "Use the next_cursor value from the previous page.")

// EncodeCursor is the other half of DecodeCursor. A nil position means "no next page" and
// gives a nil cursor, which is sent as JSON null.
func EncodeCursor[T any](pos *T) *string {
	if pos == nil {
		return nil
	}
	b, err := json.Marshal(pos)
	if err != nil {
		// Cursor types are plain structs of strings and numbers; this cannot fail.
		panic(fmt.Sprintf("httpapi: encode cursor: %v", err))
	}
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

// QueryEnum reads an optional query value that must be one of allowed. Absent gives "".
func QueryEnum(r *http.Request, name string, allowed ...string) (string, error) {
	if !r.URL.Query().Has(name) {
		return "", nil
	}
	v := r.URL.Query().Get(name)
	for _, a := range allowed {
		if v == a {
			return v, nil
		}
	}
	return "", domain.InvalidField(name, "oneof", fmt.Sprintf("Must be one of: %s.", joinComma(allowed)))
}

func joinComma(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
