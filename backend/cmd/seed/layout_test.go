package main

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTalantaLayoutShape(t *testing.T) {
	stands, err := talantaLayout()
	require.NoError(t, err)
	require.Len(t, stands, 8, "four stands, two tiers each")

	total, largest, curved, straight := 0, 0, 0, 0
	codes := map[string]bool{}
	standRows := map[string]bool{}
	kinds := map[string]int{}
	inactive := 0

	for _, st := range stands {
		standRows[st.Code+"-"+st.Tier] = true
		assert.GreaterOrEqual(t, len(st.Polygon), 4, "%s %s polygon", st.Code, st.Tier)
		assert.GreaterOrEqual(t, len(st.Blocks), 10, "%s %s has too few blocks", st.Code, st.Tier)
		assert.LessOrEqual(t, len(st.Blocks), 20, "%s %s has too many blocks", st.Code, st.Tier)

		for _, b := range st.Blocks {
			require.False(t, codes[b.Code], "block code %s is used twice", b.Code)
			codes[b.Code] = true

			n := b.Layout.SeatCount()
			assert.GreaterOrEqual(t, n, 300, "block %s is too small", b.Code)
			assert.LessOrEqual(t, n, 2000, "block %s is too large for one seat map payload", b.Code)
			total += n
			largest = max(largest, n)
			if b.Params.ArcRadius > 0 {
				curved++
			} else {
				straight++
			}

			for _, row := range b.Layout.Rows {
				for i := range row.Seats {
					kind, active := b.seatKind(row.Index, i)
					kinds[kind]++
					if !active {
						inactive++
					}
				}
			}
			for _, pt := range b.Layout.Outline {
				assert.True(t, pt.X >= 0 && pt.X <= planWidth && pt.Y >= 0 && pt.Y <= planHeight,
					"block %s outline leaves the plan at %v", b.Code, pt)
			}
		}
	}

	for _, want := range []string{"N-LOWER", "N-UPPER", "S-LOWER", "S-UPPER", "E-LOWER", "E-UPPER", "W-LOWER", "W-UPPER"} {
		assert.True(t, standRows[want], "missing stand row %s", want)
	}
	assert.InDelta(t, 60000, total, 1000, "total seats")
	assert.Greater(t, largest, 1200, "one block must be big enough to need the Skia renderer")
	assert.Positive(t, curved)
	assert.Positive(t, straight)
	assert.Positive(t, kinds[seatAccessible])
	assert.Equal(t, kinds[seatAccessible], kinds[seatCompanion], "every wheelchair bay has a companion seat")
	assert.Positive(t, kinds[seatRestrictedView])
	assert.Positive(t, inactive, "some seats must be inactive so publish has something to skip")
	t.Logf("%d seats in %d blocks (%d curved), largest block %d, %d accessible, %d restricted view, %d inactive",
		total, len(codes), curved, largest, kinds[seatAccessible], kinds[seatRestrictedView], inactive)
}

func TestTalantaLayoutCategories(t *testing.T) {
	stands, err := talantaLayout()
	require.NoError(t, err)

	perCategory := map[string]int{}
	rowOverrides := 0
	for _, st := range stands {
		for _, b := range st.Blocks {
			perCategory[b.Category]++
			rowOverrides += len(b.RowCategory)
			if b.Category == catVIP || b.Category == catVVIP {
				assert.Equal(t, "W", st.Code, "VIP and VVIP are in the West stand only")
			}
		}
	}
	for _, c := range []string{catVVIP, catVIP, catRegular, catTerrace} {
		assert.Positive(t, perCategory[c], "no block is painted %s", c)
	}
	assert.Positive(t, rowOverrides, "the template must exercise the row override")
}

// Seats are generated block by block from independent parameters. If a rotation or an origin
// were wrong, two blocks would land on top of each other; this catches that across the whole
// venue by bucketing seats into a grid and comparing near neighbours only.
func TestTalantaLayoutSeatsDoNotOverlap(t *testing.T) {
	stands, err := talantaLayout()
	require.NoError(t, err)

	const minGap = 4.5 // just under one seat pitch
	type cell struct{ x, y int }
	type placed struct {
		x, y  float64
		label string
	}
	grid := map[cell][]placed{}

	for _, st := range stands {
		for _, b := range st.Blocks {
			for _, row := range b.Layout.Rows {
				for _, s := range row.Seats {
					require.True(t, s.X >= 0 && s.X <= planWidth && s.Y >= 0 && s.Y <= planHeight,
						"seat %s %s is off the plan", b.Code, s.Label)
					c := cell{int(s.X / minGap), int(s.Y / minGap)}
					for dx := -1; dx <= 1; dx++ {
						for dy := -1; dy <= 1; dy++ {
							for _, other := range grid[cell{c.x + dx, c.y + dy}] {
								if math.Hypot(s.X-other.x, s.Y-other.y) < minGap {
									t.Fatalf("seat %s %s is on top of %s", b.Code, s.Label, other.label)
								}
							}
						}
					}
					grid[c] = append(grid[c], placed{s.X, s.Y, b.Code + " " + s.Label})
				}
			}
		}
	}
}

// Every seat must sit inside the band its tier occupies, on the outside of the pitch. A block
// turned the wrong way would climb towards the centre circle instead.
func TestTalantaLayoutSeatsFaceThePitch(t *testing.T) {
	stands, err := talantaLayout()
	require.NoError(t, err)

	for _, st := range stands {
		for _, b := range st.Blocks {
			front := b.Layout.Rows[0].Seats[0]
			back := b.Layout.Rows[len(b.Layout.Rows)-1].Seats[0]
			dFront := math.Hypot(front.X-centreX, front.Y-centreY)
			dBack := math.Hypot(back.X-centreX, back.Y-centreY)
			assert.Greater(t, dBack, dFront, "block %s: the back row is nearer the pitch than the front row", b.Code)

			for _, row := range b.Layout.Rows {
				for _, s := range row.Seats {
					inPitch := math.Abs(s.X-centreX) < 340 && math.Abs(s.Y-centreY) < 525
					require.False(t, inPitch, "seat %s %s is on the pitch", b.Code, s.Label)
				}
			}
		}
	}
}
