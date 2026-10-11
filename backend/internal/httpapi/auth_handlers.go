package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// Request bodies. The rules mirror the contract's schemas; the service checks what a tag
// cannot (phone format after normalising, trimmed names).

type otpRequestBody struct {
	Phone string `json:"phone" validate:"required,max=32"`
}

type otpVerifyBody struct {
	Phone string `json:"phone" validate:"required,max=32"`
	Code  string `json:"code" validate:"required,len=6,number"`
}

type passwordLoginBody struct {
	Email    string `json:"email" validate:"required,max=254,email"`
	Password string `json:"password" validate:"required,max=256"`
}

type totpVerifyBody struct {
	MFAToken string `json:"mfa_token" validate:"required,max=1024"`
	Code     string `json:"code" validate:"required,len=6,number"`
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token" validate:"required,max=512"`
}

type acceptInvitationBody struct {
	Name     string `json:"name" validate:"required,max=100"`
	Password string `json:"password" validate:"required,min=10,max=256"`
}

func (s *Server) requestOTP(w http.ResponseWriter, r *http.Request) {
	var body otpRequestBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := s.auth.RequestOTP(r.Context(), body.Phone, ClientIP(r.Context()))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, otpRequestedDTO{
		ExpiresInSeconds:   int(res.ExpiresIn / time.Second),
		ResendAfterSeconds: int(res.ResendAfter / time.Second),
		DevCode:            res.DevCode,
	})
}

func (s *Server) verifyOTP(w http.ResponseWriter, r *http.Request) {
	var body otpVerifyBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := s.auth.VerifyOTP(r.Context(), body.Phone, body.Code)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionDTO(session))
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	var body passwordLoginBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	res, err := s.auth.Login(r.Context(), body.Email, body.Password)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	if res.MFA != nil {
		writeJSON(w, http.StatusOK, mfaChallengeDTO{
			MFARequired:      true,
			MFAToken:         res.MFA.Token,
			ExpiresInSeconds: int(res.MFA.ExpiresIn / time.Second),
		})
		return
	}
	writeJSON(w, http.StatusOK, loginSessionDTO{authSessionDTO: newAuthSessionDTO(*res.Session)})
}

func (s *Server) verifyTOTP(w http.ResponseWriter, r *http.Request) {
	var body totpVerifyBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := s.auth.VerifyTOTPLogin(r.Context(), body.MFAToken, body.Code)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionDTO(session))
}

func (s *Server) refreshTokens(w http.ResponseWriter, r *http.Request) {
	var body refreshBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := s.auth.Refresh(r.Context(), body.RefreshToken)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthSessionDTO(session))
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var body refreshBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	if err := s.auth.Logout(r.Context(), body.RefreshToken); err != nil {
		WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getInvitation(w http.ResponseWriter, r *http.Request) {
	inv, err := s.auth.PreviewInvitation(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, invitationPreviewDTO{Email: inv.Email, Role: inv.Role, ExpiresAt: ts(inv.ExpiresAt)})
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	// A dead link is reported before the body is looked at: "this link has expired" is the
	// useful answer even when the form was also filled in wrongly.
	if _, err := s.auth.PreviewInvitation(r.Context(), token); err != nil {
		WriteError(w, r, err)
		return
	}
	var body acceptInvitationBody
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, r, err)
		return
	}
	session, err := s.auth.AcceptInvitation(r.Context(), token, body.Name, body.Password)
	if err != nil {
		WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newAuthSessionDTO(session))
}
