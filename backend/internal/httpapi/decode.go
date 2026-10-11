package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	"github.com/go-playground/validator/v10"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// DefaultBodyLimit is the most a request body may be. The largest real body is a batch of
// 500 gate scans, well under this.
const DefaultBodyLimit = 1 << 20

// validate is shared: validator caches each struct's rules on first use.
var validate = newValidator()

func newValidator() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	// Report fields by their JSON name, which is what the client sent and what the contract
	// calls them.
	v.RegisterTagNameFunc(func(f reflect.StructField) string {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			return f.Name
		}
		return name
	})
	return v
}

// DecodeJSON reads a JSON request body into dst and validates it by dst's `validate` tags.
// Every failure is a *domain.Error ready for WriteError:
//
//   - not application/json, malformed JSON, a wrong type, an unknown field, a failed rule
//     → 400 VALIDATION_FAILED with one entry per bad field;
//   - a body over the size limit → 413 PAYLOAD_TOO_LARGE.
func DecodeJSON(r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); ct != "" || r.ContentLength != 0 {
		mediaType, _, err := mime.ParseMediaType(ct)
		if err != nil || mediaType != "application/json" {
			return domain.InvalidField("Content-Type", "content_type", "Send the body as application/json.")
		}
	}

	dec := json.NewDecoder(r.Body)
	// A misspelt field would otherwise be dropped silently and the request would "work"
	// with a default the caller did not mean.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		return decodeError(err)
	}
	// One JSON value only: `{"a":1}{"b":2}` is refused.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return domain.ErrPayloadTooLarge
		}
		return domain.InvalidField("body", "json", "The body must be a single JSON object.")
	}
	return Validate(dst)
}

func decodeError(err error) error {
	var (
		tooBig    *http.MaxBytesError
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
	)
	switch {
	case errors.As(err, &tooBig):
		return domain.ErrPayloadTooLarge
	case errors.Is(err, io.EOF):
		return domain.InvalidField("body", "required", "Send a JSON body.")
	case errors.As(err, &syntaxErr), errors.Is(err, io.ErrUnexpectedEOF):
		return domain.InvalidField("body", "json", "The body is not valid JSON.")
	case errors.As(err, &typeErr):
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		return domain.InvalidField(field, "type", fmt.Sprintf("Must be %s.", jsonTypeName(typeErr.Type)))
	}
	// encoding/json has no typed error for an unknown field.
	if name, ok := strings.CutPrefix(err.Error(), "json: unknown field "); ok {
		if unquoted, uerr := strconv.Unquote(name); uerr == nil {
			name = unquoted
		}
		return domain.InvalidField(name, "unknown", "This field is not accepted.")
	}
	return domain.InvalidField("body", "json", "The body is not valid JSON.")
}

func jsonTypeName(t reflect.Type) string {
	if t == nil {
		return "a different type"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "a list"
	default:
		return "an object"
	}
}

// Validate checks v against its `validate` tags and returns a VALIDATION_FAILED error
// listing every failed field, or nil.
func Validate(v any) error {
	err := validate.Struct(v)
	if err == nil {
		return nil
	}
	var verrs validator.ValidationErrors
	if !errors.As(err, &verrs) {
		// InvalidValidationError: v is not a struct. A programming mistake, so INTERNAL.
		return fmt.Errorf("validate %T: %w", v, err)
	}
	fields := make([]domain.FieldError, 0, len(verrs))
	for _, fe := range verrs {
		fields = append(fields, domain.FieldError{
			Field:   fieldPath(fe.Namespace()),
			Rule:    fe.Tag(),
			Message: ruleMessage(fe),
		})
	}
	return domain.Invalid(fields...)
}

// fieldPath drops the struct's own name from validator's namespace: "createHold.seat_ids[2]"
// becomes "seat_ids[2]".
func fieldPath(namespace string) string {
	if _, rest, ok := strings.Cut(namespace, "."); ok {
		return rest
	}
	return namespace
}

func ruleMessage(fe validator.FieldError) string {
	isText := fe.Kind() == reflect.String
	isList := fe.Kind() == reflect.Slice || fe.Kind() == reflect.Array
	switch fe.Tag() {
	case "required":
		return "This field is required."
	case "email":
		return "Enter a valid email address."
	case "min":
		switch {
		case isText:
			return fmt.Sprintf("Use at least %s characters.", fe.Param())
		case isList:
			return fmt.Sprintf("Give at least %s.", fe.Param())
		}
		return fmt.Sprintf("Must be %s or more.", fe.Param())
	case "max":
		switch {
		case isText:
			return fmt.Sprintf("Use at most %s characters.", fe.Param())
		case isList:
			return fmt.Sprintf("Give at most %s.", fe.Param())
		}
		return fmt.Sprintf("Must be %s or less.", fe.Param())
	case "len":
		if isText {
			return fmt.Sprintf("Must be exactly %s characters.", fe.Param())
		}
		return fmt.Sprintf("Must have exactly %s.", fe.Param())
	case "number", "numeric":
		return "Use digits only."
	case "oneof":
		return fmt.Sprintf("Must be one of: %s.", strings.Join(strings.Fields(fe.Param()), ", "))
	case "gt":
		return fmt.Sprintf("Must be more than %s.", fe.Param())
	case "gte":
		return fmt.Sprintf("Must be %s or more.", fe.Param())
	case "lt":
		return fmt.Sprintf("Must be less than %s.", fe.Param())
	case "lte":
		return fmt.Sprintf("Must be %s or less.", fe.Param())
	case "unique":
		return "Must not repeat a value."
	case "uuid", "uuid4":
		return "Must be a UUID."
	case "hexcolor":
		return "Must be a colour such as #064E3B."
	}
	return "This value is not valid."
}
