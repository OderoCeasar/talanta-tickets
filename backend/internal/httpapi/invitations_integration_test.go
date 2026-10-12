//go:build integration

package httpapi_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

const invitePrefix = "https://admin.talanta.test/invite/"

// invite creates an invitation as the given admin and returns it with the token from its link.
func (e *env) invite(adminToken, email, role string) (invitationJSON, string) {
	e.t.Helper()
	rec := e.do("POST", "/admin/invitations", map[string]string{"email": email, "role": role}, adminToken)
	require.Equal(e.t, http.StatusCreated, rec.Code, rec.Body.String())
	inv := decode[invitationJSON](e.t, rec)
	require.NotNil(e.t, inv.InviteURL)
	require.True(e.t, strings.HasPrefix(*inv.InviteURL, invitePrefix), *inv.InviteURL)
	return inv, strings.TrimPrefix(*inv.InviteURL, invitePrefix)
}

func (e *env) accept(token, name, password string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/auth/invitations/"+token+"/accept", map[string]string{"name": name, "password": password}, "")
}

func (e *env) findInvitation(adminToken string, id int64) invitationJSON {
	e.t.Helper()
	rec := e.do("GET", "/admin/invitations?limit=100", nil, adminToken)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	for _, inv := range decode[page[invitationJSON]](e.t, rec).Items {
		if inv.ID == id {
			return inv
		}
	}
	e.t.Fatalf("invitation %d is not in the list", id)
	return invitationJSON{}
}

// The whole journey: admin invites → the link is previewed → accepted → the new user signs
// in with their password → the invitation shows ACCEPTED → the link is dead.
func TestInvitation_HappyPath(t *testing.T) {
	e := newEnv(t)
	admin, adminSession := e.staffSession(domain.RoleAdmin)
	email := randEmail(t, "new.organiser")
	const password = "a-long-pass-phrase"

	// 1. Invite. Mixed case and spaces in the address are normalised.
	rec := e.do("POST", "/admin/invitations", map[string]string{"email": "  " + strings.ToUpper(email[:3]) + email[3:] + " ", "role": "ORGANISER"}, adminSession.AccessToken)
	require.Equal(t, http.StatusBadRequest, rec.Code, "an address with spaces round it is not an email: %s", rec.Body.String())

	inv, token := e.invite(adminSession.AccessToken, strings.ToUpper(email[:3])+email[3:], "ORGANISER")
	assert.Equal(t, email, inv.Email)
	assert.Equal(t, "ORGANISER", inv.Role)
	assert.Equal(t, "PENDING", inv.Status)
	assert.Nil(t, inv.AcceptedAt)
	assert.Equal(t, admin.ID, inv.InvitedBy.ID)
	assert.Equal(t, *admin.Email, *inv.InvitedBy.Email)
	assert.Equal(t, *admin.Name, *inv.InvitedBy.Name)
	expires, err := time.Parse(time.RFC3339, inv.ExpiresAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(72*time.Hour), expires, time.Minute)

	// The token is 32 random bytes in base64url, and only its SHA-256 is stored.
	assert.Regexp(t, `^[A-Za-z0-9_-]{43}$`, token)
	var storedHash []byte
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT token_hash FROM invitations WHERE id = $1`, inv.ID).Scan(&storedHash))
	want := sha256.Sum256([]byte(token))
	assert.Equal(t, want[:], storedHash)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM invitations WHERE encode(token_hash, 'escape') = $1`, token))

	// The email went through the Mailer and carries the same link.
	mails := e.mail.all()
	require.Len(t, mails, 1)
	assert.Equal(t, email, mails[0].To)
	assert.Contains(t, mails[0].Text, *inv.InviteURL)
	assert.Contains(t, mails[0].Text, "an organiser")
	// The link is not written to the log by the service.
	assert.NotContains(t, e.logs.String(), token)

	// The list never shows the link again.
	listed := e.findInvitation(adminSession.AccessToken, inv.ID)
	assert.Nil(t, listed.InviteURL)
	assert.Equal(t, "PENDING", listed.Status)

	// 2. The set-password page previews the link.
	rec = e.do("GET", "/auth/invitations/"+token, nil, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, fmt.Sprintf(`{"email":%q,"role":"ORGANISER","expires_at":%q}`, email, inv.ExpiresAt), rec.Body.String())
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM users WHERE lower(email) = $1`, email), "previewing creates nothing")

	// 3. Accept: account created, signed in.
	rec = e.accept(token, " Wanjiru  Kamau ", password)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	session := decode[sessionJSON](t, rec)
	assert.Equal(t, email, *session.User.Email)
	assert.Equal(t, "ORGANISER", session.User.Role, "the role comes from the invitation, not from the request")
	assert.Equal(t, "Wanjiru Kamau", *session.User.Name)
	assert.Nil(t, session.User.Phone)
	assert.False(t, session.User.TOTPEnabled)
	assert.NotContains(t, rec.Body.String(), password)
	require.Equal(t, http.StatusOK, e.do("GET", "/me", nil, session.AccessToken).Code)
	require.Equal(t, http.StatusOK, e.refresh(session.RefreshToken).Code)

	// The password is stored as argon2id.
	var hash string
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT password_hash FROM users WHERE id = $1`, session.User.ID).Scan(&hash))
	assert.True(t, strings.HasPrefix(hash, "$argon2id$"), hash)

	// 4. The new user signs in with the password they chose…
	signedIn := e.login(email, password)
	assert.Equal(t, session.User.ID, signedIn.User.ID)
	// …is an organiser: allowed into /admin, but not to invite.
	assert.Equal(t, http.StatusNotFound, httptestDo(e, "GET", "/admin/no-such-thing", signedIn.AccessToken).Code)
	wantError(t, e.do("GET", "/admin/invitations", nil, signedIn.AccessToken), http.StatusForbidden, domain.CodeForbidden)

	// 5. The invitation shows ACCEPTED.
	accepted := e.findInvitation(adminSession.AccessToken, inv.ID)
	assert.Equal(t, "ACCEPTED", accepted.Status)
	require.NotNil(t, accepted.AcceptedAt)
	assert.Nil(t, accepted.InviteURL)

	// And the new user is in the staff list.
	// The tests share one database and by now it may hold more organisers than one page.
	found, path := false, "/admin/users?limit=100&role=ORGANISER"
	for cursor := ""; !found; {
		rec = e.do("GET", path+cursor, nil, adminSession.AccessToken)
		require.Equal(t, http.StatusOK, rec.Code)
		found = strings.Contains(rec.Body.String(), email)
		next := decode[page[userJSON]](t, rec).NextCursor
		if next == nil {
			break
		}
		cursor = "&cursor=" + *next
	}
	assert.True(t, found, "the new organiser %s is in the staff list", email)

	// 6. The token cannot be used again: not to preview, not to accept.
	wantError(t, e.do("GET", "/auth/invitations/"+token, nil, ""), http.StatusNotFound, domain.CodeInvitationInvalid)
	wantError(t, e.accept(token, "Someone Else", "another-long-password"), http.StatusNotFound, domain.CodeInvitationInvalid)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM users WHERE lower(email) = $1`, email))
	e.login(email, password) // the password was not replaced
}

func TestInvitation_AdminRoleInvitation(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	email := randEmail(t, "new.admin")

	_, token := e.invite(adminSession.AccessToken, email, "ADMIN")
	assert.Contains(t, e.mail.all()[0].Text, "an admin")
	rec := e.accept(token, "Second Admin", "a-long-pass-phrase")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	s := decode[sessionJSON](t, rec)
	assert.Equal(t, "ADMIN", s.User.Role)
	require.Equal(t, http.StatusOK, e.do("GET", "/admin/invitations", nil, s.AccessToken).Code)
}

func TestInvitation_ExpiredRevokedAndUnknownTokens(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	admin := adminSession.AccessToken

	check := func(t *testing.T, token string, status int, code domain.Code, email string) {
		t.Helper()
		wantError(t, e.do("GET", "/auth/invitations/"+token, nil, ""), status, code)
		wantError(t, e.accept(token, "Some Name", "a-long-pass-phrase"), status, code)
		// A dead link says so even when the form is also wrong.
		wantError(t, e.accept(token, "", "short"), status, code)
		if email != "" {
			assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM users WHERE lower(email) = $1`, email), "no account from a dead link")
		}
	}

	t.Run("expired", func(t *testing.T) {
		email := randEmail(t, "late")
		inv, token := e.invite(admin, email, "ORGANISER")
		exec(t, `UPDATE invitations SET expires_at = now() - interval '1 second' WHERE id = $1`, inv.ID)
		check(t, token, http.StatusGone, domain.CodeInvitationExpired, email)
		assert.Equal(t, "EXPIRED", e.findInvitation(admin, inv.ID).Status)
	})

	t.Run("expires exactly at 72 hours", func(t *testing.T) {
		email := randEmail(t, "edge")
		_, token := e.invite(admin, email, "ORGANISER")
		other := newEnv(t) // same database, its own clock
		other.clock.Advance(72*time.Hour - time.Minute)
		require.Equal(t, http.StatusOK, other.do("GET", "/auth/invitations/"+token, nil, "").Code)
		other.clock.Advance(2 * time.Minute)
		wantError(t, other.do("GET", "/auth/invitations/"+token, nil, ""), http.StatusGone, domain.CodeInvitationExpired)
	})

	t.Run("revoked", func(t *testing.T) {
		email := randEmail(t, "revoked")
		inv, token := e.invite(admin, email, "ORGANISER")
		require.Equal(t, http.StatusNoContent, e.do("DELETE", "/admin/invitations/"+strconv.FormatInt(inv.ID, 10), nil, admin).Code)
		check(t, token, http.StatusNotFound, domain.CodeInvitationInvalid, email)
		assert.Equal(t, "REVOKED", e.findInvitation(admin, inv.ID).Status)
	})

	t.Run("unknown and malformed", func(t *testing.T) {
		check(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", http.StatusNotFound, domain.CodeInvitationInvalid, "")
		// Outside the contract's token pattern: still a plain 404, checked without the
		// contract helper because the path itself breaks the contract.
		for _, token := range []string{"short", "has.dots.in.it.has.dots.in.it.has.dots", strings.Repeat("A", 200), "%27%20OR%201=1--"} {
			rec := httptestDo(e, "GET", "/auth/invitations/"+token, "")
			assert.Equal(t, http.StatusNotFound, rec.Code, token)
			assert.Contains(t, rec.Body.String(), "INVITATION_INVALID", token)
		}
	})
}

func TestInvitation_Conflicts(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	admin := adminSession.AccessToken
	post := func(email, role string) *httptest.ResponseRecorder {
		return e.do("POST", "/admin/invitations", map[string]string{"email": email, "role": role}, admin)
	}

	t.Run("email already has an account", func(t *testing.T) {
		existing := e.newStaff(domain.RoleOrganiser)
		wantError(t, post(*existing.Email, "ORGANISER"), http.StatusConflict, domain.CodeEmailAlreadyRegistered)
		wantError(t, post(strings.ToUpper(*existing.Email), "ADMIN"), http.StatusConflict, domain.CodeEmailAlreadyRegistered)
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM invitations WHERE email = $1`, *existing.Email))
	})

	t.Run("email already has a pending invitation", func(t *testing.T) {
		email := randEmail(t, "twice")
		first, firstToken := e.invite(admin, email, "ORGANISER")
		wantError(t, post(email, "ORGANISER"), http.StatusConflict, domain.CodeInvitationAlreadyPending)
		wantError(t, post(strings.ToUpper(email), "ADMIN"), http.StatusConflict, domain.CodeInvitationAlreadyPending)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM invitations WHERE email = $1`, email))
		// The first link still works.
		require.Equal(t, http.StatusOK, e.do("GET", "/auth/invitations/"+firstToken, nil, "").Code)
		_ = first
	})

	t.Run("a lapsed invitation is revoked automatically when re-inviting", func(t *testing.T) {
		email := randEmail(t, "lapsed")
		old, oldToken := e.invite(admin, email, "ORGANISER")
		exec(t, `UPDATE invitations SET expires_at = now() - interval '1 hour' WHERE id = $1`, old.ID)

		fresh, freshToken := e.invite(admin, email, "ADMIN")
		assert.NotEqual(t, old.ID, fresh.ID)
		assert.Equal(t, "REVOKED", e.findInvitation(admin, old.ID).Status)
		assert.Equal(t, "PENDING", e.findInvitation(admin, fresh.ID).Status)
		wantError(t, e.do("GET", "/auth/invitations/"+oldToken, nil, ""), http.StatusNotFound, domain.CodeInvitationInvalid)
		require.Equal(t, http.StatusOK, e.do("GET", "/auth/invitations/"+freshToken, nil, "").Code)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM invitations WHERE email = $1 AND accepted_at IS NULL AND revoked_at IS NULL`, email))
	})

	t.Run("after a revoke the address can be invited again", func(t *testing.T) {
		email := randEmail(t, "again")
		inv, _ := e.invite(admin, email, "ORGANISER")
		require.Equal(t, http.StatusNoContent, e.do("DELETE", "/admin/invitations/"+strconv.FormatInt(inv.ID, 10), nil, admin).Code)
		e.invite(admin, email, "ORGANISER")
	})

	t.Run("an account appears between invite and accept", func(t *testing.T) {
		email := randEmail(t, "raced")
		inv, token := e.invite(admin, email, "ORGANISER")
		_, err := e.q.CreateStaffUser(context.Background(), store.CreateStaffUserParams{Email: email, Role: "ORGANISER", PasswordHash: staffPasswordHash})
		require.NoError(t, err)

		wantError(t, e.accept(token, "Late Comer", "a-long-pass-phrase"), http.StatusConflict, domain.CodeEmailAlreadyRegistered)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM users WHERE lower(email) = $1`, email))
		// The failed accept rolled back whole: the invitation is not marked accepted.
		assert.Equal(t, "PENDING", e.findInvitation(admin, inv.ID).Status)
		wantError(t, e.do("POST", "/admin/invitations/"+strconv.FormatInt(inv.ID, 10)+"/resend", nil, admin), http.StatusConflict, domain.CodeEmailAlreadyRegistered)
	})

	t.Run("two admins invite the same address at once", func(t *testing.T) {
		email := randEmail(t, "simultaneous")
		const n = 8
		codes := make([]int, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				codes[i] = post(email, "ORGANISER").Code
			}(i)
		}
		wg.Wait()
		created := 0
		for _, c := range codes {
			if c == http.StatusCreated {
				created++
			} else {
				assert.Equal(t, http.StatusConflict, c)
			}
		}
		assert.Equal(t, 1, created)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM invitations WHERE email = $1`, email))
	})
}

func TestInvitation_Validation(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	admin := adminSession.AccessToken

	wantFields(t, e.do("POST", "/admin/invitations", `{}`, admin), "email", "role")
	wantFields(t, e.do("POST", "/admin/invitations", map[string]string{"email": "not-an-email", "role": "ORGANISER"}, admin), "email")
	for _, role := range []string{"FAN", "GATE_STAFF", "organiser", "ROOT", ""} {
		wantFields(t, e.do("POST", "/admin/invitations", map[string]string{"email": randEmail(t, "x"), "role": role}, admin), "role")
	}
	wantFields(t, e.do("POST", "/admin/invitations", `{"email":"a@b.co","role":"ADMIN","invited_by":1}`, admin), "invited_by")

	_, token := e.invite(admin, randEmail(t, "form"), "ORGANISER")
	rules := wantFields(t, e.accept(token, "Name", "too-short"), "password")
	assert.Equal(t, "min", rules["password"], "the password rule is ten characters")
	wantFields(t, e.accept(token, "", "a-long-pass-phrase"), "name")
	wantFields(t, e.accept(token, "   ", "a-long-pass-phrase"), "name")
	wantFields(t, e.accept(token, strings.Repeat("n", 101), "a-long-pass-phrase"), "name")
	wantFields(t, e.do("POST", "/auth/invitations/"+token+"/accept", `{}`, ""), "name", "password")
	wantFields(t, e.do("POST", "/auth/invitations/"+token+"/accept", `{"name":"A","password":"a-long-pass-phrase","role":"ADMIN"}`, ""), "role")
	wantFields(t, e.accept(token, "Name", strings.Repeat("p", 257)), "password")

	// None of that used the link up. Exactly ten characters is enough.
	require.Equal(t, http.StatusCreated, e.accept(token, "Name", "0123456789").Code)

	for _, bad := range []string{"0", "-1", "abc", "1.5"} {
		wantFields(t, e.do("POST", "/admin/invitations/"+bad+"/resend", nil, admin), "invitationId")
		wantFields(t, e.do("DELETE", "/admin/invitations/"+bad, nil, admin), "invitationId")
	}
}

func TestInvitation_ResendAndRevoke(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	admin := adminSession.AccessToken
	resend := func(id int64) *httptest.ResponseRecorder {
		return e.do("POST", "/admin/invitations/"+strconv.FormatInt(id, 10)+"/resend", nil, admin)
	}
	revoke := func(id int64) *httptest.ResponseRecorder {
		return e.do("DELETE", "/admin/invitations/"+strconv.FormatInt(id, 10), nil, admin)
	}

	t.Run("resend gives a new link and kills the old one", func(t *testing.T) {
		email := randEmail(t, "resend")
		inv, oldToken := e.invite(admin, email, "ORGANISER")

		rec := resend(inv.ID)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		again := decode[invitationJSON](t, rec)
		require.NotNil(t, again.InviteURL)
		newToken := strings.TrimPrefix(*again.InviteURL, invitePrefix)
		assert.NotEqual(t, oldToken, newToken)
		assert.Equal(t, inv.ID, again.ID, "the same invitation, not a second one")
		assert.Equal(t, "PENDING", again.Status)
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

		mails := e.mail.all()
		assert.Contains(t, mails[len(mails)-1].Text, *again.InviteURL)

		wantError(t, e.do("GET", "/auth/invitations/"+oldToken, nil, ""), http.StatusNotFound, domain.CodeInvitationInvalid)
		wantError(t, e.accept(oldToken, "Name", "a-long-pass-phrase"), http.StatusNotFound, domain.CodeInvitationInvalid)
		require.Equal(t, http.StatusCreated, e.accept(newToken, "Name", "a-long-pass-phrase").Code)

		// Accepted: neither resend nor revoke applies any more.
		wantError(t, resend(inv.ID), http.StatusConflict, domain.CodeInvitationNotPending)
		wantError(t, revoke(inv.ID), http.StatusConflict, domain.CodeInvitationNotPending)
	})

	t.Run("resend revives an expired invitation", func(t *testing.T) {
		inv, oldToken := e.invite(admin, randEmail(t, "revive"), "ORGANISER")
		exec(t, `UPDATE invitations SET expires_at = now() - interval '1 day' WHERE id = $1`, inv.ID)
		require.Equal(t, "EXPIRED", e.findInvitation(admin, inv.ID).Status)

		rec := resend(inv.ID)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		again := decode[invitationJSON](t, rec)
		assert.Equal(t, "PENDING", again.Status)
		expires, err := time.Parse(time.RFC3339, again.ExpiresAt)
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now().Add(72*time.Hour), expires, time.Minute)
		wantError(t, e.do("GET", "/auth/invitations/"+oldToken, nil, ""), http.StatusNotFound, domain.CodeInvitationInvalid)
		require.Equal(t, http.StatusOK, e.do("GET", "/auth/invitations/"+strings.TrimPrefix(*again.InviteURL, invitePrefix), nil, "").Code)
	})

	t.Run("revoke is safe to repeat and blocks resend", func(t *testing.T) {
		inv, token := e.invite(admin, randEmail(t, "revoke"), "ORGANISER")
		rec := revoke(inv.ID)
		require.Equal(t, http.StatusNoContent, rec.Code)
		assert.Empty(t, rec.Body.String())
		require.Equal(t, http.StatusNoContent, revoke(inv.ID).Code)
		wantError(t, resend(inv.ID), http.StatusConflict, domain.CodeInvitationNotPending)
		wantError(t, e.accept(token, "Name", "a-long-pass-phrase"), http.StatusNotFound, domain.CodeInvitationInvalid)
	})

	t.Run("an expired invitation can be revoked", func(t *testing.T) {
		inv, _ := e.invite(admin, randEmail(t, "expired.revoke"), "ORGANISER")
		exec(t, `UPDATE invitations SET expires_at = now() - interval '1 day' WHERE id = $1`, inv.ID)
		require.Equal(t, http.StatusNoContent, revoke(inv.ID).Code)
		assert.Equal(t, "REVOKED", e.findInvitation(admin, inv.ID).Status)
	})

	t.Run("unknown id", func(t *testing.T) {
		wantError(t, resend(9_000_000_000), http.StatusNotFound, domain.CodeNotFound)
		wantError(t, revoke(9_000_000_000), http.StatusNotFound, domain.CodeNotFound)
	})
}

// Two people (or one double click) accepting the same link at the same moment: exactly one
// account, exactly one session, and the others are told the link is dead.
func TestInvitation_ConcurrentAcceptsCreateOneUser(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)

	for round := 0; round < 3; round++ {
		email := randEmail(t, "race")
		inv, token := e.invite(adminSession.AccessToken, email, "ORGANISER")

		const n = 8
		recs := make([]*httptest.ResponseRecorder, n)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				recs[i] = e.accept(token, fmt.Sprintf("Racer %d", i), fmt.Sprintf("password-number-%d", i))
			}(i)
		}
		close(start)
		wg.Wait()

		winners := 0
		var winner sessionJSON
		for _, rec := range recs {
			switch rec.Code {
			case http.StatusCreated:
				winners++
				winner = decode[sessionJSON](t, rec)
			case http.StatusNotFound:
				wantError(t, rec, http.StatusNotFound, domain.CodeInvitationInvalid)
			default:
				t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
			}
		}
		require.Equal(t, 1, winners, "round %d: exactly one accept wins", round)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM users WHERE lower(email) = $1`, email))
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, winner.User.ID))
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM invitations WHERE id = $1 AND accepted_at IS NOT NULL`, inv.ID))
		// The account has the winner's name and password, not a mix.
		idx := strings.TrimPrefix(*winner.User.Name, "Racer ")
		e.login(email, "password-number-"+idx)
	}
}

// A mail failure must not fail the invitation: the admin still has the link.
func TestInvitation_MailFailureDoesNotFailTheInvitation(t *testing.T) {
	e := newEnv(t, withMailError(errors.New("smtp: connection refused")))
	_, adminSession := e.staffSession(domain.RoleAdmin)

	inv, token := e.invite(adminSession.AccessToken, randEmail(t, "nomail"), "ORGANISER")
	assert.Equal(t, "PENDING", inv.Status)
	assert.Contains(t, e.logs.String(), "invitation email was not sent")
	assert.Contains(t, e.logs.String(), "connection refused")
	assert.NotContains(t, e.logs.String(), token, "the failure is logged without the link")

	rec := e.do("POST", "/admin/invitations/"+strconv.FormatInt(inv.ID, 10)+"/resend", nil, adminSession.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	newToken := strings.TrimPrefix(*decode[invitationJSON](t, rec).InviteURL, invitePrefix)
	require.Equal(t, http.StatusCreated, e.accept(newToken, "Name", "a-long-pass-phrase").Code)
}

// Request logs name the route, never the URL, so the token in the path stays out of them.
func TestInvitation_TokenIsNotInRequestLogs(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	_, token := e.invite(adminSession.AccessToken, randEmail(t, "logs"), "ORGANISER")

	require.Equal(t, http.StatusOK, e.do("GET", "/auth/invitations/"+token, nil, "").Code)
	require.Equal(t, http.StatusCreated, e.accept(token, "Name", "a-long-pass-phrase").Code)

	logs := e.logs.String()
	assert.Contains(t, logs, "/v1/auth/invitations/{token}/accept")
	assert.NotContains(t, logs, token)
	assert.NotContains(t, logs, "a-long-pass-phrase")
	assert.NotContains(t, logs, staffPassword)
}

func TestInvitation_RateLimitedPerIP(t *testing.T) {
	e := newEnv(t)
	// Default: 30 per 15 minutes from one address, across preview and accept.
	for i := 0; i < 30; i++ {
		wantError(t, e.do("GET", "/auth/invitations/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil, ""), http.StatusNotFound, domain.CodeInvitationInvalid)
	}
	rec := e.do("GET", "/auth/invitations/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", nil, "")
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	rec = e.accept("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "Name", "a-long-pass-phrase")
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
}

func TestListInvitations_PagingAndStatusFilter(t *testing.T) {
	e := newEnv(t)
	admin, adminSession := e.staffSession(domain.RoleAdmin)
	token := adminSession.AccessToken

	// Five invitations from this admin: 2 pending, 1 expired, 1 revoked, 1 accepted.
	var ids []int64
	var tokens []string
	for i := 0; i < 5; i++ {
		inv, tok := e.invite(token, randEmail(t, fmt.Sprintf("list%d", i)), "ORGANISER")
		ids = append(ids, inv.ID)
		tokens = append(tokens, tok)
	}
	exec(t, `UPDATE invitations SET expires_at = now() - interval '1 hour' WHERE id = $1`, ids[2])
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/admin/invitations/"+strconv.FormatInt(ids[3], 10), nil, token).Code)
	require.Equal(t, http.StatusCreated, e.accept(tokens[4], "Accepted Person", "a-long-pass-phrase").Code)

	// Walk every page, two at a time; keep only this admin's rows (the database is shared).
	mine := map[int64]string{}
	var order []int64
	path := "/admin/invitations?limit=2"
	pages := 0
	for {
		rec := e.do("GET", path, nil, token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		p := decode[page[invitationJSON]](t, rec)
		require.LessOrEqual(t, len(p.Items), 2)
		pages++
		for _, inv := range p.Items {
			assert.Nil(t, inv.InviteURL, "invite_url is always null in the list")
			if inv.InvitedBy.ID == admin.ID {
				_, dup := mine[inv.ID]
				assert.False(t, dup, "invitation %d appeared on two pages", inv.ID)
				mine[inv.ID] = inv.Status
				order = append(order, inv.ID)
			}
		}
		if p.NextCursor == nil {
			break
		}
		path = "/admin/invitations?limit=2&cursor=" + *p.NextCursor
		require.Less(t, pages, 10_000)
	}
	assert.Equal(t, map[int64]string{
		ids[0]: "PENDING", ids[1]: "PENDING", ids[2]: "EXPIRED", ids[3]: "REVOKED", ids[4]: "ACCEPTED",
	}, mine)
	assert.Equal(t, []int64{ids[4], ids[3], ids[2], ids[1], ids[0]}, order, "newest first")
	assert.GreaterOrEqual(t, pages, 3)

	// The filter agrees with the status shown.
	for status, want := range map[string][]int64{
		"PENDING": {ids[1], ids[0]}, "EXPIRED": {ids[2]}, "REVOKED": {ids[3]}, "ACCEPTED": {ids[4]},
	} {
		rec := e.do("GET", "/admin/invitations?limit=100&status="+status, nil, token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var got []int64
		for _, inv := range decode[page[invitationJSON]](t, rec).Items {
			assert.Equal(t, status, inv.Status)
			if inv.InvitedBy.ID == admin.ID {
				got = append(got, inv.ID)
			}
		}
		assert.Equal(t, want, got, status)
	}

	for _, bad := range []string{"?status=pending", "?status=NOPE", "?limit=0", "?limit=101", "?limit=x", "?cursor=!!!", "?cursor=bm90anNvbg"} {
		rec := e.do("GET", "/admin/invitations"+bad, nil, token)
		wantError(t, rec, http.StatusBadRequest, domain.CodeValidationFailed)
	}
}

func TestListStaffUsers_PagingOrderAndRoleFilter(t *testing.T) {
	e := newEnv(t)
	_, adminSession := e.staffSession(domain.RoleAdmin)
	token := adminSession.AccessToken
	ctx := context.Background()

	// Names chosen to sort together and apart from other tests' users; one has no name.
	tag := randDigits(t, 10)
	mk := func(name *string, role string) int64 {
		u, err := e.q.CreateStaffUser(ctx, store.CreateStaffUserParams{Email: randEmail(t, "staff"), Name: name, Role: role, PasswordHash: staffPasswordHash})
		require.NoError(t, err)
		return u.ID
	}
	n := func(s string) *string { v := "zz" + tag + " " + s; return &v }
	charlie := mk(n("charlie"), "ORGANISER")
	alice := mk(n("Alice"), "ADMIN")
	bob := mk(n("bob"), "ORGANISER")
	bob2 := mk(n("bob"), "ORGANISER") // same name: id breaks the tie
	// A fan and a gate steward must never be listed.
	fan := e.phoneSession(randPhone(t)).User.ID

	walk := func(query string) (ids []int64, roles map[int64]string) {
		roles = map[int64]string{}
		path := "/admin/users?limit=3" + query
		for i := 0; ; i++ {
			rec := e.do("GET", path, nil, token)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			p := decode[page[userJSON]](t, rec)
			require.LessOrEqual(t, len(p.Items), 3)
			for _, u := range p.Items {
				_, dup := roles[u.ID]
				require.False(t, dup, "user %d appeared twice", u.ID)
				roles[u.ID] = u.Role
				ids = append(ids, u.ID)
				assert.NotNil(t, u.Email)
			}
			if p.NextCursor == nil {
				return ids, roles
			}
			path = "/admin/users?limit=3" + query + "&cursor=" + *p.NextCursor
			require.Less(t, i, 10_000)
		}
	}
	position := func(ids []int64, id int64) int {
		for i, v := range ids {
			if v == id {
				return i
			}
		}
		return -1
	}

	ids, roles := walk("")
	for _, id := range []int64{alice, bob, bob2, charlie} {
		require.NotEqual(t, -1, position(ids, id), "user %d missing from the list", id)
	}
	assert.Equal(t, -1, position(ids, fan), "fans are not staff")
	for id, role := range roles {
		assert.Contains(t, []string{"ORGANISER", "ADMIN"}, role, "user %d", id)
	}
	// By name without regard to case, then by id.
	assert.Less(t, position(ids, alice), position(ids, bob))
	assert.Less(t, position(ids, bob), position(ids, bob2))
	assert.Less(t, position(ids, bob2), position(ids, charlie))

	ids, roles = walk("&role=ADMIN")
	assert.NotEqual(t, -1, position(ids, alice))
	assert.Equal(t, -1, position(ids, bob))
	for _, role := range roles {
		assert.Equal(t, "ADMIN", role)
	}
	ids, _ = walk("&role=ORGANISER")
	assert.Equal(t, -1, position(ids, alice))
	assert.NotEqual(t, -1, position(ids, charlie))

	// The answer never carries secrets.
	rec := e.do("GET", "/admin/users?limit=100", nil, token)
	assert.NotContains(t, rec.Body.String(), "argon2")
	assert.NotContains(t, rec.Body.String(), "password")
	assert.NotContains(t, rec.Body.String(), "totp_secret")

	for _, bad := range []string{"?role=FAN", "?role=admin", "?limit=0", "?cursor=!!!"} {
		wantError(t, e.do("GET", "/admin/users"+bad, nil, token), http.StatusBadRequest, domain.CodeValidationFailed)
	}
}
