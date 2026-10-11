// Package notify sends messages to people: SMS for sign-in codes and tickets, email for
// staff invitations. Each channel is an interface with a mock that writes to the log, so the
// system runs with no provider configured.
package notify

import (
	"context"
	"log/slog"
)

// SMSSender delivers a text message to a phone in E.164 form.
type SMSSender interface {
	SendSMS(ctx context.Context, to, body string) error
}

// Mail is one plain-text email.
type Mail struct {
	To      string
	Subject string
	Text    string
}

// Mailer delivers an email.
type Mailer interface {
	SendMail(ctx context.Context, m Mail) error
}

// LogSMS is the mock SMS provider: it writes the message to the log instead of sending it.
//
// Message bodies carry secrets (sign-in codes). Outside production they are logged at info so
// a developer can read the code. In production the body is never logged, at any level: a
// LOG_LEVEL=debug set while chasing a fault must not turn the log into a list of live codes.
type LogSMS struct {
	Logger     *slog.Logger
	Production bool
}

func (s LogSMS) SendSMS(ctx context.Context, to, body string) error {
	if s.Production {
		s.Logger.InfoContext(ctx, "sms not sent: no SMS provider is configured", "to", to)
		return nil
	}
	s.Logger.InfoContext(ctx, "sms (mock)", "to", to, "body", body)
	return nil
}

// LogMailer is the mock mail provider. The same rule as LogSMS applies: an invitation email
// carries the one-time link, which must not be in production logs at any level.
type LogMailer struct {
	Logger     *slog.Logger
	Production bool
}

func (m LogMailer) SendMail(ctx context.Context, mail Mail) error {
	if m.Production {
		m.Logger.InfoContext(ctx, "mail not sent: no SMTP server is configured", "to", mail.To, "subject", mail.Subject)
		return nil
	}
	m.Logger.InfoContext(ctx, "mail (mock)", "to", mail.To, "subject", mail.Subject, "text", mail.Text)
	return nil
}
