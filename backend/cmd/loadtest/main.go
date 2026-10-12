// Command loadtest is the load test of docs/ARCHITECTURE.md §14: many fans, each signed in
// as a different user, all trying to hold the same few seats at the same instant through the
// real HTTP API.
//
// It passes when exactly one request per seat wins (201), every other request is a clean
// 409, nothing answers 5xx, and the database afterwards shows each seat held once.
//
//	make loadtest                      # 500 fans at 50 seats on the local stack
//
// It creates its fans directly in the database and signs their access tokens with the
// development secret, because signing 500 fans in through the OTP endpoint from one address
// would be stopped by the per-address sign-in limit (which is the limit doing its job). It
// therefore refuses to run against anything but a local development database.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

type options struct {
	api     string
	dbURL   string
	users   int
	seats   int
	eventID int64
	keep    bool
}

type result struct {
	status  int
	code    string
	elapsed time.Duration
	err     error
}

func run() error {
	var o options
	flag.StringVar(&o.api, "api", "http://localhost:8080", "API origin, without /v1")
	flag.StringVar(&o.dbURL, "db", os.Getenv("DATABASE_URL"), "database the API uses (default $DATABASE_URL)")
	flag.IntVar(&o.users, "users", 500, "fans, one request each")
	flag.IntVar(&o.seats, "seats", 50, "seats they all go for")
	flag.Int64Var(&o.eventID, "event", 0, "event ID (default: the on-sale event with most free seats)")
	flag.BoolVar(&o.keep, "keep", false, "leave the holds in place instead of releasing them")
	flag.Parse()

	if o.users < o.seats || o.seats < 1 {
		return errors.New("need at least one seat and at least as many users as seats")
	}
	if err := localOnly(o.dbURL); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	pool, err := pgxpool.New(ctx, o.dbURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	if o.eventID == 0 {
		// The effective sales window is checked by the API; this only picks a candidate.
		err := pool.QueryRow(ctx, `
			SELECT e.id FROM events e
			JOIN event_seats es ON es.event_id = e.id AND es.status = 'AVAILABLE'
			WHERE e.status = 'ON_SALE'
			GROUP BY e.id ORDER BY count(*) DESC LIMIT 1`).Scan(&o.eventID)
		if err != nil {
			return fmt.Errorf("find an on-sale event (run `make seed` first): %w", err)
		}
	}

	seatIDs, err := freeSeats(ctx, pool, o.eventID, o.seats)
	if err != nil {
		return err
	}
	tokens, err := fans(ctx, pool, o.users)
	if err != nil {
		return err
	}
	fmt.Printf("event %d · %d fans · %d seats (event_seat ids %d…%d) · %s\n",
		o.eventID, o.users, o.seats, seatIDs[0], seatIDs[len(seatIDs)-1], o.api)

	results := fire(ctx, o, tokens, seatIDs)
	passed := report(o, results)

	dbOK, err := checkDatabase(ctx, pool, o.eventID, seatIDs)
	if err != nil {
		return err
	}
	if !o.keep {
		if err := release(ctx, pool, seatIDs); err != nil {
			return err
		}
	}
	if !passed || !dbOK {
		return errors.New("FAILED")
	}
	fmt.Println("PASSED")
	return nil
}

// localOnly keeps this tool, which writes users and signs tokens with the public
// development secret, away from any real database.
func localOnly(dbURL string) error {
	if dbURL == "" {
		return errors.New("no database: pass -db or set DATABASE_URL")
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("database URL: %w", err)
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "::1":
		return nil
	}
	return fmt.Errorf("refusing to run against %q: the load test is for a local development database only", u.Hostname())
}

// freeSeats picks n AVAILABLE seats that sit together in one block, so the test hits one
// small region of the inventory the way a rush for the best seats does.
func freeSeats(ctx context.Context, pool *pgxpool.Pool, eventID int64, n int) ([]int64, error) {
	rows, err := pool.Query(ctx, `
		SELECT es.id FROM event_seats es
		WHERE es.event_id = $1 AND es.status = 'AVAILABLE' AND es.category_id IS NOT NULL
		  AND es.block_id = (
			SELECT block_id FROM event_seats
			WHERE event_id = $1 AND status = 'AVAILABLE' AND category_id IS NOT NULL
			GROUP BY block_id HAVING count(*) >= $2 ORDER BY block_id LIMIT 1)
		ORDER BY es.id LIMIT $2`, eventID, n)
	if err != nil {
		return nil, fmt.Errorf("pick seats: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) < n {
		return nil, fmt.Errorf("event %d has no block with %d free seats", eventID, n)
	}
	return ids, nil
}

// fans makes n fan users and one access token for each.
func fans(ctx context.Context, pool *pgxpool.Pool, n int) ([]string, error) {
	signer := auth.NewTokens([]byte(config.DevJWTSecret), time.Now)
	tokens := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// 0799 9xx xxx is not a range the seeder or the mock's magic numbers use.
		phone := fmt.Sprintf("+2547999%05d", i)
		var id int64
		err := pool.QueryRow(ctx, `
			INSERT INTO users (phone_e164, name, role) VALUES ($1, $2, 'FAN')
			ON CONFLICT (phone_e164) DO UPDATE SET name = EXCLUDED.name
			RETURNING id`, phone, fmt.Sprintf("Load test fan %d", i)).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("create fan %d: %w", i, err)
		}
		tok, err := signer.IssueAccess(id, domain.RoleFan, "")
		if err != nil {
			return nil, fmt.Errorf("sign token: %w", err)
		}
		tokens = append(tokens, tok)
	}
	return tokens, nil
}

// fire sends every request at once: all goroutines are ready and blocked on one channel
// before any request leaves, so the server sees a real burst, not a ramp.
func fire(ctx context.Context, o options, tokens []string, seatIDs []int64) []result {
	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: o.users, MaxConnsPerHost: 0},
	}
	results := make([]result, len(tokens))
	start := make(chan struct{})
	var ready, done sync.WaitGroup
	for i, tok := range tokens {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			// Fan i goes for seat i mod seats: every seat is wanted by the same number of fans.
			body, _ := json.Marshal(map[string]any{"event_id": o.eventID, "seat_ids": []int64{seatIDs[i%len(seatIDs)]}})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.api+"/v1/holds", bytes.NewReader(body))
			if err != nil {
				results[i] = result{err: err}
				ready.Done()
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tok)
			ready.Done()
			<-start

			t0 := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				results[i] = result{err: err, elapsed: time.Since(t0)}
				return
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			r := result{status: resp.StatusCode, elapsed: time.Since(t0)}
			var e struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(raw, &e) == nil {
				r.code = e.Error.Code
			}
			results[i] = r
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()
	return results
}

func report(o options, results []result) bool {
	var won, lost, serverErrors, transport int
	other := map[string]int{}
	latencies := make([]time.Duration, 0, len(results))
	for _, r := range results {
		switch {
		case r.err != nil:
			transport++
			other["transport error: "+r.err.Error()]++
			continue
		case r.status == http.StatusCreated:
			won++
		case r.status == http.StatusConflict && r.code == string(domain.CodeSeatsUnavailable):
			lost++
		case r.status >= 500:
			serverErrors++
			other[fmt.Sprintf("%d %s", r.status, r.code)]++
		default:
			other[fmt.Sprintf("%d %s", r.status, r.code)]++
		}
		latencies = append(latencies, r.elapsed)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	pct := func(p float64) time.Duration {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[min(len(latencies)-1, int(p*float64(len(latencies))))]
	}

	fmt.Printf("requests sent        %d\n", len(results))
	fmt.Printf("holds won (201)      %d   (want %d)\n", won, o.seats)
	fmt.Printf("refused (409)        %d   (want %d)\n", lost, o.users-o.seats)
	fmt.Printf("5xx answers          %d   (want 0)\n", serverErrors)
	fmt.Printf("transport errors     %d   (want 0)\n", transport)
	for k, v := range other {
		fmt.Printf("  other: %-40s %d\n", k, v)
	}
	fmt.Printf("latency p50 %s · p95 %s · p99 %s · max %s\n",
		pct(0.50).Round(time.Millisecond), pct(0.95).Round(time.Millisecond),
		pct(0.99).Round(time.Millisecond), pct(1).Round(time.Millisecond))
	if p95 := pct(0.95); p95 > 300*time.Millisecond {
		fmt.Printf("NOTE: p95 is over the 300 ms target of §14 (%s)\n", p95.Round(time.Millisecond))
	}
	return won == o.seats && lost == o.users-o.seats && serverErrors == 0 && transport == 0 && len(other) == 0
}

// checkDatabase is the part an HTTP count cannot show: what the inventory really says.
func checkDatabase(ctx context.Context, pool *pgxpool.Pool, eventID int64, seatIDs []int64) (bool, error) {
	var held, distinctHolds, activeHolds, twice int
	err := pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE es.status = 'HELD'),
		       count(DISTINCT es.hold_id) FILTER (WHERE es.status = 'HELD'),
		       count(DISTINCT h.id) FILTER (WHERE h.status = 'ACTIVE')
		FROM event_seats es LEFT JOIN holds h ON h.id = es.hold_id
		WHERE es.id = ANY($1)`, seatIDs).Scan(&held, &distinctHolds, &activeHolds)
	if err != nil {
		return false, fmt.Errorf("check seats: %w", err)
	}
	// A seat row has one hold_id, so "held twice" can only show up as two ACTIVE holds
	// that each believe they own the seat: a hold with no seat row pointing back at it.
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM holds h
		WHERE h.event_id = $1 AND h.status = 'ACTIVE' AND h.expires_at > now()
		  AND NOT EXISTS (SELECT 1 FROM event_seats es WHERE es.hold_id = h.id AND es.status = 'HELD')`,
		eventID).Scan(&twice)
	if err != nil {
		return false, fmt.Errorf("check holds: %w", err)
	}
	var sold, heldAll, available, blocked, total int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'SOLD'), count(*) FILTER (WHERE status = 'HELD'),
		       count(*) FILTER (WHERE status = 'AVAILABLE'), count(*) FILTER (WHERE status = 'BLOCKED'), count(*)
		FROM event_seats WHERE event_id = $1`, eventID).Scan(&sold, &heldAll, &available, &blocked, &total)
	if err != nil {
		return false, fmt.Errorf("count inventory: %w", err)
	}

	fmt.Printf("database: %d of %d seats HELD, by %d different holds (%d ACTIVE)\n", held, len(seatIDs), distinctHolds, activeHolds)
	fmt.Printf("database: ACTIVE holds that own no seat (a double hold would leave one)   %d   (want 0)\n", twice)
	fmt.Printf("database: sold %d + held %d + available %d + blocked %d = %d of %d seats\n",
		sold, heldAll, available, blocked, sold+heldAll+available+blocked, total)
	ok := held == len(seatIDs) && distinctHolds == len(seatIDs) && activeHolds == len(seatIDs) &&
		twice == 0 && sold+heldAll+available+blocked == total
	return ok, nil
}

// release frees what the test held, so it can be run again straight away. It goes through
// the same statuses the API's own release writes.
func release(ctx context.Context, pool *pgxpool.Pool, seatIDs []int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE holds SET status = 'RELEASED'
		WHERE status = 'ACTIVE' AND id IN (SELECT hold_id FROM event_seats WHERE id = ANY($1) AND status = 'HELD')`, seatIDs); err != nil {
		return fmt.Errorf("release holds: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE event_seats SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL
		WHERE id = ANY($1) AND status = 'HELD'`, seatIDs); err != nil {
		return fmt.Errorf("free seats: %w", err)
	}
	return tx.Commit(ctx)
}
