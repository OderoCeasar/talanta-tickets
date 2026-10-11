package httpapi

import (
	"net/http"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

func (s *Server) listVenues(w http.ResponseWriter, r *http.Request) {
	venues, err := s.venues.List(r.Context())
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]venueDTO, 0, len(venues))
	for _, v := range venues {
		items = append(items, newVenueDTO(v))
	}
	writeJSON(w, http.StatusOK, listDTO[venueDTO]{Items: items})
}

func (s *Server) getVenue(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "venueId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	d, err := s.venues.Get(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newVenueDetailDTO(d))
}

// Numbers that must be present are pointers: a plain float64 cannot tell "0" from "missing",
// and an origin of 0 is a real position.
type createBlockBody struct {
	StandID     int64    `json:"stand_id" validate:"required,min=1"`
	Code        string   `json:"code" validate:"required,max=12,blockcode"`
	Label       string   `json:"label" validate:"required"`
	OriginX     *float64 `json:"origin_x" validate:"required"`
	OriginY     *float64 `json:"origin_y" validate:"required"`
	RotationDeg *float64 `json:"rotation_deg" validate:"omitempty,min=-360,max=360"`
	ArcRadius   *float64 `json:"arc_radius" validate:"omitempty,gt=0"`
	SeatPitchX  *float64 `json:"seat_pitch_x" validate:"required,gt=0"`
	SeatPitchY  *float64 `json:"seat_pitch_y" validate:"required,gt=0"`
}

func (s *Server) createBlock(w http.ResponseWriter, r *http.Request) {
	venueID, err := PathID(r, "venueId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body createBlockBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	var checks fieldChecks
	checks.required("label", &body.Label, domain.BlockLabelMaxLength)
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	in := venue.CreateBlockInput{
		VenueID: venueID, StandID: body.StandID, Code: body.Code, Label: body.Label,
		OriginX: *body.OriginX, OriginY: *body.OriginY, ArcRadius: body.ArcRadius,
		SeatPitchX: *body.SeatPitchX, SeatPitchY: *body.SeatPitchY,
	}
	if body.RotationDeg != nil {
		in.RotationDeg = *body.RotationDeg
	}
	b, err := s.venues.CreateBlock(r.Context(), in)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newBlockDTO(b))
}

type updateBlockBody struct {
	Code        *string           `json:"code" validate:"omitempty,max=12,blockcode"`
	Label       *string           `json:"label"`
	OriginX     *float64          `json:"origin_x"`
	OriginY     *float64          `json:"origin_y"`
	RotationDeg *float64          `json:"rotation_deg" validate:"omitempty,min=-360,max=360"`
	ArcRadius   optional[float64] `json:"arc_radius"`
	SeatPitchX  *float64          `json:"seat_pitch_x" validate:"omitempty,gt=0"`
	SeatPitchY  *float64          `json:"seat_pitch_y" validate:"omitempty,gt=0"`
}

func (s *Server) updateBlock(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "blockId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body updateBlockBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	if body.Code == nil && body.Label == nil && body.OriginX == nil && body.OriginY == nil && body.RotationDeg == nil &&
		!body.ArcRadius.Set && body.SeatPitchX == nil && body.SeatPitchY == nil {
		WriteError(w, r, errEmptyPatch)
		return
	}
	var checks fieldChecks
	if body.Label != nil {
		checks.required("label", body.Label, domain.BlockLabelMaxLength)
	}
	if body.ArcRadius.Value != nil && !(*body.ArcRadius.Value > 0) {
		checks.add("arc_radius", "gt", "Must be more than 0.")
	}
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	b, err := s.venues.UpdateBlock(r.Context(), id, venue.UpdateBlockInput{
		Code: body.Code, Label: body.Label, OriginX: body.OriginX, OriginY: body.OriginY, RotationDeg: body.RotationDeg,
		ArcRadiusSet: body.ArcRadius.Set, ArcRadius: body.ArcRadius.Value,
		SeatPitchX: body.SeatPitchX, SeatPitchY: body.SeatPitchY,
	})
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newBlockDTO(b))
}

func (s *Server) deleteBlock(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "blockId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.venues.DeleteBlock(r.Context(), id); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type generateSeatsBody struct {
	Rows               int       `json:"rows" validate:"required,min=1,max=200"`
	SeatsPerRow        int       `json:"seats_per_row" validate:"required,min=1,max=500"`
	RowLabelScheme     string    `json:"row_label_scheme" validate:"omitempty,oneof=ALPHA NUMERIC"`
	NumberingDirection string    `json:"numbering_direction" validate:"omitempty,oneof=LTR RTL"`
	RowOffsets         []float64 `json:"row_offsets" validate:"max=200"`
	DryRun             bool      `json:"dry_run"`
}

func (s *Server) generateBlockSeats(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "blockId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body generateSeatsBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	in := venue.GenerateInput{
		Rows: body.Rows, SeatsPerRow: body.SeatsPerRow, RowOffsets: body.RowOffsets, DryRun: body.DryRun,
		Labels: seatmap.LabelsAlpha, Numbering: seatmap.LeftToRight,
	}
	if body.RowLabelScheme != "" {
		in.Labels = seatmap.LabelScheme(body.RowLabelScheme)
	}
	if body.NumberingDirection != "" {
		in.Numbering = seatmap.Direction(body.NumberingDirection)
	}
	layout, err := s.venues.GenerateSeats(r.Context(), id, in)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newBlockLayoutDTO(layout))
}

func (s *Server) getBlockLayout(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "blockId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	layout, err := s.venues.BlockLayout(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newBlockLayoutDTO(layout))
}

func (s *Server) listVenueTemplates(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "venueId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	templates, err := s.events.Templates(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]templateDTO, 0, len(templates))
	for _, t := range templates {
		items = append(items, newTemplateDTO(t))
	}
	writeJSON(w, http.StatusOK, listDTO[templateDTO]{Items: items})
}

type createTemplateBody struct {
	Name    string `json:"name" validate:"required"`
	EventID int64  `json:"event_id" validate:"required,min=1"`
}

func (s *Server) createVenueTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "venueId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var body createTemplateBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	var checks fieldChecks
	checks.required("name", &body.Name, domain.TemplateNameMaxLength)
	if err := checks.err(); err != nil {
		WriteError(w, r, err)
		return
	}
	t, err := s.events.CreateTemplate(r.Context(), id, body.Name, body.EventID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newTemplateDTO(t))
}
