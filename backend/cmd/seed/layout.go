package main

import (
	"fmt"
	"math"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
)

// The Talanta layout is made up; no real drawings were available. One plan unit is 10 cm, so
// a seat is 50 cm wide and a row 80 cm deep.
//
// The bowl is a rounded rectangle around a pitch whose long axis runs north–south. West and
// East are straight stands along the touchlines. North and South sit behind the goals and
// wrap round both corners, so each of them is a straight middle with a curved quarter at
// each end. Every stand has a Lower and an Upper tier, giving the eight stand rows.
const (
	venueName = "Talanta Stadium"
	venueCity = "Nairobi"

	planWidth  = 2200
	planHeight = 2500
	centreX    = planWidth / 2
	centreY    = planHeight / 2

	// Half-lengths of the straight parts of the bowl; the four corner centres sit at
	// (centreX ± halfEnd, centreY ± halfSide).
	halfSide = 480.0 // West and East
	halfEnd  = 300.0 // North and South

	pitchX = 5.0
	pitchY = 8.0
	aisle  = 6.0 // gap between neighbouring blocks, measured along the front row

	lowerFrontRadius = 150.0 // front row of the lower tier, from a corner centre
	lowerRows        = 28
	tierGap          = 40.0 // walkway between the tiers
	upperRows        = 31

	sideBlocks   = 10 // blocks along a touchline, per tier
	endBlocks    = 6  // straight blocks behind a goal, per tier
	cornerWedges = 2  // a corner is cut into this many wedges, each split front and back
)

const (
	tierLower = "LOWER"
	tierUpper = "UPPER"
)

const (
	catVVIP    = "VVIP"
	catVIP     = "VIP"
	catRegular = "Regular"
	catTerrace = "Terrace"
)

const (
	seatStandard       = "STANDARD"
	seatAccessible     = "ACCESSIBLE"
	seatCompanion      = "COMPANION"
	seatRestrictedView = "RESTRICTED_VIEW"
)

type standSpec struct {
	Code    string
	Name    string
	Tier    string
	Order   int32
	Polygon []seatmap.Point
	Blocks  []blockSpec
}

type blockSpec struct {
	Code   string
	Label  string
	Params seatmap.BlockParams
	Layout seatmap.Layout
	// Category is what the default template paints the whole block with.
	Category string
	// RowCategory overrides Category for single rows, keyed by row index.
	RowCategory map[int]string

	accessibleFront bool // wheelchair bays with companion seats on the front row
	underOverhang   bool // back corners look out under the upper tier
	gantry          bool // a few front seats removed for a camera position
}

// seatKind gives the type of the seat at column i of a row, and whether it exists for sale.
func (b blockSpec) seatKind(row, i int) (seatType string, active bool) {
	n := b.Params.SeatsPerRow
	switch {
	case b.gantry && row < 2 && i < 3:
		return seatStandard, false
	case b.accessibleFront && row == 0 && i < 4:
		// Bays come in pairs: a wheelchair space with its companion beside it.
		if i%2 == 0 {
			return seatAccessible, true
		}
		return seatCompanion, true
	case b.underOverhang && row >= b.Params.Rows-2 && (i < 2 || i >= n-2):
		return seatRestrictedView, true
	}
	return seatStandard, true
}

type tierSpec struct {
	name        string
	frontRadius float64
	rows        int
	series      int // block codes are <stand><series + n>, so W104 is lower and W204 upper
}

func tiers() []tierSpec {
	return []tierSpec{
		{name: tierLower, frontRadius: lowerFrontRadius, rows: lowerRows, series: 100},
		{name: tierUpper, frontRadius: lowerFrontRadius + lowerRows*pitchY + tierGap, rows: upperRows, series: 200},
	}
}

// straightRun is the straight part of a stand: where its front row starts for a tier whose
// front row is `radius` from the corner centres, which way the row runs, and how the block
// must be turned so that rows climb away from the pitch.
type straightRun struct {
	start    func(radius float64) seatmap.Point
	dirX     float64
	dirY     float64
	rotation float64
	length   float64
	blocks   int
}

// corner is a quarter-circle of seating about one corner centre. Angles are plan angles in
// degrees (0 = east, 90 = south, because y grows downwards) and run from `from` down to `to`,
// the direction in which seatmap lays out a curved row.
type corner struct {
	cx, cy   float64
	from, to float64
}

type standShape struct {
	code, name string
	before     *corner // curved quarter met first when walking along the stand's rows
	run        straightRun
	after      *corner
}

func standShapes() []standShape {
	west, east := centreX-halfEnd, centreX+halfEnd
	north, south := centreY-halfSide, centreY+halfSide
	return []standShape{
		{
			code: "N", name: "North Stand",
			before: &corner{cx: east, cy: north, from: 360, to: 270},
			run: straightRun{
				start: func(r float64) seatmap.Point { return seatmap.Point{X: east, Y: north - r} },
				dirX:  -1, rotation: 180, length: 2 * halfEnd, blocks: endBlocks,
			},
			after: &corner{cx: west, cy: north, from: 270, to: 180},
		},
		{
			code: "E", name: "East Stand",
			run: straightRun{
				start: func(r float64) seatmap.Point { return seatmap.Point{X: east + r, Y: south} },
				dirY:  -1, rotation: 270, length: 2 * halfSide, blocks: sideBlocks,
			},
		},
		{
			code: "S", name: "South Stand",
			before: &corner{cx: west, cy: south, from: 180, to: 90},
			run: straightRun{
				start: func(r float64) seatmap.Point { return seatmap.Point{X: west, Y: south + r} },
				dirX:  1, rotation: 0, length: 2 * halfEnd, blocks: endBlocks,
			},
			after: &corner{cx: east, cy: south, from: 90, to: 0},
		},
		{
			code: "W", name: "West Stand",
			run: straightRun{
				start: func(r float64) seatmap.Point { return seatmap.Point{X: west - r, Y: north} },
				dirY:  1, rotation: 90, length: 2 * halfSide, blocks: sideBlocks,
			},
		},
	}
}

// talantaLayout builds the whole venue in memory: stands, blocks, rows and seats with their
// plan coordinates. Nothing here touches the database.
func talantaLayout() ([]standSpec, error) {
	var stands []standSpec
	order := int32(0)
	for _, shape := range standShapes() {
		for _, tier := range tiers() {
			order++
			st := standSpec{
				Code:    shape.code,
				Name:    shape.name,
				Tier:    tier.name,
				Order:   order,
				Polygon: standPolygon(shape, tier),
			}

			var placed []placedBlock
			if shape.before != nil {
				placed = append(placed, cornerBlocks(*shape.before, tier)...)
			}
			placed = append(placed, straightBlocks(shape.run, tier)...)
			if shape.after != nil {
				placed = append(placed, cornerBlocks(*shape.after, tier)...)
			}

			for i, pb := range placed {
				layout, err := seatmap.Generate(pb.params)
				if err != nil {
					return nil, fmt.Errorf("block %d of %s %s: %w", i+1, shape.code, tier.name, err)
				}
				code := fmt.Sprintf("%s%d", shape.code, tier.series+i+1)
				b := blockSpec{
					Code:     code,
					Label:    "Block " + code,
					Params:   pb.params,
					Layout:   layout,
					Category: catRegular,
				}
				// Distance from the middle of the straight, in blocks: 0 is one of the two
				// centre blocks. Corner blocks are never "central".
				fromMiddle := -1
				if pb.straight {
					fromMiddle = abs(2*pb.index-(shape.run.blocks-1)) / 2
				}

				switch {
				case tier.name == tierUpper && (shape.code == "N" || shape.code == "S"):
					b.Category = catTerrace
				case shape.code == "W" && tier.name == tierLower && fromMiddle == 0:
					b.Category = catVVIP
				case shape.code == "W" && tier.name == tierLower && fromMiddle == 1:
					b.Category = catVIP
				case shape.code == "W" && tier.name == tierUpper && fromMiddle == 0:
					// The best view in the upper tier: the front five rows are sold as VIP
					// while the rest of the block stays Regular.
					b.RowCategory = map[int]string{0: catVIP, 1: catVIP, 2: catVIP, 3: catVIP, 4: catVIP}
				}

				b.underOverhang = tier.name == tierLower && pb.reachesBack
				b.accessibleFront = tier.name == tierLower && pb.straight && pb.index%3 == 0
				b.gantry = shape.code == "E" && tier.name == tierUpper && fromMiddle == 0
				st.Blocks = append(st.Blocks, b)
			}
			stands = append(stands, st)
		}
	}
	return stands, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// placedBlock is a block's geometry plus what the seeder needs to know about where it sits.
type placedBlock struct {
	params      seatmap.BlockParams
	straight    bool
	index       int  // position along the straight run; meaningless for corner blocks
	reachesBack bool // its last row is the last row of the tier
}

// straightBlocks cuts a straight run into equal blocks with an aisle between neighbours.
func straightBlocks(run straightRun, tier tierSpec) []placedBlock {
	slot := (run.length - float64(run.blocks-1)*aisle) / float64(run.blocks)
	seats := int(slot / pitchX)
	// Centre the seats in their slot so the leftover width is shared by both aisles.
	margin := (slot - float64(seats-1)*pitchX) / 2
	start := run.start(tier.frontRadius)

	out := make([]placedBlock, run.blocks)
	for k := range out {
		along := float64(k)*(slot+aisle) + margin
		out[k] = placedBlock{
			straight:    true,
			index:       k,
			reachesBack: true,
			params: seatmap.BlockParams{
				OriginX:     start.X + run.dirX*along,
				OriginY:     start.Y + run.dirY*along,
				RotationDeg: run.rotation,
				PitchX:      pitchX,
				PitchY:      pitchY,
				Rows:        tier.rows,
				SeatsPerRow: seats,
				Numbering:   seatmap.LeftToRight,
			},
		}
	}
	return out
}

// cornerBlocks fills a quarter-circle with curved blocks. The tier is split into a front and
// a back ring because a curved row gets wider with radius: one block the full depth of the
// tier would have its back seats more than twice as far apart as its front ones. Each ring
// sets its own seat count from its own front row.
func cornerBlocks(c corner, tier tierSpec) []placedBlock {
	frontRows := tier.rows / 2
	rings := []struct {
		radius float64
		rows   int
	}{
		{tier.frontRadius, frontRows},
		{tier.frontRadius + float64(frontRows)*pitchY, tier.rows - frontRows},
	}

	sweep := (c.from - c.to) / cornerWedges // degrees per wedge
	var out []placedBlock
	for w := 0; w < cornerWedges; w++ {
		mid := (c.from - (float64(w)+0.5)*sweep) * math.Pi / 180
		for r, ring := range rings {
			arc := ring.radius*sweep*math.Pi/180 - aisle
			seats := int(arc / pitchX)
			width := float64(seats-1) * pitchX

			// The middle of the front row lies on the wedge's centre line. seatmap bends
			// the row about a point `radius` behind that middle, which is the corner centre
			// once the block is turned to face outwards along the centre line.
			sin, cos := math.Sincos(mid)
			midX, midY := c.cx+ring.radius*cos, c.cy+ring.radius*sin
			out = append(out, placedBlock{
				reachesBack: r == len(rings)-1,
				params: seatmap.BlockParams{
					OriginX:     midX - width/2*sin,
					OriginY:     midY + width/2*cos,
					RotationDeg: math.Mod(mid*180/math.Pi-90+360, 360),
					ArcRadius:   ring.radius,
					PitchX:      pitchX,
					PitchY:      pitchY,
					Rows:        ring.rows,
					SeatsPerRow: seats,
					Numbering:   seatmap.LeftToRight,
				},
			})
		}
	}
	return out
}

// standPolygon outlines a stand row for the stadium overview: the band between the tier's
// front and back edges, followed along the stand's corners and straight.
func standPolygon(shape standShape, tier tierSpec) []seatmap.Point {
	inner := tier.frontRadius - pitchY/2
	outer := tier.frontRadius + float64(tier.rows-1)*pitchY + pitchY/2

	edge := func(radius float64) []seatmap.Point {
		var pts []seatmap.Point
		if shape.before != nil {
			pts = append(pts, arcPoints(*shape.before, radius)...)
		} else {
			pts = append(pts, shape.run.start(radius))
		}
		if shape.after != nil {
			pts = append(pts, arcPoints(*shape.after, radius)...)
		} else {
			s := shape.run.start(radius)
			pts = append(pts, seatmap.Point{X: s.X + shape.run.dirX*shape.run.length, Y: s.Y + shape.run.dirY*shape.run.length})
		}
		return pts
	}

	pts := edge(inner)
	back := edge(outer)
	for i := len(back) - 1; i >= 0; i-- {
		pts = append(pts, back[i])
	}
	for i := range pts {
		pts[i] = seatmap.Point{X: math.Round(pts[i].X*100) / 100, Y: math.Round(pts[i].Y*100) / 100}
	}
	return pts
}

// arcPoints samples a corner's quarter-circle every 7.5°, enough to look round on the
// overview without bloating the payload.
func arcPoints(c corner, radius float64) []seatmap.Point {
	const steps = 12
	pts := make([]seatmap.Point, 0, steps+1)
	for i := 0; i <= steps; i++ {
		a := (c.from - (c.from-c.to)*float64(i)/steps) * math.Pi / 180
		sin, cos := math.Sincos(a)
		pts = append(pts, seatmap.Point{X: c.cx + radius*cos, Y: c.cy + radius*sin})
	}
	return pts
}
