package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
)

// optional is a PATCH field that may be absent, null or a value. encoding/json cannot tell
// the first two apart on a plain pointer, and the contract gives them different meanings:
// absent leaves the field alone, null clears it.
type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

func (o optional[T]) nullable() events.Nullable[T] {
	return events.Nullable[T]{Set: o.Set, Value: o.Value}
}

// timeFormatMessage tells a caller what a time must look like.
const timeFormatMessage = "Use a date and time such as 2026-11-14T13:00:00Z."

// QueryTime reads an optional RFC 3339 query value such as ?from=.
func QueryTime(r *http.Request, name string) (*time.Time, error) {
	if !r.URL.Query().Has(name) {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, r.URL.Query().Get(name))
	if err != nil {
		return nil, domain.InvalidField(name, "datetime", timeFormatMessage)
	}
	return &t, nil
}

// fieldChecks collects the failures of hand-checked fields: the ones validator tags cannot
// reach because they sit inside an optional.
type fieldChecks []domain.FieldError

func (c *fieldChecks) add(field, rule, message string) {
	*c = append(*c, domain.FieldError{Field: field, Rule: rule, Message: message})
}

// time parses an RFC 3339 time sent as a JSON string. Times are decoded as strings and
// parsed here, not by a JSON unmarshaler, so a bad one is reported against its own field.
func (c *fieldChecks) time(field string, v *string) *time.Time {
	if v == nil {
		return nil
	}
	t, err := time.Parse(time.RFC3339, *v)
	if err != nil {
		c.add(field, "datetime", timeFormatMessage)
		return nil
	}
	return &t
}

// optionalTime is time for a field that may also be absent or null.
func (c *fieldChecks) optionalTime(field string, o optional[string]) events.Nullable[time.Time] {
	return events.Nullable[time.Time]{Set: o.Set, Value: c.time(field, o.Value)}
}

// text trims a nullable text field in place and checks its length. Empty becomes null.
func (c *fieldChecks) text(field string, v **string, maxLen int) {
	if *v == nil {
		return
	}
	s := strings.TrimSpace(**v)
	if s == "" {
		*v = nil
		return
	}
	if utf8.RuneCountInString(s) > maxLen {
		c.add(field, "max", fmt.Sprintf("Use at most %d characters.", maxLen))
		return
	}
	*v = &s
}

// required trims a text field that must not be empty and checks its length.
func (c *fieldChecks) required(field string, v *string, maxLen int) {
	*v = strings.TrimSpace(*v)
	switch {
	case *v == "":
		c.add(field, "required", "This field is required.")
	case utf8.RuneCountInString(*v) > maxLen:
		c.add(field, "max", fmt.Sprintf("Use at most %d characters.", maxLen))
	}
}

// posterURL checks a nullable poster address: an absolute http or https URL.
func (c *fieldChecks) posterURL(field string, v **string) {
	c.text(field, v, domain.PosterURLMaxLength)
	if *v == nil {
		return
	}
	u, err := url.Parse(**v)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		c.add(field, "url", "Use a web address starting with https://.")
	}
}

// finite refuses NaN and infinities, which JSON cannot carry but a huge exponent can make.
func (c *fieldChecks) finite(field string, v *float64) {
	if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
		c.add(field, "number", "Must be an ordinary number.")
	}
}

func (c fieldChecks) err() error {
	if len(c) == 0 {
		return nil
	}
	return domain.Invalid(c...)
}

// errEmptyPatch is the answer to a PATCH that changes nothing.
var errEmptyPatch = domain.InvalidField("body", "required", "Send at least one field to change.")
