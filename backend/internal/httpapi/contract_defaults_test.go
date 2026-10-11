package httpapi

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
)

// Fix 10(a). openapi-typescript turns a property that declares `default` into a REQUIRED
// field of the generated type, so `UpdateCategory.confirm` and the optional fields of
// `GenerateSeats` had to be sent by clients although the server never needed them. In a
// request body an optional field therefore says what happens when it is left out in its
// description, not with `default`. This test keeps it that way for every request body.
func TestContract_RequestBodiesDeclareNoDefaults(t *testing.T) {
	doc := contracttest.Load(t).Doc()
	bodies := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			media := op.RequestBody.Value.Content.Get("application/json")
			if media == nil || media.Schema == nil {
				continue
			}
			bodies++
			walkSchema(media.Schema, method+" "+path, map[*openapi3.Schema]bool{}, func(where string, s *openapi3.Schema) {
				assert.Nil(t, s.Default, "%s: `default` makes the generated TypeScript field required; say it in the description instead", where)
			})
		}
	}
	require.Greater(t, bodies, 20, "the walk must have found the request bodies")

	// The fields the admin and mobile engineers reported, by name: present, optional.
	for schema, fields := range map[string][]string{
		"UpdateCategory": {"confirm"},
		"GenerateSeats":  {"row_label_scheme", "numbering_direction", "dry_run", "row_offsets"},
	} {
		s := doc.Components.Schemas[schema].Value
		for _, f := range fields {
			require.Contains(t, s.Properties, f, schema)
			assert.NotContains(t, s.Required, f, "%s.%s", schema, f)
			assert.Nil(t, s.Properties[f].Value.Default, "%s.%s", schema, f)
		}
	}
}

func walkSchema(ref *openapi3.SchemaRef, where string, seen map[*openapi3.Schema]bool, visit func(string, *openapi3.Schema)) {
	if ref == nil || ref.Value == nil || seen[ref.Value] {
		return
	}
	s := ref.Value
	seen[s] = true
	visit(where, s)
	for name, p := range s.Properties {
		walkSchema(p, where+"."+name, seen, visit)
	}
	walkSchema(s.Items, where+"[]", seen, visit)
	for _, group := range []openapi3.SchemaRefs{s.AllOf, s.AnyOf, s.OneOf} {
		for _, sub := range group {
			walkSchema(sub, where, seen, visit)
		}
	}
}
