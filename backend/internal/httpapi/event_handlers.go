package httpapi

import (
	"net/http"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
)

func eventStatusNames(statuses []domain.EventStatus) []string {
	out := make([]string, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, string(s))
	}
	return out
}

// eventListFilter reads the query of both event lists: status, from, to, limit, cursor.
func eventListFilter(r *http.Request, statuses []domain.EventStatus) (events.ListFilter, error) {
	var f events.ListFilter
	var err error
	if f.Limit, err = PageLimit(r); err != nil {
		return f, err
	}
	raw, err := QueryEnum(r, "status", eventStatusNames(statuses)...)
	if err != nil {
		return f, err
	}
	if raw != "" {
		status := domain.EventStatus(raw)
		f.Status = &status
	}
	if f.From, err = QueryTime(r, "from"); err != nil {
		return f, err
	}
	if f.To, err = QueryTime(r, "to"); err != nil {
		return f, err
	}
	var pos events.EventCursor
	present, err := DecodeCursor(r, &pos)
	if err != nil {
		return f, err
	}
	if present {
		if pos.ID < 1 || pos.KickoffAt.IsZero() {
			return f, errBadCursor
		}
		f.After = &pos
	}
	return f, nil
}

func writeEventPage(w http.ResponseWriter, list []events.Event, next *events.EventCursor) {
	items := make([]eventDTO, 0, len(list))
	for _, e := range list {
		items = append(items, newEventDTO(e))
	}
	writeJSON(w, http.StatusOK, pageDTO[eventDTO]{Items: items, NextCursor: EncodeCursor(next)})
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	f, err := eventListFilter(r, domain.PublicEventStatuses)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	list, next, err := s.events.ListPublic(r.Context(), f)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeEventPage(w, list, next)
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	ev, err := s.events.GetPublic(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newEventDTO(ev))
}

func writeCategories(w http.ResponseWriter, cats []events.Category) {
	items := make([]categoryDTO, 0, len(cats))
	for _, c := range cats {
		items = append(items, newCategoryDTO(c))
	}
	writeJSON(w, http.StatusOK, listDTO[categoryDTO]{Items: items})
}

func (s *Server) listEventCategories(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	cats, err := s.events.PublicCategories(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeCategories(w, cats)
}

// --- Seat map ---------------------------------------------------------------------------

func (s *Server) getEventMap(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	m, err := s.events.StadiumMap(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newStadiumMapDTO(m))
}

func (s *Server) listStandBlocks(w http.ResponseWriter, r *http.Request) {
	eventID, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	standID, err := PathID(r, "standId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	sb, err := s.events.StandBlocks(r.Context(), eventID, standID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newStandBlocksDTO(sb))
}

// maxMarkerLength is the contract's bound on ?since=.
const maxMarkerLength = 128

func (s *Server) getBlockSeats(w http.ResponseWriter, r *http.Request) {
	eventID, err := PathID(r, "eventId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	blockID, err := PathID(r, "blockId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	since := r.URL.Query().Get("since")
	if r.URL.Query().Has("since") && (since == "" || len(since) > maxMarkerLength) {
		// A marker of the wrong size is a malformed request. One of the right size that the
		// server cannot read is not an error: it is answered with the full block.
		WriteError(w, r, domain.InvalidField("since", "len", "Use the marker value from the previous answer."))
		return
	}
	// The token is optional here. With one, the caller's own holds come back as "M".
	var viewerID int64
	if p, ok := PrincipalFrom(r.Context()); ok {
		viewerID = p.UserID
	}
	bs, err := s.events.BlockSeats(r.Context(), eventID, blockID, viewerID, since)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	// The answer depends on who asks and is stale within seconds.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, newBlockSeatsDTO(bs))
}
