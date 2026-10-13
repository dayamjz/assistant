package principles

// unclaimed is the table of principles no test in this repository claims, and
// what a reader should know about the gap.
//
// A row is a declaration that the gap is known and looked at, and it is
// nothing else. It does not say the principle is unimplemented, does not say
// the gap is acceptable, and does not say the reason beside it is true: the
// reason is prose, written by whoever added the row, and no mechanism here
// checks it against the code. What the table buys is that the gap is
// enumerable and shows up in review as a line somebody had to write, rather
// than as an absence nobody can see.
//
// Check refuses a row whose principle a test does turn out to cite, so a row
// cannot outlive the gap it describes.
var unclaimed = map[Principle]string{}
