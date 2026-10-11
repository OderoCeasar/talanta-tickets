package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
)

func errorIn(t *testing.T, rec *httptest.ResponseRecorder) errorPayload {
	t.Helper()
	var body struct {
		Error *errorPayload `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	require.NotNil(t, body.Error, rec.Body.String())
	return *body.Error
}

// The code table, the contract's ErrorCode enum and the table in API_NOTES are three copies
// of one list. This test makes them agree: same codes, same statuses.
func TestErrorCodesMatchContractAndNotes(t *testing.T) {
	doc := contracttest.Load(t).Doc()
	var enum []string
	for _, v := range doc.Components.Schemas["ErrorCode"].Value.Enum {
		enum = append(enum, v.(string))
	}
	var table []string
	for code := range statusByCode {
		table = append(table, string(code))
	}
	sort.Strings(enum)
	sort.Strings(table)
	assert.Equal(t, enum, table, "statusByCode and the ErrorCode enum in openapi.yaml must list the same codes")

	notes, err := os.ReadFile("../../../docs/API_NOTES.md")
	require.NoError(t, err)
	rows := regexp.MustCompile("(?m)^\\| `([A-Z_]+)` \\| (\\d{3}) \\|").FindAllStringSubmatch(string(notes), -1)
	require.NotEmpty(t, rows)
	fromNotes := map[string]int{}
	for _, m := range rows {
		status, _ := strconv.Atoi(m[2])
		fromNotes[m[1]] = status
	}
	for code, status := range statusByCode {
		assert.Equal(t, status, fromNotes[string(code)], "status of %s in docs/API_NOTES.md", code)
	}
	assert.Len(t, fromNotes, len(statusByCode), "docs/API_NOTES.md lists a code the API does not know")
}

func TestWriteErrorDomainError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), domain.ErrInvitationExpired)

	assert.Equal(t, http.StatusGone, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	// details is present and null, not missing.
	assert.JSONEq(t, `{"error":{"code":"INVITATION_EXPIRED","message":"This invitation has expired. Ask an admin to send it again.","details":null}}`, rec.Body.String())
}

func TestWriteErrorWrappedDomainErrorKeepsItsCode(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), fmt.Errorf("accept: %w", domain.ErrEmailAlreadyRegistered))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, domain.CodeEmailAlreadyRegistered, errorIn(t, rec).Code)
}

func TestWriteErrorValidationDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), domain.InvalidField("phone", "e164", "Bad phone."))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"VALIDATION_FAILED","message":"Some fields are not valid.","details":{"fields":[{"field":"phone","rule":"e164","message":"Bad phone."}]}}}`, rec.Body.String())
}

func TestWriteErrorRateLimitedSetsRetryAfter(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), domain.RateLimited(42))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "42", rec.Header().Get("Retry-After"))
	assert.JSONEq(t, `{"retry_after_seconds":42}`, mustJSON(t, errorIn(t, rec).Details))

	// Never zero: Retry-After must be at least a second.
	rec = httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), domain.RateLimited(0))
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}

// An unknown error must reach the client as a bare INTERNAL and reach the log in full, with
// the request ID that ties the two together.
func TestWriteErrorUnknownErrorIsLoggedAndNotLeaked(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))

	secret := `pq: password authentication failed for user "talanta" at /srv/app/store.go:42`
	h := requestID(requestLogger(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, fmt.Errorf("load user: %w", errors.New(secret)))
	})))

	req := httptest.NewRequest("GET", "/v1/anything", nil)
	req.Header.Set("X-Request-Id", "req-abc-12345")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.JSONEq(t, `{"error":{"code":"INTERNAL","message":"Something went wrong on our side.","details":null}}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "password authentication")
	assert.Equal(t, "req-abc-12345", rec.Header().Get("X-Request-Id"))

	var found bool
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		if entry["msg"] == "internal error" {
			found = true
			assert.Equal(t, "req-abc-12345", entry["request_id"])
			assert.Contains(t, entry["err"], "password authentication failed")
			assert.Equal(t, "ERROR", entry["level"])
		}
	}
	assert.True(t, found, "the real error was not logged: %s", logs.String())
}

func TestWriteErrorUnknownDomainCodeIsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), domain.E("MADE_UP", "secret detail"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, domain.CodeInternal, errorIn(t, rec).Code)
	assert.NotContains(t, rec.Body.String(), "secret detail")
}

func TestWriteErrorTimeoutIsInternal(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, httptest.NewRequest("GET", "/x", nil), fmt.Errorf("query: %w", context.DeadlineExceeded))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, domain.CodeInternal, errorIn(t, rec).Code)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
