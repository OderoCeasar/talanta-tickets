package booking

import (
	"reflect"
	"testing"
)

// row builds the free seats of one row: numbers lists the free seat numbers, and types
// overrides the type of some of them.
func row(rowID int64, rowIndex, rowSeats int, numbers []int, types map[int]string) []candidate {
	var out []candidate
	for _, n := range numbers {
		st := seatStandard
		if t, ok := types[n]; ok {
			st = t
		}
		out = append(out, candidate{ID: rowID*100 + int64(n), RowID: rowID, RowIndex: rowIndex, RowSeats: rowSeats, SeatNumber: n, SeatType: st})
	}
	return out
}

func runIDs(runs []run) [][]int64 {
	out := [][]int64{}
	for _, r := range runs {
		out = append(out, r.SeatIDs)
	}
	return out
}

func TestRunsInBlock(t *testing.T) {
	cases := []struct {
		name    string
		seats   []candidate
		n       int
		allowed func(string) bool
		want    [][]int64
	}{
		{
			name:  "a full row is cut from seat 1, the run nearest the middle first",
			seats: row(1, 1, 8, []int{1, 2, 3, 4, 5, 6, 7, 8}, nil), n: 3, allowed: standardOnly,
			want: [][]int64{{104, 105, 106}, {101, 102, 103}},
		},
		{
			name:  "never across a gap (seat 4 is held, sold, blocked or switched off)",
			seats: row(1, 1, 8, []int{1, 2, 3, 5, 6, 7, 8}, nil), n: 4, allowed: standardOnly,
			want: [][]int64{{105, 106, 107, 108}},
		},
		{
			name:  "two seats each side of a gap are not four together",
			seats: row(1, 1, 5, []int{1, 2, 4, 5}, nil), n: 4, allowed: standardOnly,
			want: [][]int64{},
		},
		{
			name: "never across two rows, even when the numbers follow on",
			seats: append(row(1, 1, 2, []int{1, 2}, nil),
				row(2, 2, 4, []int{3, 4}, nil)...), n: 4, allowed: standardOnly,
			want: [][]int64{},
		},
		{
			name: "the front row first, whatever order the middle would give",
			seats: append(row(1, 1, 6, []int{1, 2}, nil),
				row(2, 2, 6, []int{3, 4}, nil)...), n: 2, allowed: standardOnly,
			want: [][]int64{{101, 102}, {203, 204}},
		},
		{
			name:  "an accessible seat and its companion break a run and are never in one",
			seats: row(1, 1, 6, []int{1, 2, 3, 4, 5, 6}, map[int]string{3: "ACCESSIBLE", 4: "COMPANION"}), n: 2, allowed: withRestrictedView,
			want: [][]int64{{101, 102}, {105, 106}},
		},
		{
			name:  "restricted view breaks a run of standard seats",
			seats: row(1, 1, 6, []int{1, 2, 3, 4, 5, 6}, map[int]string{3: seatRestrictedView}), n: 3, allowed: standardOnly,
			want: [][]int64{{104, 105, 106}},
		},
		{
			name:  "restricted view is part of a run in the second pass",
			seats: row(1, 1, 6, []int{1, 2, 3, 4, 5, 6}, map[int]string{3: seatRestrictedView}), n: 6, allowed: withRestrictedView,
			want: [][]int64{{101, 102, 103, 104, 105, 106}},
		},
		{
			name:  "a single seat is a run of one",
			seats: row(1, 1, 3, []int{2}, nil), n: 1, allowed: standardOnly,
			want: [][]int64{{102}},
		},
		{name: "nothing free", seats: nil, n: 2, allowed: standardOnly, want: [][]int64{}},
		{name: "a quantity of zero finds nothing", seats: row(1, 1, 3, []int{1, 2}, nil), n: 0, allowed: standardOnly, want: [][]int64{}},
	}
	for _, c := range cases {
		if got := runIDs(runsInBlock(c.seats, c.n, c.allowed)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got  %v\n want %v", c.name, got, c.want)
		}
	}
}

// No run ever shares a seat with another, and every run is n consecutive numbers of one row.
func TestRunsInBlock_RunsNeverOverlap(t *testing.T) {
	var seats []candidate
	for r := 1; r <= 6; r++ {
		var numbers []int
		for n := 1; n <= 23; n++ {
			if (n*r)%7 != 0 { // a gap pattern that differs per row
				numbers = append(numbers, n)
			}
		}
		seats = append(seats, row(int64(r), r, 23, numbers, nil)...)
	}
	for n := 1; n <= 8; n++ {
		seen := map[int64]bool{}
		for _, r := range runsInBlock(seats, n, standardOnly) {
			if len(r.SeatIDs) != n {
				t.Fatalf("n=%d: run of %d", n, len(r.SeatIDs))
			}
			for i, id := range r.SeatIDs {
				if seen[id] {
					t.Fatalf("n=%d: seat %d is in two runs", n, id)
				}
				seen[id] = true
				if i > 0 && (id != r.SeatIDs[i-1]+1 || id/100 != r.SeatIDs[0]/100) {
					t.Fatalf("n=%d: %v is not consecutive seats of one row", n, r.SeatIDs)
				}
			}
		}
	}
}

func TestBestWindow(t *testing.T) {
	want := []int{1, 8, 32, 128, 512, 2048, 2048}
	for i, w := range want {
		if got := bestWindow(i + 1); got != w {
			t.Errorf("bestWindow(%d) = %d, want %d", i+1, got, w)
		}
	}
}
