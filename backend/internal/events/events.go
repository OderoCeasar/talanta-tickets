package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Nullable is a field of a partial update that can be left alone, set, or cleared.
type Nullable[T any] struct {
	// Set is true when the caller sent the field at all.
	Set bool
	// Value nil with Set true means "clear it".
	Value *T
}

func (n Nullable[T]) apply(dst **T) {
	if n.Set {
		*dst = n.Value
	}
}

// EventCursor is the position after the last event of a page.
type EventCursor struct {
	KickoffAt time.Time `json:"k"`
	ID        int64     `json:"i"`
}

// ListFilter narrows an event list.
type ListFilter struct {
	// Status filters on the effective status.
	Status *domain.EventStatus
	// From and To bound the kickoff: at or after From, before To.
	From, To *time.Time
	Limit    int
	After    *EventCursor
}

func (f ListFilter) cursor() (bool, time.Time, int64) {
	if f.After == nil {
		return false, time.Time{}, 0
	}
	return true, f.After.KickoffAt, f.After.ID
}

func statusText(s *domain.EventStatus) *string {
	if s == nil {
		return nil
	}
	v := string(*s)
	return &v
}

// ListPublic is the fan's list: published events from From (default now) on, soonest first.
func (s *Service) ListPublic(ctx context.Context, f ListFilter) ([]Event, *EventCursor, error) {
	now := s.now()
	from := now
	if f.From != nil {
		from = *f.From
	}
	has, kickoff, id := f.cursor()
	rows, err := s.q.ListPublicEvents(ctx, store.ListPublicEventsParams{
		FromAt: from, ToAt: f.To, EffectiveStatus: statusText(f.Status), NowAt: now,
		HasCursor: has, CursorKickoffAt: kickoff, CursorID: id, PageLimit: int32(f.Limit + 1),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list public events: %w", err)
	}
	return s.page(ctx, f.Limit, len(rows), func(i int) (store.Event, string) { return rows[i].Event, rows[i].VenueName })
}

// ListAdmin is the organiser's list: every status, latest kickoff first.
func (s *Service) ListAdmin(ctx context.Context, f ListFilter) ([]Event, *EventCursor, error) {
	has, kickoff, id := f.cursor()
	rows, err := s.q.ListAdminEvents(ctx, store.ListAdminEventsParams{
		FromAt: f.From, ToAt: f.To, EffectiveStatus: statusText(f.Status), NowAt: s.now(),
		HasCursor: has, CursorKickoffAt: kickoff, CursorID: id, PageLimit: int32(f.Limit + 1),
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list events: %w", err)
	}
	return s.page(ctx, f.Limit, len(rows), func(i int) (store.Event, string) { return rows[i].Event, rows[i].VenueName })
}

// page turns limit+1 fetched rows into one page and the cursor for the next.
func (s *Service) page(ctx context.Context, limit, fetched int, at func(int) (store.Event, string)) ([]Event, *EventCursor, error) {
	n := min(fetched, limit)
	out := make([]Event, 0, n)
	for i := 0; i < n; i++ {
		row, venue := at(i)
		ev, err := s.view(ctx, row, venue, s.policy.ListCountsTTL)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, ev)
	}
	if fetched <= limit || n == 0 {
		return out, nil, nil
	}
	last := out[n-1].Row
	return out, &EventCursor{KickoffAt: last.KickoffAt, ID: last.ID}, nil
}

// GetPublic returns one event to a fan. A draft is not found.
func (s *Service) GetPublic(ctx context.Context, eventID int64) (Event, error) {
	row, err := s.publicEvent(ctx, eventID)
	if err != nil {
		return Event{}, err
	}
	return s.view(ctx, row.Event, row.VenueName, s.policy.CountsTTL)
}

// GetAdmin returns one event in any status.
func (s *Service) GetAdmin(ctx context.Context, eventID int64) (Event, error) {
	row, err := s.getEvent(ctx, eventID)
	if err != nil {
		return Event{}, err
	}
	return s.view(ctx, row.Event, row.VenueName, s.policy.CountsTTL)
}

// CreateInput is a new draft event.
type CreateInput struct {
	VenueID           int64
	Title             string
	Competition       *string
	HomeTeam          *string
	AwayTeam          *string
	KickoffAt         time.Time
	GatesOpenAt       *time.Time
	PosterURL         *string
	SalesOpenAt       *time.Time
	SalesCloseAt      *time.Time
	MaxTicketsPerUser int
	// TemplateID copies a venue template's categories and painting onto the new event.
	TemplateID *int64
}

// timeProblems checks the order of an event's times: gates before kickoff, sales closing
// after they open and not after kickoff.
func timeProblems(kickoff time.Time, gates, open, closeAt *time.Time) []domain.FieldError {
	var out []domain.FieldError
	if gates != nil && !gates.Before(kickoff) {
		out = append(out, domain.FieldError{Field: "gates_open_at", Rule: "before_kickoff", Message: "Gates must open before kickoff."})
	}
	switch {
	case closeAt == nil:
	case open != nil && !closeAt.After(*open):
		out = append(out, domain.FieldError{Field: "sales_close_at", Rule: "after_open", Message: "Sales must close after they open."})
	case closeAt.After(kickoff):
		out = append(out, domain.FieldError{Field: "sales_close_at", Rule: "before_kickoff", Message: "Sales must close at or before kickoff."})
	}
	return out
}

// mergeFieldErrors appends the errors of more that name a field and rule not already in base,
// so two checks that overlap (the order of the times) report a problem once.
func mergeFieldErrors(base, more []domain.FieldError) []domain.FieldError {
	for _, m := range more {
		dup := false
		for _, b := range base {
			if b.Field == m.Field && b.Rule == m.Rule {
				dup = true
				break
			}
		}
		if !dup {
			base = append(base, m)
		}
	}
	return base
}

// Create makes a draft event, optionally from a venue template.
func (s *Service) Create(ctx context.Context, in CreateInput) (Event, error) {
	if problems := timeProblems(in.KickoffAt, in.GatesOpenAt, in.SalesOpenAt, in.SalesCloseAt); len(problems) > 0 {
		return Event{}, domain.Invalid(problems...)
	}
	venue, err := s.q.GetVenue(ctx, in.VenueID)
	if errors.Is(err, store.ErrNoRows) {
		return Event{}, domain.E(domain.CodeNotFound, "We could not find that venue.")
	}
	if err != nil {
		return Event{}, fmt.Errorf("get venue %d: %w", in.VenueID, err)
	}
	if in.TemplateID != nil {
		tpl, err := s.q.GetVenueTemplate(ctx, *in.TemplateID)
		if errors.Is(err, store.ErrNoRows) {
			return Event{}, domain.E(domain.CodeNotFound, "We could not find that template.")
		}
		if err != nil {
			return Event{}, fmt.Errorf("get template %d: %w", *in.TemplateID, err)
		}
		if tpl.VenueID != in.VenueID {
			return Event{}, domain.VenueMismatch("That template belongs to a different venue.",
				domain.FieldError{Field: "template_id", Rule: "venue", Message: "This template is for another venue."})
		}
	}
	if in.MaxTicketsPerUser == 0 {
		in.MaxTicketsPerUser = domain.DefaultMaxTicketsPerUser
	}

	var row store.Event
	err = store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		row, err = store.New(tx).CreateEvent(ctx, store.CreateEventParams{
			VenueID: in.VenueID, Title: in.Title, Competition: in.Competition,
			HomeTeam: in.HomeTeam, AwayTeam: in.AwayTeam, KickoffAt: in.KickoffAt,
			GatesOpenAt: in.GatesOpenAt, PosterUrl: in.PosterURL,
			SalesOpenAt: in.SalesOpenAt, SalesCloseAt: in.SalesCloseAt,
			MaxTicketsPerUser: int32(in.MaxTicketsPerUser),
		})
		if err != nil {
			return fmt.Errorf("create event: %w", err)
		}
		if in.TemplateID == nil {
			return nil
		}
		// No event lock: nobody else can see the new event until this commits.
		_, err = store.ApplyVenueTemplate(ctx, tx, row.ID, *in.TemplateID)
		return err
	})
	if err != nil {
		return Event{}, err
	}
	return s.view(ctx, row, venue.Name, s.policy.CountsTTL)
}

// UpdateInput changes an event's details. Nil and unset fields are left alone.
type UpdateInput struct {
	Title             *string
	Competition       Nullable[string]
	HomeTeam          Nullable[string]
	AwayTeam          Nullable[string]
	KickoffAt         *time.Time
	GatesOpenAt       Nullable[time.Time]
	PosterURL         Nullable[string]
	SalesOpenAt       Nullable[time.Time]
	SalesCloseAt      Nullable[time.Time]
	MaxTicketsPerUser *int
}

// Update edits an event's details. The sales window is part of what "on sale" means, so the
// edit takes the event lock like everything else that publish depends on.
func (s *Service) Update(ctx context.Context, eventID int64, in UpdateInput) (Event, error) {
	var row store.Event
	err := store.InTx(ctx, s.db, func(q *store.Queries) error {
		ev, err := s.lockEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		status := domain.EventStatus(ev.Status)
		if !status.Editable() {
			return domain.EventNotEditable(status, fmt.Sprintf("An event that is %s can no longer be edited.", status))
		}

		before := ev.KickoffAt
		if in.Title != nil {
			ev.Title = *in.Title
		}
		if in.KickoffAt != nil {
			ev.KickoffAt = *in.KickoffAt
		}
		if in.MaxTicketsPerUser != nil {
			ev.MaxTicketsPerUser = int32(*in.MaxTicketsPerUser)
		}
		in.Competition.apply(&ev.Competition)
		in.HomeTeam.apply(&ev.HomeTeam)
		in.AwayTeam.apply(&ev.AwayTeam)
		in.GatesOpenAt.apply(&ev.GatesOpenAt)
		in.PosterURL.apply(&ev.PosterUrl)
		in.SalesOpenAt.apply(&ev.SalesOpenAt)
		in.SalesCloseAt.apply(&ev.SalesCloseAt)

		// The merged result is what must make sense, not just the fields that were sent.
		problems := timeProblems(ev.KickoffAt, ev.GatesOpenAt, ev.SalesOpenAt, ev.SalesCloseAt)
		if status != domain.EventDraft {
			// Publish refused an event with no sales window or a kickoff in the past; an
			// edit afterwards must not be a way round that.
			problems = mergeFieldErrors(problems, domain.PublishedEventProblems(
				ev.KickoffAt, ev.SalesOpenAt, ev.SalesCloseAt, !ev.KickoffAt.Equal(before), s.now()))
		}
		if len(problems) > 0 {
			return domain.Invalid(problems...)
		}
		row, err = q.UpdateEvent(ctx, store.UpdateEventParams{
			ID: ev.ID, Title: ev.Title, Competition: ev.Competition, HomeTeam: ev.HomeTeam, AwayTeam: ev.AwayTeam,
			KickoffAt: ev.KickoffAt, GatesOpenAt: ev.GatesOpenAt, PosterUrl: ev.PosterUrl,
			SalesOpenAt: ev.SalesOpenAt, SalesCloseAt: ev.SalesCloseAt, MaxTicketsPerUser: ev.MaxTicketsPerUser,
		})
		if err != nil {
			return fmt.Errorf("update event %d: %w", eventID, err)
		}
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	venue, err := s.q.GetVenue(ctx, row.VenueID)
	if err != nil {
		return Event{}, fmt.Errorf("get venue %d: %w", row.VenueID, err)
	}
	return s.view(ctx, row, venue.Name, s.policy.CountsTTL)
}

// Delete removes a draft event with its categories and painting (the database cascades
// them). Only a draft: a published event has inventory and may have bookings, and is
// cancelled instead. It takes the event lock like every other edit, so it cannot slip in
// between a publish reading the draft and copying it.
func (s *Service) Delete(ctx context.Context, eventID int64) error {
	err := store.InTx(ctx, s.db, func(q *store.Queries) error {
		ev, err := s.lockEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		if status := domain.EventStatus(ev.Status); status != domain.EventDraft {
			return domain.EventNotEditable(status, fmt.Sprintf("Only a draft can be deleted. This event is %s; cancel it instead.", status))
		}
		n, err := q.DeleteDraftEvent(ctx, eventID)
		if err != nil {
			return fmt.Errorf("delete event %d: %w", eventID, err)
		}
		if n != 1 {
			// Cannot happen while the row lock is held.
			return fmt.Errorf("delete event %d: status changed under the row lock", eventID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.counts.invalidate(eventID)
	s.log.InfoContext(ctx, "draft event deleted", "event_id", eventID)
	return nil
}
