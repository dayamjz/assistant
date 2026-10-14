package vcs

import "context"

// automaticMaintenanceOff is the configuration DisableAutomaticMaintenance
// writes, in the order it writes it.
var automaticMaintenanceOff = [][2]string{
	{"gc.auto", "0"},
	{"maintenance.auto", "false"},
}

// DisableAutomaticMaintenance writes gc.auto=0 and maintenance.auto=false into
// this repository's own configuration, the file git config --local names from
// this handle, and into no other file: not the user's global configuration and
// not the system's.
//
// What it buys a caller is a repository whose own configuration does not ask
// git for automatic background maintenance. That is this program's measure
// against one source of git work it did not start running in a repository it
// owns at the same time as an operation it did start, and it is best-effort in
// the way the next paragraph says. It is meant for repositories the program
// owns, and nothing here decides which those are: a caller that owns no
// repository has no business calling it, and PRD principle P1 forbids calling
// it on a person's own checkout.
//
// It is a write of two keys and nothing more. It does not stop git work a
// caller asks for explicitly, and nothing here observes whether a git process
// run in the repository later does any maintenance; the claim is what the
// repository's own configuration says, which a test reads back. Running it
// again rewrites the same two values, so a caller may repeat it on a
// repository that already carries them.
func (r *Repository) DisableAutomaticMaintenance(ctx context.Context) error {
	for _, kv := range automaticMaintenanceOff {
		if _, err := r.run(ctx, "config-set", "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}
