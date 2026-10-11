// Package seatmap turns a block's placement and grid settings into rows and seats with
// coordinates in venue plan space. It has no database or HTTP code: the seeder and the admin
// "generate seats" endpoint both call it, so a seat lands in the same place whichever made it.
//
// Geometry. A block has a local frame: +x runs along a row, +y runs away from the pitch (row
// index grows with y). The plan uses screen axes (y grows downwards), and a block is placed by
// rotating its local frame clockwise by RotationDeg about the origin and moving it to
// (OriginX, OriginY). With rotation 0 the block faces up the screen, like a south stand.
//
// A straight block is a plain grid: seat i of row r sits at local (offset_r + i·PitchX,
// r·PitchY).
//
// A curved block (ArcRadius > 0) is that same grid bent around a centre that lies ArcRadius
// behind the middle of the front row, on the pitch side. Rows become concentric arcs and every
// row spans the same angle, so the block is a ring sector and neighbouring sectors tile a
// corner without gaps. PitchX is therefore the seat spacing on the front row; rows further
// back are proportionally wider. The middle of the front row does not move when a block is
// switched between straight and curved.
package seatmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Direction is which end of a row seat number 1 is at, looking along the block's local +x.
type Direction string

const (
	LeftToRight Direction = "LTR"
	RightToLeft Direction = "RTL"
)

// Limits on one block. They exist so a typo in the admin form cannot ask the server for
// millions of seats; no real block comes near them.
const (
	MaxRows        = 200
	MaxSeatsPerRow = 500
)

// maxArcStepDeg is how far apart outline points are placed on a curved edge. Five degrees
// keeps a quarter-circle corner visibly round with about twenty points per arc.
const maxArcStepDeg = 5.0

// BlockParams is everything needed to generate a block. It mirrors the columns of `blocks`.
type BlockParams struct {
	OriginX, OriginY float64
	RotationDeg      float64
	// ArcRadius is the radius of the front row. Zero means a straight block.
	ArcRadius float64
	// PitchX is the distance between seats in a row, PitchY the distance between rows.
	PitchX, PitchY float64
	Rows           int
	SeatsPerRow    int
	Numbering      Direction
	// RowOffsets shifts each row along local x so rows need not start level. Nil means none;
	// otherwise it must have one entry per row.
	RowOffsets []float64
}

// Point is a position in venue plan coordinates. In JSON it is the pair [x, y], which is how
// `stands.polygon` and `blocks.outline` store their points.
type Point struct {
	X, Y float64
}

func (p Point) MarshalJSON() ([]byte, error) {
	return json.Marshal([2]float64{p.X, p.Y})
}

func (p *Point) UnmarshalJSON(data []byte) error {
	var pair [2]float64
	if err := json.Unmarshal(data, &pair); err != nil {
		return fmt.Errorf("seatmap: a point must be [x, y]: %w", err)
	}
	p.X, p.Y = pair[0], pair[1]
	return nil
}

// Seat is one generated seat.
type Seat struct {
	Number int
	// Label is the row label followed by the seat number, for example "C14".
	Label string
	X, Y  float64
}

// Row is one generated row, front row first.
type Row struct {
	Index   int
	Label   string
	XOffset float64
	Seats   []Seat
}

// Layout is the result of generating one block.
type Layout struct {
	Rows []Row
	// Outline is the block's footprint, half a pitch larger than the seat centres on every
	// side, as a closed polygon without a repeated last point.
	Outline []Point
}

// SeatCount is the number of seats in the layout.
func (l Layout) SeatCount() int {
	n := 0
	for _, r := range l.Rows {
		n += len(r.Seats)
	}
	return n
}

// ErrInvalidParams is wrapped by every validation failure from Generate.
var ErrInvalidParams = errors.New("invalid block parameters")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidParams, fmt.Sprintf(format, args...))
}

// Validate reports whether p can be generated.
func (p BlockParams) Validate() error {
	switch {
	case p.Rows < 1 || p.Rows > MaxRows:
		return invalid("rows must be between 1 and %d, got %d", MaxRows, p.Rows)
	case p.SeatsPerRow < 1 || p.SeatsPerRow > MaxSeatsPerRow:
		return invalid("seats per row must be between 1 and %d, got %d", MaxSeatsPerRow, p.SeatsPerRow)
	case !(p.PitchX > 0) || !(p.PitchY > 0):
		return invalid("seat pitch must be positive, got %g × %g", p.PitchX, p.PitchY)
	case p.ArcRadius < 0:
		return invalid("arc radius must not be negative, got %g", p.ArcRadius)
	case p.Numbering != LeftToRight && p.Numbering != RightToLeft:
		return invalid("numbering must be %s or %s, got %q", LeftToRight, RightToLeft, p.Numbering)
	case p.RowOffsets != nil && len(p.RowOffsets) != p.Rows:
		return invalid("got %d row offsets for %d rows", len(p.RowOffsets), p.Rows)
	}
	for _, v := range []float64{p.OriginX, p.OriginY, p.RotationDeg, p.ArcRadius, p.PitchX, p.PitchY} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return invalid("values must be finite numbers")
		}
	}
	for i, off := range p.RowOffsets {
		if math.IsNaN(off) || math.IsInf(off, 0) {
			return invalid("row offset %d is not a finite number", i)
		}
	}
	if p.ArcRadius > 0 {
		lo, hi := p.extent()
		// Past a full turn the ends of a row would overlap its own start.
		if (hi-lo+p.PitchX)/p.ArcRadius >= 2*math.Pi {
			return invalid("a row %g long does not fit around a radius of %g", hi-lo+p.PitchX, p.ArcRadius)
		}
	}
	return nil
}

// Generate builds the rows, seats and outline of a block.
func Generate(p BlockParams) (Layout, error) {
	if err := p.Validate(); err != nil {
		return Layout{}, err
	}
	place := p.placer()

	rows := make([]Row, p.Rows)
	for r := range rows {
		label := RowLabel(r)
		row := Row{Index: r, Label: label, XOffset: p.offset(r), Seats: make([]Seat, p.SeatsPerRow)}
		for i := range row.Seats {
			number := i + 1
			if p.Numbering == RightToLeft {
				number = p.SeatsPerRow - i
			}
			pt := place(row.XOffset+float64(i)*p.PitchX, float64(r)*p.PitchY)
			row.Seats[i] = Seat{
				Number: number,
				Label:  label + strconv.Itoa(number),
				X:      round2(pt.X),
				Y:      round2(pt.Y),
			}
		}
		rows[r] = row
	}
	return Layout{Rows: rows, Outline: p.outline(place)}, nil
}

// RowLabel names rows the way a spreadsheet names columns: A…Z, then AA, AB, … AZ, BA, …
// The index is zero-based; a negative index has no label.
func RowLabel(index int) string {
	if index < 0 {
		return ""
	}
	var buf [8]byte
	i := len(buf)
	for n := index + 1; n > 0; n = (n - 1) / 26 {
		i--
		buf[i] = byte('A' + (n-1)%26)
	}
	return string(buf[i:])
}

func (p BlockParams) offset(row int) float64 {
	if p.RowOffsets == nil {
		return 0
	}
	return p.RowOffsets[row]
}

// width is the length of an unshifted row, first seat centre to last.
func (p BlockParams) width() float64 {
	return float64(p.SeatsPerRow-1) * p.PitchX
}

// extent is the smallest and largest local x of any seat centre, across all rows.
func (p BlockParams) extent() (lo, hi float64) {
	minOff, maxOff := 0.0, 0.0
	for i, off := range p.RowOffsets {
		if i == 0 || off < minOff {
			minOff = off
		}
		if i == 0 || off > maxOff {
			maxOff = off
		}
	}
	return minOff, maxOff + p.width()
}

// placer returns the function that maps a position on the unbent grid to plan coordinates.
func (p BlockParams) placer() func(lx, ly float64) Point {
	sin, cos := math.Sincos(p.RotationDeg * math.Pi / 180)
	toPlan := func(lx, ly float64) Point {
		return Point{
			X: p.OriginX + lx*cos - ly*sin,
			Y: p.OriginY + lx*sin + ly*cos,
		}
	}
	if p.ArcRadius == 0 {
		return toPlan
	}
	mid := p.width() / 2
	return func(lx, ly float64) Point {
		// Distance along the front row from its middle becomes an angle; distance back from
		// the front row becomes extra radius.
		theta := (lx - mid) / p.ArcRadius
		rho := p.ArcRadius + ly
		s, c := math.Sincos(theta)
		return toPlan(mid+rho*s, rho*c-p.ArcRadius)
	}
}

func (p BlockParams) outline(place func(lx, ly float64) Point) []Point {
	lo, hi := p.extent()
	lo -= p.PitchX / 2
	hi += p.PitchX / 2
	front := -p.PitchY / 2
	back := float64(p.Rows-1)*p.PitchY + p.PitchY/2

	// A straight edge needs only its two ends; a curved one is sampled.
	steps := 1
	if p.ArcRadius > 0 {
		sweepDeg := (hi - lo) / p.ArcRadius * 180 / math.Pi
		steps = max(1, int(math.Ceil(sweepDeg/maxArcStepDeg)))
	}

	pts := make([]Point, 0, 2*(steps+1))
	for i := 0; i <= steps; i++ {
		pts = append(pts, place(lo+(hi-lo)*float64(i)/float64(steps), front))
	}
	for i := steps; i >= 0; i-- {
		pts = append(pts, place(lo+(hi-lo)*float64(i)/float64(steps), back))
	}
	for i := range pts {
		pts[i] = Point{X: round2(pts[i].X), Y: round2(pts[i].Y)}
	}
	return pts
}

// round2 keeps two decimals. Plan units are far coarser than that, and short numbers keep the
// block payload small.
func round2(v float64) float64 {
	r := math.Round(v*100) / 100
	if r == 0 {
		return 0 // not -0, which would serialise as "-0"
	}
	return r
}
