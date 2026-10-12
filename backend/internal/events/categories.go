package events

import (
	"context"
	"errors"
	"fmt"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Category is a ticket category with how many of its seats exist and are free.
type Category struct {
	Row            store.EventCategory
	SeatsAvailable int
	SeatsTotal     int
}

// categories lists an event's categories, cheapest first, with their seat counts.
func (s *Service) categories(ctx context.Context, ev store.Event) ([]Category, error) {
	rows, err := s.q.ListEventCategoriesCheapestFirst(ctx, ev.ID)
	if err != nil {
		return nil, fmt.Errorf("list categories of event %d: %w", ev.ID, err)
	}
	counts := map[int64]domain.SeatCounts{}
	if domain.EventStatus(ev.Status).HasInventory() {
		inv, err := s.inventory(ctx, ev.ID)
		if err != nil {
			return nil, err
		}
		counts = inv.ByCategory()
	}
	out := make([]Category, 0, len(rows))
	for _, r := range rows {
		c := counts[r.ID]
		out = append(out, Category{Row: r, SeatsAvailable: c.Available, SeatsTotal: c.Total()})
	}
	return out, nil
}

// PublicCategories lists a published event's categories. A draft is not found.
func (s *Service) PublicCategories(ctx context.Context, eventID int64) ([]Category, error) {
	row, err := s.publicEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return s.categories(ctx, row.Event)
}

// AdminCategories lists an event's categories in any status.
func (s *Service) AdminCategories(ctx context.Context, eventID int64) ([]Category, error) {
	row, err := s.getEvent(ctx, eventID)
	if err != nil {
		return nil, err
	}
	return s.categories(ctx, row.Event)
}

// CategoryInput is a new category. PriceCents is integer cents; zero is accepted on a draft
// and blocks publishing.
type CategoryInput struct {
	Name        string
	PriceCents  int64
	ColorHex    string
	Description *string
}

// priceProblem holds the two rules about a category's price.
//
// A price is always whole shillings: M-Pesa takes no cents, so a booking with cents in its
// total could be created, hold its seats, and then never be paid for.
//
// And the rule readiness enforces at publish stays true afterwards: once an event has left
// DRAFT none of its categories may be free. A draft may hold a zero price while the organiser
// is still setting things up.
func priceProblem(status domain.EventStatus, priceCents int64) error {
	if priceCents%100 != 0 {
		return domain.InvalidField("price_cents", "whole_shillings", "A price must be in whole shillings, with no cents.")
	}
	if status == domain.EventDraft || priceCents > 0 {
		return nil
	}
	return domain.InvalidField("price_cents", "gt", "A published event's category needs a price above zero.")
}

func nameTaken(name string) *domain.Error {
	return domain.AlreadyExists("name", fmt.Sprintf("This event already has a category called %s.", name))
}

// CreateCategory adds a category to an event.
func (s *Service) CreateCategory(ctx context.Context, eventID int64, in CategoryInput) (Category, error) {
	var row store.EventCategory
	err := store.InTx(ctx, s.db, func(q *store.Queries) error {
		ev, err := s.lockEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		status := domain.EventStatus(ev.Status)
		if !status.Editable() {
			return domain.EventNotEditable(status, fmt.Sprintf("An event that is %s can no longer be edited.", status))
		}
		if err := priceProblem(status, in.PriceCents); err != nil {
			return err
		}
		row, err = q.CreateEventCategory(ctx, store.CreateEventCategoryParams{
			EventID: eventID, Name: in.Name, PriceCents: in.PriceCents, ColorHex: in.ColorHex, Description: in.Description,
		})
		if store.IsUniqueViolation(err, "") {
			return nameTaken(in.Name)
		}
		if err != nil {
			return fmt.Errorf("create category: %w", err)
		}
		return nil
	})
	// A new category has no seats yet, whatever the event's status.
	return Category{Row: row}, err
}

// CategoryUpdate changes a category. Nil and unset fields are left alone.
type CategoryUpdate struct {
	Name        *string
	PriceCents  *int64
	ColorHex    *string
	Description Nullable[string]
	// Confirm must be true to change the price once the event has left DRAFT.
	Confirm bool
}

// UpdateCategory renames, recolours or reprices a category.
//
// A price change on an event that has left DRAFT needs Confirm: fans may already be looking
// at the old price, and seats already held or sold keep it. Bookings are never touched:
// booking_items carries the price each seat was booked at.
func (s *Service) UpdateCategory(ctx context.Context, categoryID int64, in CategoryUpdate) (Category, error) {
	found, err := s.q.GetEventCategoryByID(ctx, categoryID)
	if errors.Is(err, store.ErrNoRows) {
		return Category{}, domain.ErrNotFound
	}
	if err != nil {
		return Category{}, fmt.Errorf("get category %d: %w", categoryID, err)
	}

	var (
		row store.EventCategory
		ev  store.Event
	)
	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		ev, err = s.lockEvent(ctx, q, found.EventID)
		if err != nil {
			return err
		}
		// Read again under the lock: the copy above may be stale by now.
		cat, err := q.GetEventCategory(ctx, store.GetEventCategoryParams{ID: categoryID, EventID: ev.ID})
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("get category %d: %w", categoryID, err)
		}
		status := domain.EventStatus(ev.Status)
		if !status.Editable() {
			return domain.EventNotEditable(status, fmt.Sprintf("An event that is %s can no longer be edited.", status))
		}

		// Checked on the merged row and before the confirmation question: confirm cannot
		// make a free category acceptable.
		merged := cat.PriceCents
		if in.PriceCents != nil {
			merged = *in.PriceCents
		}
		if err := priceProblem(status, merged); err != nil {
			return err
		}

		if in.PriceCents != nil && *in.PriceCents != cat.PriceCents && status != domain.EventDraft && !in.Confirm {
			use, err := q.CountCategoryUse(ctx, store.CountCategoryUseParams{EventID: ev.ID, CategoryID: &categoryID})
			if err != nil {
				return fmt.Errorf("count use of category %d: %w", categoryID, err)
			}
			return &domain.Error{
				Code: domain.CodeConfirmationRequired,
				Message: fmt.Sprintf("This event is already published. %d tickets in %s are sold or held and keep the old price. Confirm to change the price for everyone else.",
					use.Sold+use.Held, cat.Name),
				Details: domain.SalesStartedDetails{Sold: int(use.Sold), Held: int(use.Held)},
			}
		}

		if in.Name != nil {
			cat.Name = *in.Name
		}
		if in.PriceCents != nil {
			cat.PriceCents = *in.PriceCents
		}
		if in.ColorHex != nil {
			cat.ColorHex = *in.ColorHex
		}
		in.Description.apply(&cat.Description)

		row, err = q.UpdateEventCategory(ctx, store.UpdateEventCategoryParams{
			ID: categoryID, EventID: ev.ID, Name: cat.Name, PriceCents: cat.PriceCents,
			ColorHex: cat.ColorHex, Description: cat.Description,
		})
		if store.IsUniqueViolation(err, "") {
			return nameTaken(cat.Name)
		}
		if err != nil {
			return fmt.Errorf("update category %d: %w", categoryID, err)
		}
		// An open seat map carries the category table; stamping the event makes its next
		// poll answer with the full block instead of a delta.
		if err := q.TouchEvent(ctx, ev.ID); err != nil {
			return fmt.Errorf("touch event %d: %w", ev.ID, err)
		}
		return nil
	})
	if err != nil {
		return Category{}, err
	}
	out := Category{Row: row}
	if domain.EventStatus(ev.Status).HasInventory() {
		inv, err := s.inventory(ctx, ev.ID)
		if err != nil {
			return Category{}, err
		}
		c := inv.ByCategory()[row.ID]
		out.SeatsAvailable, out.SeatsTotal = c.Available, c.Total()
	}
	return out, nil
}

// DeleteCategory removes a category and its painting, unless inventory or bookings use it.
func (s *Service) DeleteCategory(ctx context.Context, categoryID int64) error {
	found, err := s.q.GetEventCategoryByID(ctx, categoryID)
	if errors.Is(err, store.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("get category %d: %w", categoryID, err)
	}
	return store.InTx(ctx, s.db, func(q *store.Queries) error {
		// The lock keeps a publish from copying this category into inventory while it is
		// being deleted.
		ev, err := s.lockEvent(ctx, q, found.EventID)
		if err != nil {
			return err
		}
		use, err := q.CountCategoryUse(ctx, store.CountCategoryUseParams{EventID: ev.ID, CategoryID: &categoryID})
		if err != nil {
			return fmt.Errorf("count use of category %d: %w", categoryID, err)
		}
		if use.Seats > 0 || use.Booked > 0 {
			return &domain.Error{
				Code:    domain.CodeCategoryInUse,
				Message: fmt.Sprintf("%s is used by this event's seats and cannot be deleted.", found.Name),
				Details: domain.CategoryInUseDetails{Seats: int(use.Seats), Sold: int(use.Sold), Held: int(use.Held)},
			}
		}
		n, err := q.DeleteEventCategory(ctx, store.DeleteEventCategoryParams{ID: categoryID, EventID: ev.ID})
		if err != nil {
			return fmt.Errorf("delete category %d: %w", categoryID, err)
		}
		if n == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}
