package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// invitationTokenBytes of randomness, sent as 43 base64url characters.
const invitationTokenBytes = 32

// The contract's pattern for the token in the link. Anything else cannot be one of ours and
// is refused without touching the database.
var invitationTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)

// nairobi is fixed at UTC+3: Kenya has no daylight saving, and a fixed zone needs no tzdata
// in the container image.
var nairobi = time.FixedZone("EAT", 3*60*60)

func toInvitation(i store.Invitation, inviter domain.Inviter) domain.Invitation {
	return domain.Invitation{
		ID:         i.ID,
		Email:      i.Email,
		Role:       domain.Role(i.Role),
		InvitedBy:  inviter,
		ExpiresAt:  i.ExpiresAt,
		AcceptedAt: i.AcceptedAt,
		RevokedAt:  i.RevokedAt,
		CreatedAt:  i.CreatedAt,
	}
}

func (s *Service) inviter(ctx context.Context, id int64) (domain.Inviter, error) {
	u, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return domain.Inviter{}, fmt.Errorf("find inviter %d: %w", id, err)
	}
	return domain.Inviter{ID: u.ID, Name: u.Name, Email: u.Email}, nil
}

func (s *Service) inviteURL(token string) string {
	return s.adminBaseURL + "/invite/" + token
}

func (s *Service) emailTaken(ctx context.Context, email string) (bool, error) {
	_, err := s.q.GetUserByEmail(ctx, email)
	if errors.Is(err, store.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find user by email: %w", err)
	}
	return true, nil
}

// CreateInvitation invites an email address to become staff. The answer carries the one-time
// link; only a hash of its token is stored.
func (s *Service) CreateInvitation(ctx context.Context, inviterID int64, rawEmail string, role domain.Role) (domain.Invitation, error) {
	email := domain.NormaliseEmail(rawEmail)
	if !role.IsStaff() {
		return domain.Invitation{}, domain.InvalidField("role", "oneof", "Choose ORGANISER or ADMIN.")
	}
	taken, err := s.emailTaken(ctx, email)
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("create invitation: %w", err)
	}
	if taken {
		return domain.Invitation{}, domain.ErrEmailAlreadyRegistered
	}

	token, err := randomToken(invitationTokenBytes)
	if err != nil {
		return domain.Invitation{}, err
	}
	now := s.now()

	var row store.Invitation
	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		open, err := q.GetOpenInvitationByEmail(ctx, email)
		switch {
		case errors.Is(err, store.ErrNoRows):
		case err != nil:
			return fmt.Errorf("find open invitation: %w", err)
		case open.ExpiresAt.After(now):
			return domain.ErrInvitationAlreadyPending
		default:
			// The old one lapsed. It is revoked so the "one live invitation per email"
			// index lets the new one in.
			if _, err := q.RevokeInvitation(ctx, open.ID); err != nil {
				return fmt.Errorf("revoke lapsed invitation: %w", err)
			}
		}

		row, err = q.CreateInvitation(ctx, store.CreateInvitationParams{
			Email:     email,
			Role:      string(role),
			TokenHash: hashToken(token),
			InvitedBy: inviterID,
			ExpiresAt: now.Add(domain.InvitationLifetime),
		})
		if store.IsUniqueViolation(err, store.ConstraintInvitationLiveEmail) {
			// Another admin invited the same address in the same moment.
			return domain.ErrInvitationAlreadyPending
		}
		if err != nil {
			return fmt.Errorf("store invitation: %w", err)
		}
		return nil
	})
	if err != nil {
		if domain.CodeOf(err) != domain.CodeInternal {
			return domain.Invitation{}, err
		}
		return domain.Invitation{}, fmt.Errorf("create invitation: %w", err)
	}

	inviter, err := s.inviter(ctx, inviterID)
	if err != nil {
		return domain.Invitation{}, err
	}
	inv := toInvitation(row, inviter)
	link := s.inviteURL(token)
	inv.InviteURL = &link

	s.log.InfoContext(ctx, "invitation created", "invitation_id", inv.ID, "role", inv.Role, "invited_by", inviterID)
	s.sendInvitationMail(ctx, inv, link)
	return inv, nil
}

// ResendInvitation gives a pending or expired invitation a new link and a new expiry. The
// old link stops working.
func (s *Service) ResendInvitation(ctx context.Context, id int64) (domain.Invitation, error) {
	current, err := s.q.GetInvitationByID(ctx, id)
	if errors.Is(err, store.ErrNoRows) {
		return domain.Invitation{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("resend invitation %d: %w", id, err)
	}
	now := s.now()
	if !domain.InvitationStatusAt(current.AcceptedAt, current.RevokedAt, current.ExpiresAt, now).CanResend() {
		return domain.Invitation{}, domain.ErrInvitationNotPending
	}
	taken, err := s.emailTaken(ctx, current.Email)
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("resend invitation %d: %w", id, err)
	}
	if taken {
		return domain.Invitation{}, domain.ErrEmailAlreadyRegistered
	}

	token, err := randomToken(invitationTokenBytes)
	if err != nil {
		return domain.Invitation{}, err
	}
	row, err := s.q.ReissueInvitation(ctx, store.ReissueInvitationParams{
		ID:        id,
		TokenHash: hashToken(token),
		ExpiresAt: now.Add(domain.InvitationLifetime),
	})
	if errors.Is(err, store.ErrNoRows) {
		// Accepted or revoked between the read and the write.
		return domain.Invitation{}, domain.ErrInvitationNotPending
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("resend invitation %d: %w", id, err)
	}

	inviter, err := s.inviter(ctx, row.InvitedBy)
	if err != nil {
		return domain.Invitation{}, err
	}
	inv := toInvitation(row, inviter)
	link := s.inviteURL(token)
	inv.InviteURL = &link

	s.log.InfoContext(ctx, "invitation resent", "invitation_id", inv.ID)
	s.sendInvitationMail(ctx, inv, link)
	return inv, nil
}

// RevokeInvitation kills the link. Revoking twice is fine; revoking an accepted invitation
// is refused, because the account already exists.
func (s *Service) RevokeInvitation(ctx context.Context, id int64) error {
	// Two passes at most: if the conditional update matches nothing, the row changed under
	// us and one re-read says how.
	for attempt := 0; attempt < 2; attempt++ {
		current, err := s.q.GetInvitationByID(ctx, id)
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("revoke invitation %d: %w", id, err)
		}
		if current.AcceptedAt != nil {
			return domain.ErrInvitationNotPending
		}
		if current.RevokedAt != nil {
			return nil
		}
		n, err := s.q.RevokeInvitation(ctx, id)
		if err != nil {
			return fmt.Errorf("revoke invitation %d: %w", id, err)
		}
		if n == 1 {
			s.log.InfoContext(ctx, "invitation revoked", "invitation_id", id)
			return nil
		}
	}
	return fmt.Errorf("revoke invitation %d: state kept changing", id)
}

// InvitationCursor is the position in the invitation list: the id of the last row seen.
type InvitationCursor struct {
	BeforeID int64 `json:"b"`
}

// ListInvitations returns one page, newest first, optionally of one status.
func (s *Service) ListInvitations(ctx context.Context, status *domain.InvitationStatus, limit int, after *InvitationCursor) ([]domain.Invitation, *InvitationCursor, error) {
	params := store.ListInvitationsPageParams{
		Now: s.now(),
		// One more than asked for, to know whether there is a next page.
		PageSize: int32(limit + 1),
	}
	if status != nil {
		st := string(*status)
		params.Status = &st
	}
	if after != nil {
		params.BeforeID = &after.BeforeID
	}
	rows, err := s.q.ListInvitationsPage(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list invitations: %w", err)
	}

	var next *InvitationCursor
	if len(rows) > limit {
		rows = rows[:limit]
		next = &InvitationCursor{BeforeID: rows[limit-1].Invitation.ID}
	}
	items := make([]domain.Invitation, 0, len(rows))
	for _, r := range rows {
		items = append(items, toInvitation(r.Invitation, domain.Inviter{
			ID: r.Invitation.InvitedBy, Name: r.InviterName, Email: r.InviterEmail,
		}))
	}
	return items, next, nil
}

// checkInvitation says whether an invitation can still be accepted, and if not, why.
func (s *Service) checkInvitation(row store.Invitation) error {
	switch domain.InvitationStatusAt(row.AcceptedAt, row.RevokedAt, row.ExpiresAt, s.now()) {
	case domain.InvitationPending:
		return nil
	case domain.InvitationExpired:
		return domain.ErrInvitationExpired
	default:
		// Accepted and revoked look the same as unknown: the link is simply dead.
		return domain.ErrInvitationInvalid
	}
}

// PreviewInvitation tells the set-password page who the link is for.
func (s *Service) PreviewInvitation(ctx context.Context, token string) (domain.Invitation, error) {
	if !invitationTokenPattern.MatchString(token) {
		return domain.Invitation{}, domain.ErrInvitationInvalid
	}
	row, err := s.q.GetInvitationByTokenHash(ctx, hashToken(token))
	if errors.Is(err, store.ErrNoRows) {
		return domain.Invitation{}, domain.ErrInvitationInvalid
	}
	if err != nil {
		return domain.Invitation{}, fmt.Errorf("preview invitation: %w", err)
	}
	if err := s.checkInvitation(row); err != nil {
		return domain.Invitation{}, err
	}
	return toInvitation(row, domain.Inviter{ID: row.InvitedBy}), nil
}

// AcceptInvitation creates the staff account, marks the invitation accepted and signs the
// new user in, all in one transaction. The link works once.
func (s *Service) AcceptInvitation(ctx context.Context, token, rawName, password string) (Session, error) {
	// The link is checked before the body, so a dead link says so whatever was typed, and
	// before any hashing, so a made-up token costs us one indexed read.
	if _, err := s.PreviewInvitation(ctx, token); err != nil {
		return Session{}, err
	}
	name, err := cleanName(rawName)
	if err != nil {
		return Session{}, err
	}
	if err := checkNewPassword("password", password); err != nil {
		return Session{}, err
	}
	// Hashed before the transaction: argon2 is slow on purpose and must not run while the
	// invitation row is locked.
	hash, err := s.hasher.Hash(ctx, password)
	if err != nil {
		return Session{}, err
	}

	var session Session
	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		// Row lock: a second accept of the same link waits here, then sees accepted_at set.
		inv, err := q.GetInvitationByTokenHashForUpdate(ctx, hashToken(token))
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrInvitationInvalid
		}
		if err != nil {
			return fmt.Errorf("lock invitation: %w", err)
		}
		if err := s.checkInvitation(inv); err != nil {
			return err
		}

		user, err := q.CreateStaffUser(ctx, store.CreateStaffUserParams{
			Email:        inv.Email,
			Name:         &name,
			Role:         inv.Role,
			PasswordHash: hash,
		})
		if store.IsUniqueViolation(err, store.ConstraintUsersEmail) {
			return domain.ErrEmailAlreadyRegistered
		}
		if err != nil {
			return fmt.Errorf("create staff user: %w", err)
		}

		n, err := q.MarkInvitationAccepted(ctx, inv.ID)
		if err != nil {
			return fmt.Errorf("mark invitation accepted: %w", err)
		}
		if n == 0 {
			// Cannot happen under the row lock unless it lapsed this instant; either way
			// the account must not be created.
			return domain.ErrInvitationInvalid
		}
		s.log.InfoContext(ctx, "invitation accepted", "invitation_id", inv.ID, "user_id", user.ID, "role", user.Role)

		session, err = s.startSession(ctx, q, user)
		return err
	})
	if err != nil {
		if domain.CodeOf(err) != domain.CodeInternal {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("accept invitation: %w", err)
	}
	return session, nil
}

// sendInvitationMail never fails the caller: the admin has the link on screen and can send
// it another way. The link is not logged here.
func (s *Service) sendInvitationMail(ctx context.Context, inv domain.Invitation, link string) {
	roleName := "an organiser"
	if inv.Role == domain.RoleAdmin {
		roleName = "an admin"
	}
	text := fmt.Sprintf(`Hello,

You have been invited to Talanta Tickets as %s.

Open this link to set your password and sign in:
%s

The link works once and expires on %s (Nairobi time). If it has expired, ask the person who invited you to send it again.

If you were not expecting this, you can ignore this email.
`, roleName, link, inv.ExpiresAt.In(nairobi).Format("Monday 2 January 2006 at 15:04"))

	err := s.mailer.SendMail(ctx, notify.Mail{
		To:      inv.Email,
		Subject: "You have been invited to Talanta Tickets",
		Text:    text,
	})
	if err != nil {
		s.log.ErrorContext(ctx, "invitation email was not sent; the admin still has the link",
			"invitation_id", inv.ID, "err", err)
	}
}

// StaffUser is one row of the staff list.
type StaffUser struct {
	ID          int64
	Name        *string
	Email       string
	Role        domain.Role
	TOTPEnabled bool
	CreatedAt   time.Time
}

// StaffCursor is the position in the staff list: the sort name and id of the last row seen.
type StaffCursor struct {
	Name string `json:"n"`
	ID   int64  `json:"i"`
}

// ListStaff returns one page of organisers and admins, by name.
func (s *Service) ListStaff(ctx context.Context, role *domain.Role, limit int, after *StaffCursor) ([]StaffUser, *StaffCursor, error) {
	params := store.ListStaffUsersParams{PageSize: int32(limit + 1)}
	if role != nil {
		r := string(*role)
		params.Role = &r
	}
	if after != nil {
		params.AfterID = &after.ID
		params.AfterName = &after.Name
	}
	rows, err := s.q.ListStaffUsers(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list staff: %w", err)
	}

	var next *StaffCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = &StaffCursor{Name: last.SortName, ID: last.ID}
	}
	items := make([]StaffUser, 0, len(rows))
	for _, r := range rows {
		email := ""
		if r.Email != nil {
			email = *r.Email
		}
		items = append(items, StaffUser{
			ID: r.ID, Name: r.Name, Email: email, Role: domain.Role(r.Role),
			TOTPEnabled: r.TotpEnabled, CreatedAt: r.CreatedAt,
		})
	}
	return items, next, nil
}
