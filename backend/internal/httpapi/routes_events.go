package httpapi

import (
	"github.com/go-chi/chi/v5"
)

// publicEventRoutes: no token needed. Paths are relative to /v1.
func (s *Server) publicEventRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		if s.limits.PublicReadPerIP.Limit > 0 {
			r.Use(s.LimitByIP("public-read", s.limits.PublicReadPerIP))
		}
		r.Get("/events", s.listEvents)
		r.Get("/events/{eventId}", s.getEvent)
		r.Get("/events/{eventId}/categories", s.listEventCategories)
		r.Get("/events/{eventId}/map", s.getEventMap)
		r.Get("/events/{eventId}/stands/{standId}/blocks", s.listStandBlocks)
		r.Get("/events/{eventId}/blocks/{blockId}/seats", s.getBlockSeats)
	})
}

// staffEventRoutes: ORGANISER and ADMIN. Paths are relative to /v1/admin.
func (s *Server) staffEventRoutes(r chi.Router) {
	r.Get("/venues", s.listVenues)
	r.Get("/venues/{venueId}", s.getVenue)
	r.Post("/venues/{venueId}/blocks", s.createBlock)
	r.Get("/venues/{venueId}/templates", s.listVenueTemplates)
	r.Post("/venues/{venueId}/templates", s.createVenueTemplate)

	r.Patch("/blocks/{blockId}", s.updateBlock)
	r.Delete("/blocks/{blockId}", s.deleteBlock)
	r.Post("/blocks/{blockId}/generate-seats", s.generateBlockSeats)
	r.Get("/blocks/{blockId}/seats", s.getBlockLayout)

	r.Get("/events", s.listAdminEvents)
	r.Post("/events", s.createEvent)
	r.Get("/events/{eventId}", s.getAdminEvent)
	r.Patch("/events/{eventId}", s.updateEvent)
	r.Get("/events/{eventId}/readiness", s.getEventReadiness)
	// Publish copies one inventory row per seat (about 60,000) and takes longer than the
	// router-wide request timeout allows, so it runs under its own.
	r.With(routeTimeout(s.publishTimeout)).Post("/events/{eventId}/publish", s.publishEvent)
	r.Post("/events/{eventId}/cancel", s.cancelEvent)
	r.Get("/events/{eventId}/categories", s.listAdminEventCategories)
	r.Post("/events/{eventId}/categories", s.createCategory)
	r.Get("/events/{eventId}/assignments", s.getEventAssignments)
	r.Post("/events/{eventId}/assign-category", s.assignCategory)
	r.Get("/events/{eventId}/sales", s.getEventSales)
	r.Get("/events/{eventId}/bookings", s.listEventBookings)

	r.Patch("/categories/{categoryId}", s.updateCategory)
	r.Delete("/categories/{categoryId}", s.deleteCategory)
}
