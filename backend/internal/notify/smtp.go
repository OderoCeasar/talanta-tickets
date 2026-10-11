package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig is read from SMTP_* env vars. The SMTP mailer is used only when Host is set.
type SMTPConfig struct {
	Host string
	Port int
	// Username and Password are optional; without them no AUTH is attempted.
	Username string
	Password string
	// From is the envelope and header sender, for example "Talanta Tickets <no-reply@…>".
	From string
	// AllowPlaintext lets mail go to a server that does not offer STARTTLS
	// (SMTP_ALLOW_PLAINTEXT). Off by default: invitation mail carries a one-time link.
	AllowPlaintext bool
	// Timeout covers the whole exchange. net/smtp has no context support, so the deadline is
	// set on the connection.
	Timeout time.Duration
}

// ErrSMTPNoTLS means the server did not offer STARTTLS and plaintext is not allowed, so
// nothing was sent.
var ErrSMTPNoTLS = errors.New("notify: smtp server does not offer STARTTLS; refusing to send in plaintext (set SMTP_ALLOW_PLAINTEXT=true to allow it)")

// SMTPMailer sends through one SMTP server. Port 465 is implicit TLS; any other port must
// upgrade with STARTTLS. A server that does not offer it is refused rather than sent to in
// the clear, because "offer" is exactly what someone in the path can strip. The exceptions
// are a loopback host (the bytes never leave the machine) and AllowPlaintext.
type SMTPMailer struct {
	cfg  SMTPConfig
	from *mail.Address
	// dial opens the plain TCP connection; replaceable in tests so a named host can be
	// served by a local fake.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

// isLoopbackHost reports whether host is this machine by name or by address.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// NewSMTPMailer checks the settings that would otherwise only fail on the first send.
func NewSMTPMailer(cfg SMTPConfig) (*SMTPMailer, error) {
	if cfg.Host == "" {
		return nil, errors.New("notify: SMTP host is empty")
	}
	from, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return nil, fmt.Errorf("notify: SMTP from address %q: %w", cfg.From, err)
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &SMTPMailer{cfg: cfg, from: from, dial: (&net.Dialer{}).DialContext}, nil
}

func (m *SMTPMailer) SendMail(ctx context.Context, msg Mail) error {
	to, err := mail.ParseAddress(msg.To)
	if err != nil {
		return fmt.Errorf("notify: recipient %q: %w", msg.To, err)
	}
	// A header value with a line break would let the subject smuggle in extra headers.
	if strings.ContainsAny(msg.Subject, "\r\n") {
		return errors.New("notify: subject contains a line break")
	}

	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	deadline, _ := ctx.Deadline()

	addr := net.JoinHostPort(m.cfg.Host, fmt.Sprint(m.cfg.Port))
	tlsConfig := &tls.Config{ServerName: m.cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if m.cfg.Port == 465 {
		conn, err = (&tls.Dialer{Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = m.dial(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("notify: dial smtp: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("notify: set smtp deadline: %w", err)
	}

	c, err := smtp.NewClient(conn, m.cfg.Host)
	if err != nil {
		return fmt.Errorf("notify: smtp greeting: %w", err)
	}
	defer c.Close()

	if m.cfg.Port != 465 {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("notify: starttls: %w", err)
			}
		} else if !m.cfg.AllowPlaintext && !isLoopbackHost(m.cfg.Host) {
			return ErrSMTPNoTLS
		}
	}
	if m.cfg.Username != "" {
		// PlainAuth itself refuses to send the password over an unencrypted connection to
		// anything but localhost.
		if err := c.Auth(smtp.PlainAuth("", m.cfg.Username, m.cfg.Password, m.cfg.Host)); err != nil {
			return fmt.Errorf("notify: smtp auth: %w", err)
		}
	}
	if err := c.Mail(m.from.Address); err != nil {
		return fmt.Errorf("notify: smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to.Address); err != nil {
		return fmt.Errorf("notify: smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("notify: smtp DATA: %w", err)
	}
	if _, err := w.Write(m.message(to, msg)); err != nil {
		return fmt.Errorf("notify: write message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("notify: finish message: %w", err)
	}
	return c.Quit()
}

func (m *SMTPMailer) message(to *mail.Address, msg Mail) []byte {
	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", m.from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", msg.Subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	// SMTP wants CRLF line endings.
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return []byte(b.String())
}
