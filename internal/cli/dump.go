package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// dumpRecords is PRD section 9's dump: every record the resolver reads, plus
// wakes and kept work, as one JSON document from a read-only open that asks
// the service nothing. Text fields the store may hold verbatim are scrubbed
// through internal/redact on the way out.
func dumpRecords(ctx context.Context, in *invocation) (any, error) {
	if err := in.parseArgs("dump", 1, func(*flag.FlagSet) {}); err != nil {
		return nil, err
	}
	records, err := store.OpenReadOnly(ctx, in.home.Database())
	if err != nil {
		return nil, err
	}
	defer func() { _ = records.Close() }()

	var dumped store.Dump
	if len(in.args) == 1 {
		dumped, err = records.DumpTask(ctx, in.args[0])
	} else {
		dumped, err = records.Dump(ctx)
	}
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, usagef("unknown task %q", in.args[0])
		}
		return nil, err
	}
	scrubDump(&dumped, redact.New())
	return dumped, nil
}

func scrubDump(d *store.Dump, r redact.Redactor) {
	for i := range d.Tasks {
		snap := &d.Tasks[i]
		if intent, ok := snap.Task.DeliveryIntent.Get(); ok {
			snap.Task.DeliveryIntent = store.Known(r.Redact(intent))
		}
		for j := range snap.Runs {
			snap.Runs[j].Intent = r.Redact(snap.Runs[j].Intent)
		}
		for j := range snap.Holds {
			snap.Holds[j].Subject = r.Redact(snap.Holds[j].Subject)
			snap.Holds[j].Detail = r.Redact(snap.Holds[j].Detail)
			if note, ok := snap.Holds[j].ResolutionNote.Get(); ok {
				snap.Holds[j].ResolutionNote = store.Known(r.Redact(note))
			}
		}
		for j := range snap.Events {
			snap.Events[j].Detail = r.Redact(snap.Events[j].Detail)
		}
	}
	for i := range d.KeptWork {
		d.KeptWork[i].Detail = r.Redact(d.KeptWork[i].Detail)
		if text, ok := d.KeptWork[i].Unestablished.Get(); ok {
			d.KeptWork[i].Unestablished = store.Known(r.Redact(text))
		}
	}
}

func readOutDump(w io.Writer, d machine.Dump) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(d); err != nil {
		writef(w, "encoding the dump: %v\n", err)
	}
}
