package notify

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretLink = "https://talanta.example.com/invite/SECRET-TOKEN-VALUE"

func capture(level slog.Level) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level})), &buf
}

// In production the mocks must not put codes or invitation links in the log at any level.
func TestLogMocksKeepSecretsOutOfProductionLogs(t *testing.T) {
	ctx := context.Background()

	logger, buf := capture(slog.LevelInfo)
	require.NoError(t, LogMailer{Logger: logger, Production: true}.SendMail(ctx, Mail{To: "a@b.co", Subject: "Invite", Text: "Open " + secretLink}))
	require.NoError(t, LogSMS{Logger: logger, Production: true}.SendSMS(ctx, "+254712345678", "Your code is 482913"))
	assert.NotContains(t, buf.String(), "SECRET-TOKEN-VALUE")
	assert.NotContains(t, buf.String(), "482913")
	assert.Contains(t, buf.String(), "a@b.co", "that a mail was not sent is still visible")
	assert.Contains(t, buf.String(), "+254712345678")

	// Not at debug either: LOG_LEVEL=debug on a production server must not expose them. Every
	// level is captured here, and nothing of the body or the link may appear.
	logger, buf = capture(slog.LevelDebug - 4)
	require.NoError(t, LogMailer{Logger: logger, Production: true}.SendMail(ctx, Mail{To: "a@b.co", Subject: "Invite", Text: "Open " + secretLink}))
	require.NoError(t, LogSMS{Logger: logger, Production: true}.SendSMS(ctx, "+254712345678", "Your code is 482913"))
	assert.NotContains(t, buf.String(), "SECRET-TOKEN-VALUE")
	assert.NotContains(t, buf.String(), "/invite/")
	assert.NotContains(t, buf.String(), "482913")
	assert.NotContains(t, buf.String(), "Your code is")
	assert.NotContains(t, buf.String(), "Open ")
	assert.Contains(t, buf.String(), "a@b.co")
	assert.Contains(t, buf.String(), "+254712345678")

	// Outside production the developer needs to read them.
	logger, buf = capture(slog.LevelInfo)
	require.NoError(t, LogMailer{Logger: logger}.SendMail(ctx, Mail{To: "a@b.co", Subject: "Invite", Text: "Open " + secretLink}))
	require.NoError(t, LogSMS{Logger: logger}.SendSMS(ctx, "+254712345678", "Your code is 482913"))
	assert.Contains(t, buf.String(), "SECRET-TOKEN-VALUE")
	assert.Contains(t, buf.String(), "482913")
}

// fakeSMTP speaks just enough SMTP (no TLS, no AUTH) to receive one message.
func fakeSMTP(t *testing.T) (host string, port int, received <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })
	out := make(chan string, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		say := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

		var transcript strings.Builder
		say("220 fake ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			transcript.WriteString(line)
			if inData {
				if line == ".\r\n" {
					inData = false
					say("250 queued")
				}
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"):
				say("250 fake")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				say("354 go ahead")
			case strings.HasPrefix(cmd, "QUIT"):
				say("221 bye")
				out <- transcript.String()
				return
			default:
				say("250 ok")
			}
		}
	}()

	h, p, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return h, n, out
}

func TestSMTPMailerSendsAMessage(t *testing.T) {
	host, port, received := fakeSMTP(t)
	m, err := NewSMTPMailer(SMTPConfig{Host: host, Port: port, From: "Talanta Tickets <no-reply@talanta.test>"})
	require.NoError(t, err)

	err = m.SendMail(context.Background(), Mail{
		To:      "new.organiser@talanta.test",
		Subject: "You have been invited — karibu",
		Text:    "Hello,\n\nOpen " + secretLink + "\n",
	})
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Contains(t, got, "MAIL FROM:<no-reply@talanta.test>")
		assert.Contains(t, got, "RCPT TO:<new.organiser@talanta.test>")
		assert.Contains(t, got, `From: "Talanta Tickets" <no-reply@talanta.test>`)
		assert.Contains(t, got, "To: <new.organiser@talanta.test>")
		assert.Contains(t, got, "Subject: =?utf-8?q?", "a non-ASCII subject is encoded")
		assert.Contains(t, got, "Content-Type: text/plain; charset=utf-8")
		assert.Contains(t, got, "Open "+secretLink+"\r\n")
	case <-time.After(5 * time.Second):
		t.Fatal("the fake SMTP server received nothing")
	}
}

// A server that does not offer STARTTLS is not sent to: the invitation link would cross the
// network in the clear, and "not offered" is what an attacker in the path makes it look like.
// The fake server offers no STARTTLS; it is reached under a public-looking name so the
// loopback exception does not apply.
func TestSMTPMailerRefusesAServerWithoutSTARTTLS(t *testing.T) {
	msg := Mail{To: "new.organiser@talanta.test", Subject: "Invite", Text: "Open " + secretLink}
	viaFake := func(t *testing.T, cfg SMTPConfig) (*SMTPMailer, <-chan string) {
		t.Helper()
		host, port, received := fakeSMTP(t)
		cfg.Port = 587
		cfg.From = "no-reply@talanta.test"
		cfg.Timeout = 3 * time.Second
		m, err := NewSMTPMailer(cfg)
		require.NoError(t, err)
		m.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(host, strconv.Itoa(port)))
		}
		return m, received
	}

	t.Run("refused by default, and nothing of the message is sent", func(t *testing.T) {
		m, received := viaFake(t, SMTPConfig{Host: "smtp.example.com"})
		err := m.SendMail(context.Background(), msg)
		require.ErrorIs(t, err, ErrSMTPNoTLS)
		select {
		case got := <-received:
			t.Fatalf("the server received a full exchange: %q", got)
		case <-time.After(200 * time.Millisecond):
		}
	})

	t.Run("SMTP_ALLOW_PLAINTEXT=true sends anyway", func(t *testing.T) {
		m, received := viaFake(t, SMTPConfig{Host: "smtp.example.com", AllowPlaintext: true})
		require.NoError(t, m.SendMail(context.Background(), msg))
		select {
		case got := <-received:
			assert.Contains(t, got, "Open "+secretLink)
		case <-time.After(5 * time.Second):
			t.Fatal("the fake SMTP server received nothing")
		}
	})

	t.Run("a loopback host is allowed without TLS", func(t *testing.T) {
		for _, host := range []string{"localhost", "127.0.0.1", "::1", "LOCALHOST"} {
			m, received := viaFake(t, SMTPConfig{Host: host})
			require.NoError(t, m.SendMail(context.Background(), msg), host)
			<-received
		}
		for _, host := range []string{"smtp.example.com", "localhost.example.com", "10.0.0.5", "mail"} {
			assert.False(t, isLoopbackHost(host), host)
		}
	})
}

func TestSMTPMailerFailsFastAndCleanly(t *testing.T) {
	_, err := NewSMTPMailer(SMTPConfig{Host: "", From: "a@b.co"})
	require.Error(t, err)
	_, err = NewSMTPMailer(SMTPConfig{Host: "smtp.example.com", From: "not an address"})
	require.Error(t, err)

	// Nothing listening: an error, not a hang.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	ln.Close()

	m, err := NewSMTPMailer(SMTPConfig{Host: "127.0.0.1", Port: port, From: "a@b.co", Timeout: 2 * time.Second})
	require.NoError(t, err)
	start := time.Now()
	err = m.SendMail(context.Background(), Mail{To: "x@y.co", Subject: "s", Text: "t"})
	require.Error(t, err)
	assert.Less(t, time.Since(start), 4*time.Second)

	// A subject that tries to add a header is refused before anything is dialled.
	err = m.SendMail(context.Background(), Mail{To: "x@y.co", Subject: "hi\r\nBcc: evil@example.com", Text: "t"})
	require.ErrorContains(t, err, "line break")
	err = m.SendMail(context.Background(), Mail{To: "not an address", Subject: "s", Text: "t"})
	require.ErrorContains(t, err, "recipient")
}
