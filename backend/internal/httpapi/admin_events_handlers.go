package httpapi

import (
	"net/http"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
)

func (s *Server) listAdminEvents(w http.ResponseWriter, r *http.Request) {
	f, err := eventListFilter(r, domain.EventStatuses)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	list, next, err := s.events.ListAdmin(r.Context(), f)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeEventPage(w, list, next)
}

type createEventBody struct {
	VenueID           int64   `json:"venue_id" validate:"required,min=1"`
	Title             string  `json:"title" validate:"required"`
	Competition       *string `json:"competition"`
	HomeTeam          *string `json:"home_team"`
	AwayTeam          *string `json:"away_team"`
	KickoffAt         *string `json:"kickoff_at" validate:"required"`
	GatesOpenAt       *string `json:"gates_open_at"`
	PosterURL         *string `json:"poster_url"`
	SalesOpenAt       *string `json:"sales_open_at"`
	SalesCloseAt      *string `json:"sales_close_at"`
	MaxTicketsPerUser *int    `json:"max_tickets_per_user" validate:"omitempty,min=1,max=50"`
	TemplateID        *int64  `json:"template_id" validate:"omitempty,min=1"`
}

func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	var body createEventBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	var checks fieldChecks
	checks.required("title", &body.Title, domain.EventTitleMaxLength)
	checks.text("competition", &body.Competition, domain.EventCompetitionMaxLength)
	checks.text("home_team", &body.HomeTeam, domain.EventTeamMaxLength)
	checks.text("away_team", &body.AwayTeam, domain.EventTeamMaxLength)
	checks.posterURL("poster_url", &body.PosterURL)
	kickoff := checks.time("kickoff_at", body.KickoffAt)
	gates := checks.time("gates_open_at", body.GatesOpenAt)
	salesOpen := checks.time("sales_open_at", body.SalesOpenAt)
	salesClose := checks.time("sales_close_at", body.SalesCloseAt)
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	in := events.CreateInput{
		VenueID: body.VenueID, Title: body.Title, Competition: body.Competition,
		HomeTeam: body.HomeTeam, AwayTeam: body.AwayTeam, KickoffAt: *kickoff,
		GatesOpenAt: gates, PosterURL: body.PosterURL,
		SalesOpenAt: salesOpen, SalesCloseAt: salesClose,
		TemplateID: body.TemplateID,
	}
	if body.MaxTicketsPerUser != nil {
		in.MaxTicketsPerUser = *body.MaxTicketsPerUser
	}
	ev, err := s.events.Create(r.Context(), in)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newEventDTO(ev))
}

func (s *Server) getAdminEvent(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ev, err := s.events.GetAdmin(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newEventDTO(ev))
}

type updateEventBody struct {
	Title             *string          `json:"title"`
	Competition       optional[string] `json:"competition"`
	HomeTeam          optional[string] `json:"home_team"`
	AwayTeam          optional[string] `json:"away_team"`
	KickoffAt         *string          `json:"kickoff_at"`
	GatesOpenAt       optional[string] `json:"gates_open_at"`
	PosterURL         optional[string] `json:"poster_url"`
	SalesOpenAt       optional[string] `json:"sales_open_at"`
	SalesCloseAt      optional[string] `json:"sales_close_at"`
	MaxTicketsPerUser *int             `json:"max_tickets_per_user" validate:"omitempty,min=1,max=50"`
}

func (b updateEventBody) empty() bool {
	return b.Title == nil && !b.Competition.Set && !b.HomeTeam.Set && !b.AwayTeam.Set && b.KickoffAt == nil &&
		!b.GatesOpenAt.Set && !b.PosterURL.Set && !b.SalesOpenAt.Set && !b.SalesCloseAt.Set && b.MaxTicketsPerUser == nil
}

func (s *Server) updateEvent(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body updateEventBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	if body.empty() {
		WriteError(w, r, errEmptyPatch)
		return
	}
	var checks fieldChecks
	if body.Title != nil {
		checks.required("title", body.Title, domain.EventTitleMaxLength)
	}
	checks.text("competition", &body.Competition.Value, domain.EventCompetitionMaxLength)
	checks.text("home_team", &body.HomeTeam.Value, domain.EventTeamMaxLength)
	checks.text("away_team", &body.AwayTeam.Value, domain.EventTeamMaxLength)
	checks.posterURL("poster_url", &body.PosterURL.Value)
	in := events.UpdateInput{
		Title: body.Title, Competition: body.Competition.nullable(),
		HomeTeam: body.HomeTeam.nullable(), AwayTeam: body.AwayTeam.nullable(),
		KickoffAt:    checks.time("kickoff_at", body.KickoffAt),
		GatesOpenAt:  checks.optionalTime("gates_open_at", body.GatesOpenAt),
		SalesOpenAt:  checks.optionalTime("sales_open_at", body.SalesOpenAt),
		SalesCloseAt: checks.optionalTime("sales_close_at", body.SalesCloseAt),
		PosterURL:    body.PosterURL.nullable(), MaxTicketsPerUser: body.MaxTicketsPerUser,
	}
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	ev, err := s.events.Update(r.Context(), id, in)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newEventDTO(ev))
}

func (s *Server) deleteEvent(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.events.Delete(r.Context(), id); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getEventReadiness(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	rd, err := s.events.Readiness(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, readinessDTO{
		EventID: rd.EventID, EventStatus: rd.Status, Ready: rd.Ready,
		SeatTotal: rd.SeatTotal, SeatsWithCategory: rd.SeatsWithCategory, Problems: rd.Problems,
	})
}

func (s *Server) publishEvent(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := s.events.Publish(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, publishResultDTO{
		Event: newEventDTO(res.Event), SeatsOnSale: res.SeatsOnSale, AlreadyPublished: res.AlreadyPublished,
	})
}

func (s *Server) cancelEvent(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := s.events.Cancel(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cancelResultDTO{
		Event: newEventDTO(res.Event), TicketsVoided: res.TicketsVoided, BookingsCancelled: res.BookingsCancelled,
		RefundsQueued: res.RefundsQueued, RefundTotalCents: res.RefundTotalCents, HoldsReleased: res.HoldsReleased,
	})
}

func (s *Server) listAdminEventCategories(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	cats, err := s.events.AdminCategories(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeCategories(w, cats)
}

type createCategoryBody struct {
	Name string `json:"name" validate:"required"`
	// PriceCents is a pointer so a missing price is refused instead of read as free.
	PriceCents  *int64  `json:"price_cents" validate:"required,min=0"`
	ColorHex    string  `json:"color_hex" validate:"required,len=7,hexcolor"`
	Description *string `json:"description"`
}

func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body createCategoryBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	var checks fieldChecks
	checks.required("name", &body.Name, domain.CategoryNameMaxLength)
	checks.text("description", &body.Description, domain.CategoryDescMaxLength)
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	cat, err := s.events.CreateCategory(r.Context(), id, events.CategoryInput{
		Name: body.Name, PriceCents: *body.PriceCents, ColorHex: body.ColorHex, Description: body.Description,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newCategoryDTO(cat))
}

type updateCategoryBody struct {
	Name        *string          `json:"name"`
	PriceCents  *int64           `json:"price_cents" validate:"omitempty,min=0"`
	ColorHex    *string          `json:"color_hex" validate:"omitempty,len=7,hexcolor"`
	Description optional[string] `json:"description"`
	Confirm     *bool            `json:"confirm"`
}

func (s *Server) updateCategory(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "categoryId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body updateCategoryBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	if body.Name == nil && body.PriceCents == nil && body.ColorHex == nil && !body.Description.Set && body.Confirm == nil {
		WriteError(w, r, errEmptyPatch)
		return
	}
	var checks fieldChecks
	if body.Name != nil {
		checks.required("name", body.Name, domain.CategoryNameMaxLength)
	}
	checks.text("description", &body.Description.Value, domain.CategoryDescMaxLength)
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	cat, err := s.events.UpdateCategory(r.Context(), id, events.CategoryUpdate{
		Name: body.Name, PriceCents: body.PriceCents, ColorHex: body.ColorHex,
		Description: body.Description.nullable(), Confirm: body.Confirm != nil && *body.Confirm,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newCategoryDTO(cat))
}

func (s *Server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "categoryId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.events.DeleteCategory(r.Context(), id); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getEventAssignments(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	a, err := s.events.Assignments(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAssignmentsDTO(a))
}

type assignCategoryBody struct {
	// CategoryID null clears the listed blocks and rows.
	CategoryID optional[int64] `json:"category_id"`
	BlockIDs   *[]int64        `json:"block_ids" validate:"required,max=1000,unique,dive,min=1"`
	RowIDs     *[]int64        `json:"row_ids" validate:"required,max=5000,unique,dive,min=1"`
}

func (s *Server) assignCategory(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body assignCategoryBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	var checks fieldChecks
	if !body.CategoryID.Set {
		checks.add("category_id", "required", "Send a category, or null to clear.")
	} else if body.CategoryID.Value != nil && *body.CategoryID.Value < 1 {
		checks.add("category_id", "id", "Must be a positive whole number.")
	}
	if len(*body.BlockIDs) == 0 && len(*body.RowIDs) == 0 {
		checks.add("block_ids", "min", "List at least one block or row.")
		checks.add("row_ids", "min", "List at least one block or row.")
	}
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	a, err := s.events.Assign(r.Context(), id, events.AssignInput{
		CategoryID: body.CategoryID.Value, BlockIDs: *body.BlockIDs, RowIDs: *body.RowIDs,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAssignmentsDTO(a))
}

// --- Sales ------------------------------------------------------------------------------

func (s *Server) getEventSales(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	bucket, err := QueryEnum(r, "bucket", events.BucketHour, events.BucketDay)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	from, err := QueryTime(r, "from")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	to, err := QueryTime(r, "to")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	sales, err := s.events.Sales(r.Context(), id, events.SalesQuery{Bucket: bucket, From: from, To: to})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newSalesSummaryDTO(sales))
}

// bookingStatuses is the contract's BookingStatus enum.
var bookingStatuses = []string{"PENDING_PAYMENT", "PAID", "FAILED", "EXPIRED", "CANCELLED", "REFUND_REQUIRED", "REFUNDED"}

func (s *Server) listEventBookings(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	raw, err := QueryEnum(r, "status", bookingStatuses...)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var status *string
	if raw != "" {
		status = &raw
	}
	var after *events.BookingCursor
	var pos events.BookingCursor
	if present, err := DecodeCursor(r, &pos); err != nil {
		WriteError(w, r, err)
		return
	} else if present {
		if pos.ID < 1 || pos.CreatedAt.IsZero() {
			WriteError(w, r, errBadCursor)
			return
		}
		after = &pos
	}
	list, next, err := s.events.Bookings(r.Context(), id, status, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]adminBookingDTO, 0, len(list))
	for _, b := range list {
		items = append(items, newAdminBookingDTO(b))
	}
	writeJSON(w, http.StatusOK, pageDTO[adminBookingDTO]{Items: items, NextCursor: EncodeCursor(next)})
}
