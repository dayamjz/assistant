package principles

import "strconv"

// Principle is one of the numbered principles in section 4 of docs/prd.html.
// The PRD owns the list; the constants below are how a test names one in code,
// and Check pins them to the PRD in both directions, so a principle the PRD
// gains and this block does not is a failure rather than a silent omission.
type Principle uint8

// The principles, in the order the PRD lists them. A constant here is an
// identifier for a principle, not a statement of it: what P6 requires is in
// the PRD and nowhere else, per P14.
const (
	P1 Principle = iota + 1
	P2
	P3
	P4
	P5
	P6
	P7
	P8
	P9
	P10
	P11
	P12
	P13
	P14
)

// last is the final constant above. All counts to it, so a principle added to
// the block belongs to this package the moment it is written down, and the
// only way to add one that All does not report is to add it out of order.
const last = P14

// All returns the principles this package has an identifier for, ascending.
// It is what Check compares against the PRD's own list.
func All() []Principle {
	out := make([]Principle, 0, last)
	for p := P1; p <= last; p++ {
		out = append(out, p)
	}
	return out
}

// String returns the principle's PRD name, such as "P6". A value this package
// has no constant for still prints its number, because the values Check
// reports as unaccounted for are exactly those values.
func (p Principle) String() string { return "P" + strconv.Itoa(int(p)) }

// known reports whether this package has a constant for the principle. It is
// not a claim that the PRD still lists it: that question is Check's, and it is
// answered against the PRD rather than against this block.
func (p Principle) known() bool { return p >= P1 && p <= last }
