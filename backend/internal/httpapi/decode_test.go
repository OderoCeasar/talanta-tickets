package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

type sampleBody struct {
	Email string   `json:"email" validate:"required,email"`
	Code  string   `json:"code" validate:"required,len=6,number"`
	Count int      `json:"count" validate:"gte=0,lte=10"`
	Tags  []string `json:"tags" validate:"max=2,dive,min=2"`
	Inner struct {
		Name string `json:"name" validate:"required"`
	} `json:"inner"`
}

func jsonRequest(body string) *http.Request {
	req := httptest.NewRequest("POST", "/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func fieldsOf(t *testing.T, err error) []domain.FieldError {
	t.Helper()
	require.Error(t, err)
	require.Equal(t, domain.CodeValidationFailed, domain.CodeOf(err), "%v", err)
	var de *domain.Error
	require.ErrorAs(t, err, &de)
	details, ok := de.Details.(domain.ValidationDetails)
	require.True(t, ok)
	return details.Fields
}

func TestDecodeJSONGood(t *testing.T) {
	var body sampleBody
	err := DecodeJSON(jsonRequest(`{"email":"a@b.co","code":"012345","count":3,"tags":["ab"],"inner":{"name":"x"}}`), &body)
	require.NoError(t, err)
	assert.Equal(t, "a@b.co", body.Email)
	assert.Equal(t, "012345", body.Code)
}

func TestDecodeJSONCharsetIsAccepted(t *testing.T) {
	req := jsonRequest(`{"email":"a@b.co","code":"012345","inner":{"name":"x"}}`)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	var body sampleBody
	require.NoError(t, DecodeJSON(req, &body))
}

func TestDecodeJSONReportsEveryBadFieldByItsJSONPath(t *testing.T) {
	var body sampleBody
	err := DecodeJSON(jsonRequest(`{"email":"nope","code":"12a456","count":11,"tags":["ab","c","de"],"inner":{}}`), &body)

	got := map[string]string{}
	for _, f := range fieldsOf(t, err) {
		got[f.Field] = f.Rule
		assert.NotEmpty(t, f.Message)
	}
	assert.Equal(t, map[string]string{
		"email":      "email",
		"code":       "number",
		"count":      "lte",
		"tags":       "max",
		"inner.name": "required",
	}, got)
}

func TestDecodeJSONNestedListIndex(t *testing.T) {
	var body sampleBody
	err := DecodeJSON(jsonRequest(`{"email":"a@b.co","code":"123456","tags":["ab","c"],"inner":{"name":"x"}}`), &body)
	fields := fieldsOf(t, err)
	require.Len(t, fields, 1)
	assert.Equal(t, "tags[1]", fields[0].Field)
	assert.Equal(t, "min", fields[0].Rule)
}

func TestDecodeJSONRefusals(t *testing.T) {
	cases := []struct {
		name, contentType, body string
		field, rule             string
	}{
		{"unknown field", "application/json", `{"email":"a@b.co","code":"123456","inner":{"name":"x"},"is_admin":true}`, "is_admin", "unknown"},
		{"malformed", "application/json", `{"email":`, "body", "json"},
		{"not json at all", "application/json", `hello`, "body", "json"},
		{"empty", "application/json", ``, "body", "required"},
		{"wrong type", "application/json", `{"email":42}`, "email", "type"},
		{"wrong nested type", "application/json", `{"inner":{"name":[1]}}`, "inner.name", "type"},
		{"array instead of object", "application/json", `[1,2]`, "body", "type"},
		{"two values", "application/json", `{"email":"a@b.co","code":"123456","inner":{"name":"x"}} {}`, "body", "json"},
		{"form encoded", "application/x-www-form-urlencoded", `email=a@b.co`, "Content-Type", "content_type"},
		{"text/plain", "text/plain", `{"email":"a@b.co"}`, "Content-Type", "content_type"},
		{"no content type", "", `{"email":"a@b.co"}`, "Content-Type", "content_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/x", strings.NewReader(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}
			var body sampleBody
			fields := fieldsOf(t, DecodeJSON(req, &body))
			require.Len(t, fields, 1)
			assert.Equal(t, tc.field, fields[0].Field)
			assert.Equal(t, tc.rule, fields[0].Rule)
		})
	}
}

func TestBodyLimitAnswers413(t *testing.T) {
	handler := bodyLimit(64)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body sampleBody
		if err := DecodeJSON(r, &body); err != nil {
			WriteError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	big := `{"email":"` + strings.Repeat("a", 200) + `@b.co"}`

	t.Run("declared length over the limit", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, jsonRequest(big))
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Equal(t, domain.CodePayloadTooLarge, errorIn(t, rec).Code)
	})

	t.Run("no declared length (chunked)", func(t *testing.T) {
		req := jsonRequest(big)
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
		assert.Equal(t, domain.CodePayloadTooLarge, errorIn(t, rec).Code)
	})

	t.Run("under the limit", func(t *testing.T) {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, jsonRequest(`{"email":"a@b.co","code":"123456","inner":{"name":"x"}}`))
		assert.Equal(t, http.StatusNoContent, rec.Code)
	})
}

func TestPageLimit(t *testing.T) {
	get := func(q string) (int, error) { return PageLimit(httptest.NewRequest("GET", "/x"+q, nil)) }

	n, err := get("")
	require.NoError(t, err)
	assert.Equal(t, 20, n)
	n, err = get("?limit=100")
	require.NoError(t, err)
	assert.Equal(t, 100, n)
	for _, bad := range []string{"?limit=0", "?limit=101", "?limit=-1", "?limit=abc", "?limit=1.5"} {
		_, err := get(bad)
		assert.Equal(t, "limit", fieldsOf(t, err)[0].Field, bad)
	}
}

func TestCursorRoundTripAndRefusals(t *testing.T) {
	type pos struct {
		ID int64 `json:"i"`
	}
	enc := EncodeCursor(&pos{ID: 42})
	require.NotNil(t, enc)
	assert.Nil(t, EncodeCursor[pos](nil))

	var got pos
	present, err := DecodeCursor(httptest.NewRequest("GET", "/x?cursor="+*enc, nil), &got)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, int64(42), got.ID)

	present, err = DecodeCursor(httptest.NewRequest("GET", "/x", nil), &got)
	require.NoError(t, err)
	assert.False(t, present)

	for _, bad := range []string{"?cursor=", "?cursor=!!!", "?cursor=bm90IGpzb24", "?cursor=" + strings.Repeat("A", 300)} {
		_, err := DecodeCursor(httptest.NewRequest("GET", "/x"+bad, nil), &got)
		assert.Equal(t, "cursor", fieldsOf(t, err)[0].Field, bad)
	}
}
