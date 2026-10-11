package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// PublishEventSeats creates the event's inventory: one event_seats row for every active seat
// of its venue, with block_id copied from the seat and the category resolved as the row's
// override, else the block's. It returns how many rows it inserted.
//
// It is safe to call twice: seats that already have a row are left alone, so a repeat returns
// 0 unless seats were added to the venue in between. It does not change the event's status;
// the caller does that in the same transaction.
func PublishEventSeats(ctx context.Context, db DBTX, eventID int64) (int64, error) {
	inserted, err := New(db).InsertEventSeatsForEvent(ctx, eventID)
	if err != nil {
		return 0, fmt.Errorf("publish seats of event %d: %w", eventID, err)
	}
	return inserted, nil
}

// TemplateCategory is one element of venue_templates.categories.
type TemplateCategory struct {
	Name        string `json:"name"`
	ColorHex    string `json:"color_hex"`
	PriceCents  int64  `json:"price_cents"`
	Description string `json:"description,omitempty"`
}

// TemplateAssignments is the shape of venue_templates.assignments. A category is named, not
// referenced by id, because categories belong to an event and a template outlives events.
type TemplateAssignments struct {
	Blocks []TemplateBlockAssignment `json:"blocks"`
	Rows   []TemplateRowAssignment   `json:"rows"`
}

type TemplateBlockAssignment struct {
	BlockID  int64  `json:"block_id"`
	Category string `json:"category"`
}

type TemplateRowAssignment struct {
	RowID    int64  `json:"row_id"`
	Category string `json:"category"`
}

// EncodeTemplate turns categories and assignments into the two JSONB values of a template.
func EncodeTemplate(categories []TemplateCategory, assignments TemplateAssignments) (cats, assigns json.RawMessage, err error) {
	// Empty lists must be stored as [] and not null: the table checks the JSON types.
	if categories == nil {
		categories = []TemplateCategory{}
	}
	if assignments.Blocks == nil {
		assignments.Blocks = []TemplateBlockAssignment{}
	}
	if assignments.Rows == nil {
		assignments.Rows = []TemplateRowAssignment{}
	}
	if cats, err = json.Marshal(categories); err != nil {
		return nil, nil, fmt.Errorf("encode template categories: %w", err)
	}
	if assigns, err = json.Marshal(assignments); err != nil {
		return nil, nil, fmt.Errorf("encode template assignments: %w", err)
	}
	return cats, assigns, nil
}

// TemplateApplied counts what ApplyVenueTemplate created.
type TemplateApplied struct {
	Categories       int64
	BlockAssignments int64
	RowAssignments   int64
}

// ApplyVenueTemplate copies a template onto an event: its categories, then the block and row
// assignments that point at them. Categories and assignments the event already has are kept,
// so applying twice changes nothing. Call it inside a transaction; the three statements only
// make sense together.
func ApplyVenueTemplate(ctx context.Context, db DBTX, eventID, templateID int64) (TemplateApplied, error) {
	q := New(db)
	var out TemplateApplied
	var err error

	out.Categories, err = q.CreateCategoriesFromTemplate(ctx, CreateCategoriesFromTemplateParams{
		EventID: eventID, TemplateID: templateID,
	})
	if err != nil {
		return out, fmt.Errorf("apply template %d to event %d: categories: %w", templateID, eventID, err)
	}
	out.BlockAssignments, err = q.AssignBlockCategoriesFromTemplate(ctx, AssignBlockCategoriesFromTemplateParams{
		EventID: eventID, TemplateID: templateID,
	})
	if err != nil {
		return out, fmt.Errorf("apply template %d to event %d: blocks: %w", templateID, eventID, err)
	}
	out.RowAssignments, err = q.AssignRowCategoriesFromTemplate(ctx, AssignRowCategoriesFromTemplateParams{
		EventID: eventID, TemplateID: templateID,
	})
	if err != nil {
		return out, fmt.Errorf("apply template %d to event %d: rows: %w", templateID, eventID, err)
	}
	return out, nil
}
