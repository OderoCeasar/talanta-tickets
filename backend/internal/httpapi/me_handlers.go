package httpapi

import "net/http"

type updateMeBody struct {
	Name string `json:"name" validate:"required,max=100"`
}

type changePasswordBody struct {
	CurrentPassword string `json:"current_password" validate:"required,max=256"`
	NewPassword     string `json:"new_password" validate:"required,min=10,max=256"`
}

type passwordConfirmBody struct {
	Password string `json:"password" validate:"required,max=256"`
}

type totpCodeBody struct {
	Code string `json:"code" validate:"required,len=6,number"`
}

// Every handler here acts on the caller's own account: the user ID comes from the access
// token and never from the request.

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	user, err := s.auth.Me(r.Context(), MustPrincipal(r.Context()).UserID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserDTO(user))
}

func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	var body updateMeBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	user, err := s.auth.UpdateName(r.Context(), MustPrincipal(r.Context()).UserID, body.Name)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserDTO(user))
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var body changePasswordBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.auth.ChangePassword(r.Context(), MustPrincipal(r.Context()), body.CurrentPassword, body.NewPassword); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setupTOTP(w http.ResponseWriter, r *http.Request) {
	setup, err := s.auth.SetupTOTP(r.Context(), MustPrincipal(r.Context()).UserID)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	// The secret must not be kept by any cache between here and the user.
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, totpSetupDTO{Secret: setup.Secret, OtpauthURL: setup.URL})
}

func (s *Server) enableTOTP(w http.ResponseWriter, r *http.Request) {
	var body totpCodeBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	user, err := s.auth.EnableTOTP(r.Context(), MustPrincipal(r.Context()).UserID, body.Code)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserDTO(user))
}

func (s *Server) disableTOTP(w http.ResponseWriter, r *http.Request) {
	var body passwordConfirmBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	user, err := s.auth.DisableTOTP(r.Context(), MustPrincipal(r.Context()).UserID, body.Password)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserDTO(user))
}
