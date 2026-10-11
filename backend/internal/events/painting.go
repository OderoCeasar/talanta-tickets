package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// BlockAssignment says which category a block is painted with.
type BlockAssignment struct {
	BlockID, CategoryID int64
}

// RowAssignment is a row's override of its block's category.
type RowAssignment struct {
	RowID, BlockID, CategoryID int64
}

// Assignments is an event's painting. A seat's category is its row's override if there is
// one, otherwise its block's.
type Assignments struct {
	EventID int64
	Blocks  []BlockAssignment
	Rows    []RowAssignment
}

func assignments(ctx context.Context, q *store.Queries, eventID int64) (Assignments, error) {
	out := Assignments{EventID: eventID, Blocks: []BlockAssignment{}, Rows: []RowAssignment{}}
	blocks, err := q.ListEventBlockCategories(ctx, eventID)
	if err != nil {
		return out, fmt.Errorf("list block categories of event %d: %w", eventID, err)
	}
	rows, err := q.ListEventRowCategoriesWithBlock(ctx, eventID)
	if err != nil {
		return out, fmt.Errorf("list row categories of event %d: %w", eventID, err)
	}
	for _, b := range blocks {
		out.Blocks = append(out.Blocks, BlockAssignment{BlockID: b.BlockID, CategoryID: b.CategoryID})
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, RowAssignment{RowID: r.RowID, BlockID: r.BlockID, CategoryID: r.CategoryID})
	}
	return out, nil
}

// Assignments returns an event's painting.
func (s *Service) Assignments(ctx context.Context, eventID int64) (Assignments, error) {
	if _, err := s.getEvent(ctx, eventID); err != nil {
		return Assignments{}, err
	}
	return assignments(ctx, s.q, eventID)
}

// AssignInput paints blocks and rows with one category, or clears them when CategoryID is nil.
type AssignInput struct {
	CategoryID *int64
	BlockIDs   []int64
	RowIDs     []int64
}

// Assign paints the listed blocks and rows in one transaction. Only a draft can be painted:
// publish copies the painting into event_seats, and a later change would leave the
// inventory and the painting disagreeing.
func (s *Service) Assign(ctx context.Context, eventID int64, in AssignInput) (Assignments, error) {
	var out Assignments
	err := store.InTx(ctx, s.db, func(q *store.Queries) error {
		// The same row lock publish takes: this either commits before publish reads the
		// painting, or runs after it and is refused just below.
		ev, err := s.lockEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		if status := domain.EventStatus(ev.Status); status != domain.EventDraft {
			return domain.EventNotEditable(status, "Seats can only be painted while the event is a draft.")
		}
		if in.CategoryID != nil {
			_, err := q.GetEventCategory(ctx, store.GetEventCategoryParams{ID: *in.CategoryID, EventID: eventID})
			if errors.Is(err, store.ErrNoRows) {
				return domain.E(domain.CodeNotFound, "That category is not part of this event.")
			}
			if err != nil {
				return fmt.Errorf("get category %d: %w", *in.CategoryID, err)
			}
		}

		var foreign []domain.FieldError
		if len(in.BlockIDs) > 0 {
			bad, err := q.BlockIDsNotInVenue(ctx, store.BlockIDsNotInVenueParams{BlockIds: in.BlockIDs, VenueID: ev.VenueID})
			if err != nil {
				return fmt.Errorf("check blocks: %w", err)
			}
			foreign = append(foreign, foreignFields("block_ids", in.BlockIDs, bad, "This block is not at the event's venue.")...)
		}
		if len(in.RowIDs) > 0 {
			bad, err := q.RowIDsNotInVenue(ctx, store.RowIDsNotInVenueParams{RowIds: in.RowIDs, VenueID: ev.VenueID})
			if err != nil {
				return fmt.Errorf("check rows: %w", err)
			}
			foreign = append(foreign, foreignFields("row_ids", in.RowIDs, bad, "This row is not at the event's venue.")...)
		}
		if len(foreign) > 0 {
			return domain.VenueMismatch("Some blocks or rows are not at this event's venue.", foreign...)
		}

		if in.CategoryID == nil {
			if len(in.BlockIDs) > 0 {
				if _, err := q.ClearBlockCategories(ctx, store.ClearBlockCategoriesParams{EventID: eventID, BlockIds: in.BlockIDs}); err != nil {
					return fmt.Errorf("clear block categories: %w", err)
				}
			}
			if len(in.RowIDs) > 0 {
				if _, err := q.ClearRowCategories(ctx, store.ClearRowCategoriesParams{EventID: eventID, RowIds: in.RowIDs}); err != nil {
					return fmt.Errorf("clear row categories: %w", err)
				}
			}
		} else {
			if len(in.BlockIDs) > 0 {
				if _, err := q.SetBlockCategories(ctx, store.SetBlockCategoriesParams{EventID: eventID, CategoryID: *in.CategoryID, BlockIds: in.BlockIDs}); err != nil {
					return fmt.Errorf("set block categories: %w", err)
				}
			}
			if len(in.RowIDs) > 0 {
				if _, err := q.SetRowCategories(ctx, store.SetRowCategoriesParams{EventID: eventID, CategoryID: *in.CategoryID, RowIds: in.RowIDs}); err != nil {
					return fmt.Errorf("set row categories: %w", err)
				}
			}
		}
		out, err = assignments(ctx, q, eventID)
		return err
	})
	return out, err
}

// foreignFields names each offending id by its position in the request ("block_ids[2]").
func foreignFields(name string, sent, bad []int64, message string) []domain.FieldError {
	if len(bad) == 0 {
		return nil
	}
	isBad := make(map[int64]bool, len(bad))
	for _, id := range bad {
		isBad[id] = true
	}
	var out []domain.FieldError
	for i, id := range sent {
		if isBad[id] {
			out = append(out, domain.FieldError{Field: fmt.Sprintf("%s[%d]", name, i), Rule: "venue", Message: message})
		}
	}
	return out
}

// Template is a saved set of categories and painting.
type Template struct {
	Row              store.VenueTemplate
	Categories       []store.TemplateCategory
	BlockAssignments int
	RowAssignments   int
}

func decodeTemplate(row store.VenueTemplate) (Template, error) {
	out := Template{Row: row, Categories: []store.TemplateCategory{}}
	if err := json.Unmarshal(row.Categories, &out.Categories); err != nil {
		return out, fmt.Errorf("decode categories of template %d: %w", row.ID, err)
	}
	var a store.TemplateAssignments
	if err := json.Unmarshal(row.Assignments, &a); err != nil {
		return out, fmt.Errorf("decode assignments of template %d: %w", row.ID, err)
	}
	out.BlockAssignments, out.RowAssignments = len(a.Blocks), len(a.Rows)
	return out, nil
}

// Templates lists a venue's templates by name.
func (s *Service) Templates(ctx context.Context, venueID int64) ([]Template, error) {
	if _, err := s.q.GetVenue(ctx, venueID); errors.Is(err, store.ErrNoRows) {
		return nil, domain.ErrNotFound
	} else if err != nil {
		return nil, fmt.Errorf("get venue %d: %w", venueID, err)
	}
	rows, err := s.q.ListVenueTemplates(ctx, venueID)
	if err != nil {
		return nil, fmt.Errorf("list templates of venue %d: %w", venueID, err)
	}
	out := make([]Template, 0, len(rows))
	for _, r := range rows {
		t, err := decodeTemplate(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// CreateTemplate saves an event's categories and painting under a name. It is a copy:
// later changes to the event do not change the template.
func (s *Service) CreateTemplate(ctx context.Context, venueID int64, name string, eventID int64) (Template, error) {
	if _, err := s.q.GetVenue(ctx, venueID); errors.Is(err, store.ErrNoRows) {
		return Template{}, domain.E(domain.CodeNotFound, "We could not find that venue.")
	} else if err != nil {
		return Template{}, fmt.Errorf("get venue %d: %w", venueID, err)
	}

	var row store.VenueTemplate
	err := store.InTx(ctx, s.db, func(q *store.Queries) error {
		// One transaction so the categories and the painting are read as one picture.
		ev, err := q.GetEvent(ctx, eventID)
		if errors.Is(err, store.ErrNoRows) {
			return domain.E(domain.CodeNotFound, "We could not find that event.")
		}
		if err != nil {
			return fmt.Errorf("get event %d: %w", eventID, err)
		}
		if ev.VenueID != venueID {
			return domain.VenueMismatch("That event is at a different venue.",
				domain.FieldError{Field: "event_id", Rule: "venue", Message: "This event is at another venue."})
		}
		cats, err := q.ListEventCategories(ctx, eventID)
		if err != nil {
			return fmt.Errorf("list categories of event %d: %w", eventID, err)
		}
		painted, err := assignments(ctx, q, eventID)
		if err != nil {
			return err
		}

		names := make(map[int64]string, len(cats))
		tcats := make([]store.TemplateCategory, 0, len(cats))
		for _, c := range cats {
			names[c.ID] = c.Name
			tc := store.TemplateCategory{Name: c.Name, ColorHex: c.ColorHex, PriceCents: c.PriceCents}
			if c.Description != nil {
				tc.Description = *c.Description
			}
			tcats = append(tcats, tc)
		}
		var ta store.TemplateAssignments
		for _, b := range painted.Blocks {
			ta.Blocks = append(ta.Blocks, store.TemplateBlockAssignment{BlockID: b.BlockID, Category: names[b.CategoryID]})
		}
		for _, r := range painted.Rows {
			ta.Rows = append(ta.Rows, store.TemplateRowAssignment{RowID: r.RowID, Category: names[r.CategoryID]})
		}
		catsJSON, assignsJSON, err := store.EncodeTemplate(tcats, ta)
		if err != nil {
			return err
		}
		row, err = q.CreateVenueTemplate(ctx, store.CreateVenueTemplateParams{
			VenueID: venueID, Name: name, Categories: catsJSON, Assignments: assignsJSON,
		})
		if errors.Is(err, store.ErrNoRows) {
			// The insert is ON CONFLICT DO NOTHING: no row back means the name is taken.
			return domain.AlreadyExists("name", fmt.Sprintf("This venue already has a template called %s.", name))
		}
		if err != nil {
			return fmt.Errorf("create template: %w", err)
		}
		return nil
	})
	if err != nil {
		return Template{}, err
	}
	return decodeTemplate(row)
}

// DeleteTemplate removes a venue's template. Events made from it keep what was copied.
func (s *Service) DeleteTemplate(ctx context.Context, venueID, templateID int64) error {
	n, err := s.q.DeleteVenueTemplate(ctx, store.DeleteVenueTemplateParams{ID: templateID, VenueID: venueID})
	if err != nil {
		return fmt.Errorf("delete template %d: %w", templateID, err)
	}
	if n == 0 {
		return domain.E(domain.CodeNotFound, "We could not find that template.")
	}
	return nil
}
