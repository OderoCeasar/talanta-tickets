package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// The web ticket page (§9.4): GET /v1/t/{webToken}, the link in the "you're going" SMS, for
// fans who do not have the app. Whoever has the link has the tickets, so:
//
//   - the token is 32 random bytes made by the database, unrelated to the booking reference;
//   - the page is never cached (private, no-store), never indexed (noindex, in a header and
//     in the page), and never sends its own address anywhere (Referrer-Policy: no-referrer);
//   - it loads nothing from anywhere: no script at all, one style block allowed by its hash,
//     images only as data: URLs. The Content-Security-Policy below says exactly that;
//   - every value on it goes through html/template, which escapes by context;
//   - a token of the wrong shape, an unknown one and a booking with no tickets get the same
//     404 page, byte for byte;
//   - requests are rate limited per IP;
//   - the request log holds the route pattern, never the path, so the token is not logged.

// webTicketRoutes: public, and no access token is looked at (a phone's browser has none; a
// stale header from somewhere else must not turn the page into a 401). Paths are relative
// to /v1.
func (s *Server) webTicketRoutes(r chi.Router) {
	r.With(s.limitWebTicket).Get("/t/{webToken}", s.getWebTicket)
}

// webTicketCSS is the page's only style. Brand colours: Emerald Ink #064E3B and Champagne
// #F8E7C9 (packages/shared/src/theme.ts); the greys and the two state colours are neutral
// tints beside them, not a second palette.
const webTicketCSS = `
*{box-sizing:border-box}
body{margin:0;background:#F8E7C9;color:#064E3B;font-family:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;line-height:1.4}
header{background:#064E3B;color:#F8E7C9;padding:20px 16px}
header p{margin:0;font-size:13px;letter-spacing:.08em;text-transform:uppercase;opacity:.85}
header h1{margin:4px 0 0;font-size:22px}
main{max-width:460px;margin:0 auto;padding:16px}
.event{margin:0 0 16px}
.event h2{margin:0 0 4px;font-size:20px}
.event p{margin:0}
.notice{margin:0 0 16px;padding:12px;border:2px solid #064E3B;border-radius:8px;font-weight:600}
.ticket{background:#fff;border-radius:12px;padding:16px;margin:0 0 16px;border:1px solid rgba(6,78,59,.2)}
.ticket h3{margin:0 0 8px;font-size:18px}
.ticket dl{margin:0 0 12px;display:grid;grid-template-columns:auto 1fr;gap:2px 12px}
.ticket dt{opacity:.7}
.ticket dd{margin:0;font-weight:600}
.qr{display:block;width:100%;max-width:300px;height:auto;margin:0 auto;image-rendering:pixelated}
.state{margin:0;padding:10px;border-radius:8px;text-align:center;font-weight:700;background:#064E3B;color:#F8E7C9}
.state.off{background:#e5e5e5;color:#444}
.hint{margin:8px 0 0;text-align:center;font-size:14px;opacity:.8}
footer{max-width:460px;margin:0 auto;padding:0 16px 24px;font-size:13px;opacity:.75}
`

// webTicketCSP allows exactly what the page has. The style hash is worked out from the
// constant above, so the two cannot disagree.
var webTicketCSP = func() string {
	sum := sha256.Sum256([]byte(webTicketCSS))
	return "default-src 'none'; img-src data:; style-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}()

var webTicketTemplate = template.Must(template.New("web-ticket").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow, noarchive">
<title>{{.Title}}</title>
<style>{{.CSS}}</style>
</head>
<body>
<header><p>Talanta Stadium Ticketing</p><h1>{{.Heading}}</h1></header>
<main>
{{- if .Message}}
<p class="notice">{{.Message}}</p>
{{- end}}
{{- with .Booking}}
<section class="event">
<h2>{{.EventTitle}}</h2>
{{- if .Teams}}<p>{{.Teams}}</p>{{end}}
<p>{{.Kickoff}}</p>
{{- if .GatesOpen}}<p>Gates open {{.GatesOpen}}</p>{{end}}
<p>{{.VenueName}}</p>
</section>
{{- if .Cancelled}}
<p class="notice">This event was cancelled. These tickets are no longer valid.</p>
{{- end}}
{{- range .Tickets}}
<article class="ticket">
<h3>{{.Seat}}</h3>
<dl>
<dt>Stand</dt><dd>{{.Stand}}</dd>
<dt>Block</dt><dd>{{.Block}}</dd>
<dt>Row</dt><dd>{{.Row}}</dd>
<dt>Seat</dt><dd>{{.Number}}</dd>
<dt>Gate</dt><dd>{{.GateGroup}}</dd>
<dt>Ticket</dt><dd>{{.Category}}</dd>
{{- if .Holder}}
<dt>Name</dt><dd>{{.Holder}}</dd>
{{- end}}
</dl>
{{- if .QR}}
<img class="qr" src="{{.QR}}" alt="QR code for seat {{.Seat}}" width="{{.QRPixels}}" height="{{.QRPixels}}">
<p class="hint">Show this code at a {{.GateGroup}} gate. Turn your screen brightness up.</p>
{{- else}}
<p class="state off">{{.State}}</p>
{{- end}}
</article>
{{- end}}
{{- end}}
</main>
<footer>{{.Footer}}</footer>
</body>
</html>
`))

type webTicketView struct {
	Title, Heading, Message, Footer string
	// CSS is the constant above; typed so the template writes it as it is.
	CSS     template.CSS
	Booking *webBookingView
}

type webBookingView struct {
	EventTitle, Teams, Kickoff, GatesOpen, VenueName string
	Cancelled                                        bool
	Tickets                                          []webSeatView
}

type webSeatView struct {
	Seat, Stand, Block, Row, GateGroup, Category, Holder, State string
	Number                                                      int32
	// QR is a data: URL of the PNG, built here from bytes we drew; empty for a ticket that
	// is not VALID, which shows State instead.
	QR       template.URL
	QRPixels int
}

// webQRPixels is the size the page's QR images are drawn at. Version 9 with its quiet zone
// is 61 modules; 6 pixels a module fits in 384 and stays sharp when a phone scales it.
const webQRPixels = 384

// nairobi is Africa/Nairobi as a fixed offset. Kenya has no daylight saving, and a fixed
// zone needs no time zone database in the container.
var nairobi = time.FixedZone("EAT", 3*60*60)

func nairobiTime(t time.Time) string { return t.In(nairobi).Format("Mon 2 Jan 2006, 3:04 PM") + " EAT" }

func newWebBookingView(b ticketing.WebBooking) (*webBookingView, error) {
	first := b.Tickets[0]
	ev := first.Row.Event
	view := &webBookingView{
		EventTitle: ev.Title, Kickoff: nairobiTime(ev.KickoffAt), VenueName: first.Row.VenueName,
		Cancelled: first.EventStatus == domain.EventCancelled,
	}
	if ev.HomeTeam != nil && ev.AwayTeam != nil {
		view.Teams = *ev.HomeTeam + " v " + *ev.AwayTeam
	}
	if ev.GatesOpenAt != nil {
		view.GatesOpen = nairobiTime(*ev.GatesOpenAt)
	}
	for _, t := range b.Tickets {
		row := t.Row
		seat := webSeatView{
			Seat: row.BlockCode + " " + row.SeatLabel, Stand: row.StandName, Block: row.BlockLabel, Row: row.RowLabel,
			Number: row.SeatNumber, GateGroup: row.Ticket.GateGroup, Category: row.CategoryName,
		}
		if row.Ticket.HolderName != nil {
			seat.Holder = *row.Ticket.HolderName
		}
		switch t.Status() {
		case domain.TicketValid:
			png, err := ticketing.QRPNG(t.QRToken(), webQRPixels)
			if err != nil {
				return nil, err
			}
			seat.QR = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
			seat.QRPixels = webQRPixels
		case domain.TicketUsed:
			seat.State = "USED"
			if row.Ticket.UsedAt != nil {
				seat.State = "USED - scanned at the gate " + nairobiTime(*row.Ticket.UsedAt)
			}
		case domain.TicketVoid:
			seat.State = "VOID - this ticket is no longer valid"
		default:
			seat.State = string(t.Status())
		}
		view.Tickets = append(view.Tickets, seat)
	}
	return view, nil
}

// writeWebTicketPage sends one of the page's answers with every header the page promises.
func writeWebTicketPage(w http.ResponseWriter, status int, view webTicketView) {
	view.CSS = template.CSS(webTicketCSS)
	var buf bytes.Buffer
	if err := webTicketTemplate.Execute(&buf, view); err != nil {
		// The template and its data are ours; this cannot fail for a reason a visitor
		// caused. Say as little as possible.
		buf.Reset()
		buf.WriteString("<!doctype html><title>Talanta tickets</title><p>Something went wrong on our side.</p>")
		status = http.StatusInternalServerError
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Security-Policy", webTicketCSP)
	h.Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// webTicketNotFound is the one answer for every link that shows nothing: malformed, unknown,
// or a booking that has no tickets.
func webTicketNotFound(w http.ResponseWriter) {
	writeWebTicketPage(w, http.StatusNotFound, webTicketView{
		Title: "Tickets not found - Talanta", Heading: "Tickets not found",
		Message: "This link does not show any tickets. Check that you opened the whole link from your SMS.",
		Footer:  "If you booked in the Talanta app, your tickets are also under My Tickets there.",
	})
}

func (s *Server) getWebTicket(w http.ResponseWriter, r *http.Request) {
	booking, err := s.tickets.ByWebToken(r.Context(), chi.URLParam(r, "webToken"))
	if errors.Is(err, domain.ErrNotFound) {
		webTicketNotFound(w)
		return
	}
	var view *webBookingView
	if err == nil {
		view, err = newWebBookingView(booking)
	}
	if err != nil {
		logInternal(r, err)
		writeWebTicketPage(w, http.StatusInternalServerError, webTicketView{
			Title: "Talanta tickets", Heading: "Something went wrong",
			Message: "We could not show your tickets just now. Please try again in a moment.",
		})
		return
	}
	writeWebTicketPage(w, http.StatusOK, webTicketView{
		Title: "Your tickets - Talanta", Heading: "Booking " + booking.Reference, Booking: view,
		Footer: "Anyone with this link can use these tickets. Do not share it. Each code is scanned once at the gate.",
	})
}

// limitWebTicket is the per-IP limit of the page. It is its own middleware because the
// page's answers are HTML: the shared one answers in the JSON error shape.
func (s *Server) limitWebTicket(next http.Handler) http.Handler {
	rule := s.limits.WebTicketPerIP
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rule.Limit > 0 {
			if d := s.limiter.Take("ip:web-ticket:"+rateLimitKey(ClientIP(r.Context())), rule); !d.Allowed {
				seconds := max(ratelimit.RetryAfterSeconds(d.RetryAfter), 1)
				w.Header().Set("Retry-After", strconv.Itoa(seconds))
				writeWebTicketPage(w, http.StatusTooManyRequests, webTicketView{
					Title: "Too many requests - Talanta", Heading: "Too many requests",
					Message: "Too many requests from your network. Wait a moment and open the link again.",
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
