package seatmap

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func base() BlockParams {
	return BlockParams{PitchX: 5, PitchY: 8, Rows: 3, SeatsPerRow: 4, Numbering: LeftToRight}
}

func TestGenerateStraight(t *testing.T) {
	p := base()
	p.OriginX, p.OriginY = 100, 200

	l, err := Generate(p)
	require.NoError(t, err)
	require.Len(t, l.Rows, 3)
	assert.Equal(t, 12, l.SeatCount())

	assert.Equal(t, "A", l.Rows[0].Label)
	assert.Equal(t, Seat{Number: 1, Label: "A1", X: 100, Y: 200}, l.Rows[0].Seats[0])
	assert.Equal(t, Seat{Number: 4, Label: "A4", X: 115, Y: 200}, l.Rows[0].Seats[3])
	assert.Equal(t, Seat{Number: 2, Label: "C2", X: 105, Y: 216}, l.Rows[2].Seats[1])

	// Half a pitch of margin on every side of the seat centres.
	assert.Equal(t, []Point{{97.5, 196}, {117.5, 196}, {117.5, 220}, {97.5, 220}}, l.Outline)
}

func TestGenerateRightToLeftNumbering(t *testing.T) {
	p := base()
	p.Numbering = RightToLeft

	l, err := Generate(p)
	require.NoError(t, err)
	// Positions do not move; only the numbers run the other way.
	assert.Equal(t, Seat{Number: 4, Label: "A4", X: 0, Y: 0}, l.Rows[0].Seats[0])
	assert.Equal(t, Seat{Number: 1, Label: "A1", X: 15, Y: 0}, l.Rows[0].Seats[3])
}

func TestGenerateRotated(t *testing.T) {
	p := base()
	p.OriginX, p.OriginY = 100, 200
	p.RotationDeg = 90 // a west stand: rows run down the plan, depth runs left

	l, err := Generate(p)
	require.NoError(t, err)
	assert.Equal(t, Seat{Number: 1, Label: "A1", X: 100, Y: 200}, l.Rows[0].Seats[0])
	assert.Equal(t, Seat{Number: 4, Label: "A4", X: 100, Y: 215}, l.Rows[0].Seats[3])
	assert.Equal(t, Seat{Number: 1, Label: "C1", X: 84, Y: 200}, l.Rows[2].Seats[0])

	p.RotationDeg = 180 // a north stand: everything mirrored through the origin
	l, err = Generate(p)
	require.NoError(t, err)
	assert.Equal(t, Seat{Number: 4, Label: "C4", X: 85, Y: 184}, l.Rows[2].Seats[3])
}

func TestGenerateRowOffsets(t *testing.T) {
	p := base()
	p.RowOffsets = []float64{0, 2.5, -5}

	l, err := Generate(p)
	require.NoError(t, err)
	assert.Equal(t, 2.5, l.Rows[1].XOffset)
	assert.Equal(t, 2.5, l.Rows[1].Seats[0].X)
	assert.Equal(t, -5.0, l.Rows[2].Seats[0].X)
	// The outline covers the furthest-left and furthest-right rows.
	assert.Equal(t, []Point{{-7.5, -4}, {20, -4}, {20, 20}, {-7.5, 20}}, l.Outline)
}

func TestGenerateCurved(t *testing.T) {
	const radius = 100.0
	p := BlockParams{
		OriginX: 0, OriginY: 0,
		ArcRadius: radius, PitchX: 5, PitchY: 8,
		Rows: 4, SeatsPerRow: 21, Numbering: LeftToRight,
	}
	l, err := Generate(p)
	require.NoError(t, err)

	// The centre of curvature is ArcRadius behind the middle of the front row.
	centre := Point{X: 50, Y: -radius}
	for _, row := range l.Rows {
		want := radius + float64(row.Index)*p.PitchY
		for _, s := range row.Seats {
			got := math.Hypot(s.X-centre.X, s.Y-centre.Y)
			assert.InDelta(t, want, got, 0.02, "row %s seat %d is off its arc", row.Label, s.Number)
		}
	}

	// The middle seat of the front row is where it would be in a straight block.
	assert.Equal(t, Seat{Number: 11, Label: "A11", X: 50, Y: 0}, l.Rows[0].Seats[10])

	// Front-row neighbours are one pitch apart along the arc (chord is a hair shorter).
	a, b := l.Rows[0].Seats[0], l.Rows[0].Seats[1]
	assert.InDelta(t, 5, math.Hypot(b.X-a.X, b.Y-a.Y), 0.02)

	// Every row spans the same angle, so the back row is wider by the ratio of the radii.
	first, last := l.Rows[3].Seats[0], l.Rows[3].Seats[1]
	assert.InDelta(t, 5*(radius+24)/radius, math.Hypot(last.X-first.X, last.Y-first.Y), 0.02)

	// Seats in one column lie on one ray from the centre.
	front, back := l.Rows[0].Seats[0], l.Rows[3].Seats[0]
	assert.InDelta(t,
		math.Atan2(front.Y-centre.Y, front.X-centre.X),
		math.Atan2(back.Y-centre.Y, back.X-centre.X), 1e-3)

	// The outline is a ring sector: inner edge then outer edge, every point on one of the
	// two radii, and enough points that the curve is not drawn as a straight line.
	require.Greater(t, len(l.Outline), 4)
	require.Zero(t, len(l.Outline)%2)
	half := len(l.Outline) / 2
	for i, pt := range l.Outline {
		want := radius - p.PitchY/2
		if i >= half {
			want = radius + 3*p.PitchY + p.PitchY/2
		}
		assert.InDelta(t, want, math.Hypot(pt.X-centre.X, pt.Y-centre.Y), 0.02, "outline point %d", i)
	}
}

func TestGenerateCurvedRotated(t *testing.T) {
	p := BlockParams{
		OriginX: 300, OriginY: 400, RotationDeg: 45,
		ArcRadius: 150, PitchX: 5, PitchY: 8,
		Rows: 2, SeatsPerRow: 11, Numbering: LeftToRight,
	}
	l, err := Generate(p)
	require.NoError(t, err)

	// Rotating the block rotates the centre of curvature with it: local (25, -150) turned 45°.
	sin, cos := math.Sincos(math.Pi / 4)
	centre := Point{X: 300 + 25*cos + 150*sin, Y: 400 + 25*sin - 150*cos}
	for _, s := range l.Rows[1].Seats {
		assert.InDelta(t, 158, math.Hypot(s.X-centre.X, s.Y-centre.Y), 0.02)
	}
}

func TestRowLabel(t *testing.T) {
	cases := map[int]string{
		0: "A", 1: "B", 25: "Z",
		26: "AA", 27: "AB", 51: "AZ", 52: "BA",
		701: "ZZ", 702: "AAA",
		-1: "",
	}
	for index, want := range cases {
		assert.Equal(t, want, RowLabel(index), "index %d", index)
	}
}

func TestGenerateLabelsPastZ(t *testing.T) {
	p := base()
	p.Rows = 28

	l, err := Generate(p)
	require.NoError(t, err)
	assert.Equal(t, "Z", l.Rows[25].Label)
	assert.Equal(t, "AA", l.Rows[26].Label)
	assert.Equal(t, "AB3", l.Rows[27].Seats[2].Label)

	seen := map[string]bool{}
	for _, r := range l.Rows {
		require.False(t, seen[r.Label], "row label %s used twice", r.Label)
		seen[r.Label] = true
	}
}

func TestGenerateRejectsBadParams(t *testing.T) {
	cases := map[string]func(*BlockParams){
		"no rows":        func(p *BlockParams) { p.Rows = 0 },
		"too many rows":  func(p *BlockParams) { p.Rows = MaxRows + 1 },
		"no seats":       func(p *BlockParams) { p.SeatsPerRow = 0 },
		"too many seats": func(p *BlockParams) { p.SeatsPerRow = MaxSeatsPerRow + 1 },
		"too many seats in all": func(p *BlockParams) {
			p.Rows, p.SeatsPerRow = MaxRows, MaxSeatsPerRow
		},
		"zero pitch":          func(p *BlockParams) { p.PitchX = 0 },
		"negative pitch":      func(p *BlockParams) { p.PitchY = -1 },
		"NaN pitch":           func(p *BlockParams) { p.PitchX = math.NaN() },
		"negative radius":     func(p *BlockParams) { p.ArcRadius = -10 },
		"unknown numbering":   func(p *BlockParams) { p.Numbering = "UP" },
		"offsets wrong count": func(p *BlockParams) { p.RowOffsets = []float64{1} },
		"infinite origin":     func(p *BlockParams) { p.OriginX = math.Inf(1) },
		"row wraps past a full circle": func(p *BlockParams) {
			p.ArcRadius = 2
			p.SeatsPerRow = 10
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := base()
			mutate(&p)
			_, err := Generate(p)
			require.ErrorIs(t, err, ErrInvalidParams)
		})
	}
}

func TestPointJSONIsAPair(t *testing.T) {
	data, err := json.Marshal([]Point{{1.5, 2}, {0, -3.25}})
	require.NoError(t, err)
	assert.JSONEq(t, `[[1.5,2],[0,-3.25]]`, string(data))

	var back []Point
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, []Point{{1.5, 2}, {0, -3.25}}, back)

	require.Error(t, json.Unmarshal([]byte(`[{"x":1,"y":2}]`), &back))
}

func TestNumericRowLabels(t *testing.T) {
	layout, err := Generate(BlockParams{PitchX: 5, PitchY: 8, Rows: 12, SeatsPerRow: 14, Numbering: LeftToRight, Labels: LabelsNumeric})
	require.NoError(t, err)
	assert.Equal(t, "1", layout.Rows[0].Label)
	assert.Equal(t, "12", layout.Rows[11].Label)
	// "1214" could be row 1, 12 or 121; the dash makes the label unambiguous.
	assert.Equal(t, "12-14", layout.Rows[11].Seats[13].Label)

	alpha, err := Generate(BlockParams{PitchX: 5, PitchY: 8, Rows: 3, SeatsPerRow: 14, Numbering: LeftToRight})
	require.NoError(t, err)
	assert.Equal(t, "C14", alpha.Rows[2].Seats[13].Label, "the default is unchanged")

	_, err = Generate(BlockParams{PitchX: 5, PitchY: 8, Rows: 1, SeatsPerRow: 1, Numbering: LeftToRight, Labels: "ROMAN"})
	require.ErrorIs(t, err, ErrInvalidParams)
}

func TestParamErrorNamesTheField(t *testing.T) {
	cases := map[string]BlockParams{
		"rows":                {PitchX: 5, PitchY: 8, Rows: 0, SeatsPerRow: 1, Numbering: LeftToRight},
		"seats_per_row":       {PitchX: 5, PitchY: 8, Rows: 1, SeatsPerRow: 501, Numbering: LeftToRight},
		"seat_pitch":          {PitchX: 0, PitchY: 8, Rows: 1, SeatsPerRow: 1, Numbering: LeftToRight},
		"arc_radius":          {PitchX: 5, PitchY: 8, Rows: 1, SeatsPerRow: 1, Numbering: LeftToRight, ArcRadius: 2},
		"numbering_direction": {PitchX: 5, PitchY: 8, Rows: 1, SeatsPerRow: 1, Numbering: "UP"},
		"row_offsets":         {PitchX: 5, PitchY: 8, Rows: 2, SeatsPerRow: 1, Numbering: LeftToRight, RowOffsets: []float64{1}},
	}
	for field, p := range cases {
		err := p.Validate()
		require.ErrorIs(t, err, ErrInvalidParams, field)
		var pe *ParamError
		require.ErrorAs(t, err, &pe, field)
		assert.Equal(t, field, pe.Field)
		assert.Contains(t, err.Error(), "invalid block parameters: ")
	}
	// A row too long for its curve is blamed on the seat count.
	var pe *ParamError
	require.ErrorAs(t, BlockParams{PitchX: 5, PitchY: 8, Rows: 1, SeatsPerRow: 100, Numbering: LeftToRight, ArcRadius: 10}.Validate(), &pe)
	assert.Equal(t, "seats_per_row", pe.Field)
}

func TestBounds(t *testing.T) {
	_, ok := BoundsOf(nil)
	assert.False(t, ok)
	b, ok := BoundsOf([]Point{{X: 10, Y: 40}, {X: -5, Y: 60}, {X: 30, Y: 20}})
	require.True(t, ok)
	assert.Equal(t, Bounds{MinX: -5, MinY: 20, MaxX: 30, MaxY: 60}, b)
	assert.False(t, b.Within(100, 100), "x below zero")
	assert.True(t, Bounds{MinX: 0, MinY: 0, MaxX: 100, MaxY: 50}.Within(100, 50), "the edges are inside")
	assert.False(t, Bounds{MinX: 0, MinY: 0, MaxX: 100.01, MaxY: 50}.Within(100, 50))

	sb, ok := SeatBounds([]Point{{X: 50, Y: 100}, {X: 65, Y: 108}}, 5, 8)
	require.True(t, ok)
	assert.Equal(t, Bounds{MinX: 46, MinY: 96, MaxX: 69, MaxY: 112}, sb, "half the larger pitch on every side")
	_, ok = SeatBounds(nil, 5, 8)
	assert.False(t, ok)
}

// Rows and seats per row are each capped, but 200 × 500 is 100,000 seats in one block. The
// product has its own cap, and the error says it is the size, not either number alone.
func TestValidateCapsTheSizeOfABlock(t *testing.T) {
	p := base()
	p.OriginX, p.OriginY = 0, 0

	p.Rows, p.SeatsPerRow = 6, MaxSeatsPerRow // exactly 3,000
	require.Equal(t, MaxSeatsPerBlock, p.Rows*p.SeatsPerRow)
	require.NoError(t, p.Validate())
	layout, err := Generate(p)
	require.NoError(t, err)
	assert.Equal(t, MaxSeatsPerBlock, layout.SeatCount())

	for _, size := range [][2]int{{MaxRows, MaxSeatsPerRow}, {7, MaxSeatsPerRow}, {MaxRows, 16}, {61, 50}} {
		p.Rows, p.SeatsPerRow = size[0], size[1]
		err := p.Validate()
		var pe *ParamError
		require.ErrorAs(t, err, &pe, "%d × %d", size[0], size[1])
		assert.Equal(t, FieldBlockSize, pe.Field)
		assert.Contains(t, pe.Reason, "at most 3000 seats")
		_, err = Generate(p)
		require.ErrorIs(t, err, ErrInvalidParams, "Generate must refuse it before building anything")
	}
}
