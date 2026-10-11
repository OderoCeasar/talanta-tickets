package httpapi

import (
	"net/http"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

type createInvitationBody struct {
	Email string `json:"email" validate:"required,max=254,email"`
	Role  string `json:"role" validate:"required,oneof=ORGANISER ADMIN"`
}

func (s *Server) listStaffUsers(w http.ResponseWriter, r *http.Request) {
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	roleRaw, err := QueryEnum(r, "role", string(domain.RoleOrganiser), string(domain.RoleAdmin))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var role *domain.Role
	if roleRaw != "" {
		v := domain.Role(roleRaw)
		role = &v
	}
	var after *auth.StaffCursor
	var pos auth.StaffCursor
	if present, err := DecodeCursor(r, &pos); err != nil {
		WriteError(w, r, err)
		return
	} else if present {
		after = &pos
	}

	users, next, err := s.auth.ListStaff(r.Context(), role, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	items := make([]staffUserDTO, 0, len(users))
	for _, u := range users {
		items = append(items, staffUserDTO{
			ID: u.ID, Name: u.Name, Email: u.Email, Role: u.Role,
			TOTPEnabled: u.TOTPEnabled, CreatedAt: ts(u.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, pageDTO[staffUserDTO]{Items: items, NextCursor: EncodeCursor(next)})
}

func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request) {
	limit, err := PageLimit(r)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	statusRaw, err := QueryEnum(r, "status",
		string(domain.InvitationPending), string(domain.InvitationAccepted),
		string(domain.InvitationExpired), string(domain.InvitationRevoked))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	var status *domain.InvitationStatus
	if statusRaw != "" {
		v := domain.InvitationStatus(statusRaw)
		status = &v
	}
	var after *auth.InvitationCursor
	var pos auth.InvitationCursor
	if present, err := DecodeCursor(r, &pos); err != nil {
		WriteError(w, r, err)
		return
	} else if present {
		after = &pos
	}

	invitations, next, err := s.auth.ListInvitations(r.Context(), status, limit, after)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	now := time.Now()
	items := make([]invitationDTO, 0, len(invitations))
	for _, inv := range invitations {
		items = append(items, newInvitationDTO(inv, now))
	}
	writeJSON(w, http.StatusOK, pageDTO[invitationDTO]{Items: items, NextCursor: EncodeCursor(next)})
}

func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	var body createInvitationBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	inv, err := s.auth.CreateInvitation(r.Context(), MustPrincipal(r.Context()).UserID, body.Email, domain.Role(body.Role))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	// The answer carries the one-time link.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, newInvitationDTO(inv, time.Now()))
}

func (s *Server) resendInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "invitationId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	inv, err := s.auth.ResendInvitation(r.Context(), id)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, newInvitationDTO(inv, time.Now()))
}

func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r, "invitationId")
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.auth.RevokeInvitation(r.Context(), id); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
