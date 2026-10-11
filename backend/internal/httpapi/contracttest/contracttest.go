// Package contracttest checks real HTTP answers against backend/docs/openapi.yaml, so the
// handlers and the contract cannot drift apart.
//
// Every endpoint test sends its requests through it:
//
//	contract := contracttest.Load(t)
//	rec := contract.Do(t, router, httptest.NewRequest("GET", "/v1/me", nil))
//	// rec is an ordinary *httptest.ResponseRecorder; the test goes on to assert on it.
//
// Do fails the test when:
//   - the path and method are not in the contract;
//   - the status code is not declared for that operation;
//   - the body does not match the declared schema (a missing required field, an extra
//     field, a wrong type, a value outside an enum, …);
//   - a header the contract declares for that answer (Retry-After) is missing or malformed;
//   - the request got a 2xx although the contract would not have allowed it — the handler
//     is then more lenient than the contract says.
//
// A 4xx or 5xx answer to a request that breaks the contract is expected (that is what the
// test is exercising), so the request is only checked when the answer is a success.
package contracttest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

// Contract is the loaded OpenAPI file. It is safe for concurrent use.
type Contract struct {
	doc    *openapi3.T
	router routers.Router

	mu   sync.Mutex
	seen map[string]bool
}

var (
	loadOnce sync.Once
	loaded   *Contract
	loadErr  error
)

// SpecPath is the absolute path of backend/docs/openapi.yaml, found from this file's own
// location so it works whatever directory `go test` runs a package in.
func SpecPath() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("contracttest: cannot find its own source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "openapi.yaml")
}

// Load reads and checks the contract once per test binary.
func Load(t testing.TB) *Contract {
	t.Helper()
	loadOnce.Do(func() { loaded, loadErr = load() })
	if loadErr != nil {
		t.Fatalf("contracttest: %v", loadErr)
	}
	return loaded
}

func load() (*Contract, error) {
	// The library checks date-time out of the box; the other formats the contract uses are
	// opt-in. (This package is only ever linked into test binaries, so setting the
	// library's global format table here affects nothing else.)
	openapi3.DefineStringFormatValidator("email", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForEmail))
	openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(openapi3.FormatOfStringForUUIDOfRFC9562))
	openapi3.DefineStringFormatValidator("uri", openapi3.NewCallbackValidator(func(v string) error {
		u, err := url.Parse(v)
		if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "") {
			return fmt.Errorf("%q is not an absolute URI", v)
		}
		return nil
	}))

	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromFile(SpecPath())
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", SpecPath(), err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("%s is not a valid OpenAPI document: %w", SpecPath(), err)
	}
	// The file lists the production and localhost servers; tests call a handler directly
	// with no host, so routes are matched on the /v1 prefix alone.
	doc.Servers = openapi3.Servers{{URL: "/v1"}}

	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build router from contract: %w", err)
	}
	return &Contract{doc: doc, router: router, seen: make(map[string]bool)}, nil
}

// Doc is the parsed contract, for tests that inspect it (for example the list of error codes).
func (c *Contract) Doc() *openapi3.T { return c.doc }

type options struct {
	skipRequest bool
}

// Option changes how one call is checked.
type Option func(*options)

// LenientRequest says the request is knowingly outside the contract although it succeeds —
// for example a phone number in local form, which the server accepts as a kindness while
// the contract tells clients to send E.164. Use it rarely and say why at the call site.
func LenientRequest() Option { return func(o *options) { o.skipRequest = true } }

// Do sends req to h, checks the answer against the contract and returns it.
func (c *Contract) Do(t testing.TB, h http.Handler, req *http.Request, opts ...Option) *httptest.ResponseRecorder {
	t.Helper()

	// The body is read twice: once by the handler, once by the validator.
	var reqBody []byte
	if req.Body != nil {
		var err error
		reqBody, err = io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("contracttest: read request body: %v", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	c.Check(t, req, reqBody, rec, opts...)
	return rec
}

// Check validates an answer that was obtained some other way (for example from a real
// listener). reqBody is the body that was sent, or nil.
func (c *Contract) Check(t testing.TB, req *http.Request, reqBody []byte, rec *httptest.ResponseRecorder, opts ...Option) {
	t.Helper()
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	label := req.Method + " " + req.URL.Path

	// A fresh request for the validator: the handler consumed the original's body and chi
	// replaced its context.
	vreq := httptest.NewRequest(req.Method, req.URL.String(), bytes.NewReader(reqBody))
	vreq.Header = req.Header.Clone()

	route, pathParams, err := c.router.FindRoute(vreq)
	if err != nil {
		t.Errorf("contract: %s is not in openapi.yaml: %v", label, err)
		return
	}
	opID := route.Operation.OperationID
	c.record(opID, rec.Code)

	filterOpts := &openapi3filter.Options{
		// Report an undeclared status code instead of letting it through.
		IncludeResponseStatus: true,
		MultiError:            true,
		// Whether the token is good is the handler's business; here only the shape matters.
		AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
	}
	input := &openapi3filter.RequestValidationInput{
		Request:    vreq,
		PathParams: pathParams,
		Route:      route,
		Options:    filterOpts,
	}

	if rec.Code >= 200 && rec.Code < 300 && !o.skipRequest {
		if err := openapi3filter.ValidateRequest(context.Background(), input); err != nil {
			t.Errorf("contract: %s (%s) answered %d to a request the contract does not allow — the handler is more lenient than openapi.yaml:\n%v\nrequest body: %s",
				label, opID, rec.Code, err, truncate(reqBody))
		}
	}

	respBody := rec.Body.Bytes()
	err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: input,
		Status:                 rec.Code,
		Header:                 rec.Header(),
		Body:                   io.NopCloser(bytes.NewReader(respBody)),
		Options:                filterOpts,
	})
	if err != nil {
		t.Errorf("contract: %s (%s) answer %d does not match openapi.yaml:\n%v\nresponse body: %s",
			label, opID, rec.Code, err, truncate(respBody))
	}

	c.checkBodyPresence(t, route, rec, label)
	c.checkHeaders(t, route, rec, label)
}

// checkBodyPresence catches what the schema check cannot: a body where the contract
// declares none (204), or none where it declares one.
func (c *Contract) checkBodyPresence(t testing.TB, route *routers.Route, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	ref := route.Operation.Responses.Status(rec.Code)
	if ref == nil || ref.Value == nil {
		return
	}
	declared := len(ref.Value.Content) > 0
	got := rec.Body.Len() > 0
	switch {
	case declared && !got:
		t.Errorf("contract: %s answer %d has no body, but openapi.yaml declares one", label, rec.Code)
	case !declared && got:
		t.Errorf("contract: %s answer %d has a body, but openapi.yaml declares none: %s", label, rec.Code, truncate(rec.Body.Bytes()))
	case declared:
		if ct := rec.Header().Get("Content-Type"); ref.Value.Content.Get(ct) == nil {
			t.Errorf("contract: %s answer %d has Content-Type %q, which openapi.yaml does not declare", label, rec.Code, ct)
		}
	}
}

// checkHeaders requires every header the contract declares for this answer, and checks
// integer ones against their minimum. (The library does not validate response headers.)
func (c *Contract) checkHeaders(t testing.TB, route *routers.Route, rec *httptest.ResponseRecorder, label string) {
	t.Helper()
	ref := route.Operation.Responses.Status(rec.Code)
	if ref == nil || ref.Value == nil {
		return
	}
	for name, h := range ref.Value.Headers {
		value := rec.Header().Get(name)
		if value == "" {
			t.Errorf("contract: %s answer %d is missing the %s header openapi.yaml declares", label, rec.Code, name)
			continue
		}
		if h.Value == nil || h.Value.Schema == nil || h.Value.Schema.Value == nil {
			continue
		}
		schema := h.Value.Schema.Value
		if schema.Type != nil && schema.Type.Is(openapi3.TypeInteger) {
			n, err := strconv.Atoi(value)
			if err != nil {
				t.Errorf("contract: %s answer %d header %s = %q is not an integer", label, rec.Code, name, value)
				continue
			}
			if schema.Min != nil && float64(n) < *schema.Min {
				t.Errorf("contract: %s answer %d header %s = %d is below the minimum %v", label, rec.Code, name, n, *schema.Min)
			}
		}
	}
}

func (c *Contract) record(operationID string, status int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[operationID+" "+strconv.Itoa(status)] = true
}

// Seen lists every "operationId status" pair checked so far in this test binary, sorted.
func (c *Contract) Seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, 0, len(c.seen))
	for k := range c.seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// HasSeen reports whether an answer with this status was checked for the operation.
func (c *Contract) HasSeen(operationID string, status int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[operationID+" "+strconv.Itoa(status)]
}

// OperationsWithTag lists the operationIds carrying an OpenAPI tag, sorted.
func (c *Contract) OperationsWithTag(tag string) []string {
	var out []string
	for _, item := range c.doc.Paths.Map() {
		for _, op := range item.Operations() {
			for _, t := range op.Tags {
				if t == tag {
					out = append(out, op.OperationID)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func truncate(b []byte) string {
	const max = 2000
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}
