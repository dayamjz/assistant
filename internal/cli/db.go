package cli

import (
	"context"
	"flag"

	"github.com/dayamjz/assistant/internal/machine"
)

// dbVerbName is the verb a reader of the records drives, PRD section 9's db.
const dbVerbName = "db"

// dbActions is the subcommand of that verb, which PRD section 9's table gives
// one row. The list is the one owner of which there are: it names what a
// command line may hold and it runs the one that was named, so the verb's
// help and its refusal cannot name a subcommand it does not dispatch.
//
// A second row is a finding against the specification rather than an
// addition here, on the verb table's own terms: this verb is in that table
// because section 9 describes it, and what it answers is that section's to
// describe.
var dbActions = []subcommand{
	{"path", "Print the database file of this home, for a reader that opens it itself.", databasePath},
}

// dbVerb runs the subcommand the command line names, on
// dispatchSubcommand's terms, which the service verb shares.
func dbVerb(ctx context.Context, in *invocation) (any, error) {
	return dispatchSubcommand(ctx, in, dbVerbName, dbActions)
}

// databasePath answers with the database file of the home the command acted
// on: PRD section 9's db path, for a person or an agent that opens the
// records with a reader of their own instead of through this program's views.
//
// The path is internal/home's, the one owner of where a home's files are
// (P14). This opens no database, creates no home and asks the service
// nothing, so it answers the same with the service up or down, which is when
// a person debugging a stuck home asks, and a home whose database has never
// been written names the file that would hold its records, which is the
// honest answer to where they are. The one filesystem read behind the answer
// is the home root's own resolution, which internal/home does when the
// invocation resolves the home: a symlinked root answers as its target.
//
// The answer is read-only in the only sense this command can promise: it
// hands over a path and does not open it. What a reader then does with the
// file is the reader's, so the sentence telling a person to open it read-only
// is PRD section 9's and the README's, and nothing here enforces it. The
// rendering writes the path alone, with a control character escaped as every
// line this program prints escapes one; the structured answer carries the
// bytes.
func databasePath(_ context.Context, in *invocation) (any, error) {
	if err := in.parseFlags(dbVerbName+" path", func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	return machine.Database{Path: in.home.Database()}, nil
}
