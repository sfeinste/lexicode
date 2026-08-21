package domain

// Board ordering is a fractional REAL per ticket (data model §4): dropping a card between two
// others assigns the midpoint of its neighbours, so a drag writes one row, not a renumbering of
// the column. PositionGap is the spacing between appended items; it leaves ~50 halvings between
// any two neighbours before float64 midpoints stop moving, which no human board will exhaust.
const PositionGap = 1024.0

// PositionBetween returns a position strictly between before and after, where zero values mean
// "no neighbour on that side":
//
//	PositionBetween(0, 0)      → first item in an empty column
//	PositionBetween(last, 0)   → append after last
//	PositionBetween(0, first)  → prepend before first
//	PositionBetween(a, b)      → drop between a and b (a < b)
//
// Positions are always > 0, so 0 is safe as the "none" sentinel.
func PositionBetween(before, after float64) float64 {
	switch {
	case before == 0 && after == 0:
		return PositionGap
	case after == 0:
		return before + PositionGap
	case before == 0:
		return after / 2
	default:
		return before + (after-before)/2
	}
}
