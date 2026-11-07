package machine

// Database is the answer to a read of where a home's records are kept: PRD
// section 9's db path, for a reader that opens the database itself rather
// than going through this program's own views.
//
// It is one field because it answers one question. Whether the file exists,
// what schema it holds and what is in it are not here: the path is composed
// by internal/home from the home root the command acted on, and a reader
// given it learns the rest by opening it. A home nothing has written yet
// names a file that is not there, which is the honest answer to where the
// records of that home would be.
//
// It is an evidence document in P13's sense, the one whose rendering for a
// person is the value alone: a caller composes it into another command line
// (sqlite3 -readonly "$(assistant db path)"), so a phrase written before it
// would become part of what that caller substitutes. The sentence pointing a
// person at it is PRD section 9's and the README's, and this package holds no
// row for it; internal/cli's renderings table says the same from the
// rendering's side.
type Database struct {
	// Path is the database file of the home the command acted on, absolute
	// because the home root is.
	Path string `json:"path"`
}
