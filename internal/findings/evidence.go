package findings

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Demand is what a run asks a review report to answer to: the commit the
// review stage was asked to read, and the paths the change touched. Both come
// from the run and neither comes from the reviewer, which is what makes the
// binding able to fail. A reviewer cannot move the target by reporting a
// different commit, and it cannot enlarge what counts as "beyond the change"
// by leaving a file out of a list.
type Demand struct {
	// Revision is the commit the stage was asked to review, spelled as the run
	// spells it. ParseReviewReport compares the report's own revision against
	// it after trimming surrounding space and in no other way, so the two
	// sides have to agree on how a commit is written down.
	Revision string
	// Touched is the repository-relative paths the change touched. It is one
	// half of the comparison the binding reports; the other half is what the
	// reviewer declared reading. Order is preserved and repeats are collapsed.
	Touched []string
}

// Empty reports whether this demand carries nothing at all. It exists so a
// caller can ask whether a demand was supplied without enumerating the fields
// of a type this package owns, and so that a field added here later is covered
// by changing this method rather than every caller that asks the question.
//
// It is a narrower question than Validate answers, and deliberately: a demand
// carrying anything at all is not empty, including a revision that is only
// spaces and paths that are all empty once trimmed. A caller refusing a demand
// where none belongs therefore refuses a half-filled one rather than reading
// it as absent, and a caller that needs a demand it can act on asks Validate.
func (d Demand) Empty() bool {
	return d.Revision == "" && len(d.Touched) == 0
}

// Validate reports whether this demand can be answered at all. A caller checks
// it before it spends an invocation, because a demand this refuses would
// refuse the reviewer's answer after the fact.
func (d Demand) Validate() error {
	if strings.TrimSpace(d.Revision) == "" {
		return ErrNoRevision
	}
	if len(dedupePaths(d.Touched)) == 0 {
		return ErrNoTouched
	}
	return nil
}

// Refusal is one finding the binding refused and the path it named that the
// evidence set does not hold. The finding is kept as the reviewer wrote it,
// before normalizing, so a caller reporting the refusal quotes the claim
// rather than a repaired copy of it.
type Refusal struct {
	// Finding is the finding as the reviewer wrote it.
	Finding Finding
	// Path is the first path it named that the evidence set does not hold.
	// A finding naming several such paths is refused once and reports the
	// first, in the order Location then Cites.
	Path string
}

// Binding is what ParseReviewReport made of the reviewer's evidence: the two
// path sets it compared, each one's part the other does not hold, and what
// that cost the reviewer's findings.
//
// The comparison is reported whether or not it found anything, because that is
// the whole discriminator. A reviewer satisfies a rule that only asks for an
// evidence set by declaring every changed file and reading nothing but the
// diff, and that declaration is derivable from the run without asking. Beyond
// is what tells the two apart, and an empty Beyond is a permitted answer, not
// an error: small changes need nothing else. What it may not be is invisible.
//
// The comparison runs both ways for the same reason. Undeclared is the part of
// the change the reviewer did not say it read, and a reviewer that read some
// of the change is as permitted as one that read all of it and as visible: an
// empty Beyond alone does not say whether the review covered the change or a
// corner of it, and the two read alike unless the other half is reported too.
type Binding struct {
	// Revision is the commit the report and the demand agreed on.
	Revision string
	// Read is the evidence set the reviewer declared, trimmed, with empty
	// entries dropped and repeats collapsed, in the order it listed them.
	Read []string
	// Touched is the paths the change touched, on the same terms.
	Touched []string
	// Beyond is the paths in Read that the change did not touch, in Read's
	// order. It is the discriminating part: it is how far a finding was
	// allowed to reach past the change, and a reviewer that never reads past
	// the diff reports it empty every time rather than invisibly.
	Beyond []string
	// Undeclared is the paths the change touched that Read does not hold, in
	// Touched's order. It is how much of the change the review did not say it
	// read, so a review of one file out of twenty is a fact a person can see
	// and a caller can count, rather than something an empty Beyond hides. It
	// is permitted and refuses nothing by itself; what it costs the reviewer
	// is that a finding naming any of these paths is refused for want of
	// evidence like any other.
	Undeclared []string
	// Refused is every finding the evidence set did not support, in the order
	// the report listed them. Each is reported in the bound report as an
	// informational finding quoting what it claimed, so a refusal is visible
	// to the person rather than quietly trimmed.
	Refused []Refusal
	// Demoted is every finding that named no path and cited none, as the
	// reviewer wrote it. Each survives in the bound report as a note.
	Demoted []Finding
}

// ReadBeyondChange reports whether the reviewer declared reading anything the
// change did not touch. That is what Beyond holds and the whole of what this
// answers.
//
// It is false for three reviews that are not the same: one that declared
// exactly the paths the change touched, one that declared some of them, and
// one that declared nothing at all. Undeclared is the half that tells them
// apart, so the review that declared exactly the change is the one this
// reports false for and whose Undeclared is empty, and a caller counting that
// case across runs asks both rather than this alone.
func (b Binding) ReadBeyondChange() bool { return len(b.Beyond) > 0 }

// Errors a caller is expected to handle. Each is a typed result, never a
// warning a review continues past.
var (
	// ErrNoRevision is returned for a Demand naming no commit. The first thing
	// a review report answers for is which commit it read, and a demand with
	// nothing to compare against would credit a report of any commit at all,
	// which is a check that passes without checking anything.
	ErrNoRevision = errors.New("findings: a review demand needs the commit the stage was asked to review")

	// ErrNoTouched is returned for a Demand naming no path: a Touched that is
	// absent, empty, or holds only entries that are empty once trimmed.
	//
	// The touched paths are one half of the comparison the binding reports. A
	// demand without them can still bind findings to the evidence set, but it
	// cannot say which part of that set reaches past the change, so every path
	// the reviewer read would be reported as beyond a change that touched
	// nothing. That is a reported fact nobody can act on, and reporting it
	// wrongly is worse than refusing.
	//
	// A legitimate run can arrive this way rather than only a caller's
	// mistake: ignore patterns exclude paths from review, this package applies
	// none of that filtering itself, and a change touching only ignored paths
	// therefore reaches a caller with every path removed while the change
	// itself is not empty. A caller meeting this refusal has nothing to review
	// and records that review was skipped, rather than failing the stage over
	// a shape that is not the change's fault.
	ErrNoTouched = errors.New("findings: a review demand needs at least one path the change touched")

	// ErrWrongRevision is returned when a review report's own revision is not
	// the commit the run asked about, including when the report states none at
	// all. A reading of some other commit is not a review of this change, so
	// it is refused rather than credited: crediting it would let a review of
	// an ancestor, of a stale working copy, or of nothing in particular clear
	// the stage. The refusal quotes both sides.
	ErrWrongRevision = errors.New("findings: the review reports a revision the run did not ask about")
)

// Guidance returns the evidence section the review stage puts in front of its
// reviewer. It names the revision to report and every path the change touched,
// and it says what the binding will do with the answer, including the part
// that makes declaring the whole diff worth nothing. It returns the same
// refusals Validate does.
//
// It is a method on the demand it describes because the two must not drift:
// what this text asks the reviewer for is exactly what ParseReviewReport binds
// against, and a reviewer held to a rule nobody stated is a trap rather than a
// check.
//
// The text asks for paths back exactly as listed, because exact equality after
// trimming surrounding space is the comparison the binding makes. Asking is
// all it does: a path the reviewer retypes, reformats, or re-cases names a
// different path than the one listed, which costs the reviewer the finding
// rather than buying it one.
func (d Demand) Guidance() (string, error) {
	if err := d.Validate(); err != nil {
		return "", err
	}
	revision := strings.TrimSpace(d.Revision)
	touched := dedupePaths(d.Touched)

	var b strings.Builder
	b.WriteString("Evidence: every finding you report is bound to what you actually read.\n\n")
	b.WriteString("Report two things about yourself, and expect both to be checked.\n\n")
	b.WriteString(`  - "revision" is the commit you reviewed. It must be exactly ` + revision +
		". A report of any other revision is refused whole, findings and all, because a " +
		"reading of some other commit is not a review of this change.\n")
	b.WriteString(`  - "read" is a list of every repository-relative path you actually opened ` +
		"and read. List all of them and list nothing else.\n\n")
	b.WriteString("Your findings are then checked against that list.\n\n")
	b.WriteString(`  - A finding whose "location" names a path outside the list is refused, ` +
		"and the refusal is reported with the path it named.\n")
	b.WriteString(`  - A finding that asserts something about code outside this change - a caller, ` +
		`an interface, a test that covers it - names that code in "cites", a list of ` +
		`repository-relative paths, and is refused unless "read" names it too.` + "\n")
	b.WriteString("  - Those two are asked first and decide on their own, whatever action you " +
		"gave a finding and whether or not you gave it one. A finding that survives them and " +
		"names no path at all is recorded as informational and will not stop the run, where " +
		"you stated an action this system recognizes; where its action is missing, empty, or " +
		"a word this system cannot read, it is not recorded that way and still goes to a " +
		"person.\n\n")
	b.WriteString("Listing the paths below and reading nothing else buys you nothing. It is a " +
		"permitted answer and it is reported as one, but it forfeits every finding that reaches " +
		"past the change, which is most of what an independent review is for. Listing a path you " +
		"did not read is worse than listing none: this is the list your findings are checked " +
		"against, not a list of what you might have read.\n\n")
	b.WriteString("The change touches these paths. Repeat any path you report exactly as it is " +
		"listed here, character for character: paths are matched by exact string equality after " +
		"trimming surrounding space, so a path you retype, reformat, or re-case names a different " +
		"path than this one.\n")
	for _, path := range touched {
		b.WriteString("  - ")
		b.WriteString(path)
		b.WriteString("\n")
	}
	return b.String(), nil
}

// bindEvidence binds a decoded review report to the evidence it declared and
// returns the report as it stands afterwards. It runs on the report as it was
// decoded and before Normalize, which is what lets it tell a finding whose
// action the reviewer stated from one P3 defaulted; see the comment on the
// demotion below.
//
// It is unexported on purpose. Reaching it requires ParseReviewReport, so
// there is no way to bind a report that has been through Normalize, through
// storage, or through a caller's own struct literal, in each of which the
// stated action is already gone and the demotion below would silently take a
// hold P3 fixed and turn it into a note.
func bindEvidence(r Report, d Demand) (Report, Binding, error) {
	revision := strings.TrimSpace(r.Revision)
	if revision != strings.TrimSpace(d.Revision) {
		return Report{}, Binding{}, fmt.Errorf("%w: it reports %q, the run asked about %q",
			ErrWrongRevision, revision, strings.TrimSpace(d.Revision))
	}

	binding := Binding{
		Revision: revision,
		Read:     dedupePaths(r.Read),
		Touched:  dedupePaths(d.Touched),
	}
	read := indexPaths(binding.Read)
	touched := indexPaths(binding.Touched)
	for _, path := range binding.Read {
		if _, ok := touched[path]; !ok {
			binding.Beyond = append(binding.Beyond, path)
		}
	}
	for _, path := range binding.Touched {
		if _, ok := read[path]; !ok {
			binding.Undeclared = append(binding.Undeclared, path)
		}
	}

	bound := make([]Finding, 0, len(r.Findings)+1)
	for _, f := range r.Findings {
		named := f.namedPaths()
		// A refusal is decided by what the finding names, never by what it
		// asks for, so it applies to an action P3 would have defaulted
		// exactly as it applies to a stated one. The carve-out PRD section 5
		// makes for P3 is on the demotion below and on nothing else: an
		// unclassified claim about code the reviewer did not read is
		// unsupported whoever was going to resolve it, and it is reported as
		// refused rather than dropped, so the person still sees it.
		if outside, ok := firstOutside(named, read); ok {
			binding.Refused = append(binding.Refused, Refusal{Finding: f, Path: outside})
			bound = append(bound, refusedNote(f, outside, read))
			continue
		}
		// A finding that named nothing rests on nothing, so it informs and
		// does not stop the run. The test is on the action the reviewer
		// stated, not on the action after P3 resolved it: a finding whose
		// action was missing, empty, or unreadable is an ask P3 made, and P3
		// outranks this rule. Normalize has not run yet, which is the only
		// point at which those two asks are still distinguishable.
		if len(named) == 0 && f.Action.Stated() {
			binding.Demoted = append(binding.Demoted, f)
			bound = append(bound, demotedNote(f))
			continue
		}
		bound = append(bound, f)
	}
	bound = append(bound, evidenceNote(binding))

	r.Revision = revision
	r.Read = binding.Read
	r.Findings = bound
	return r, binding, nil
}

// namedPaths returns every repository-relative path this finding rests on: its
// location's path first, then what it cites, trimmed, with empty entries
// dropped and repeats collapsed.
//
// A location carrying a line and no path names no path. A line on its own
// points into a file nobody named, so it supports nothing and cannot be
// checked against anything.
func (f Finding) namedPaths() []string {
	named := make([]string, 0, 1+len(f.Cites))
	if path := strings.TrimSpace(f.Location.Path); path != "" {
		named = append(named, path)
	}
	return dedupePaths(append(named, f.Cites...))
}

// firstOutside returns the first path not in the set, in order, and whether
// there was one.
func firstOutside(paths []string, set map[string]struct{}) (string, bool) {
	for _, path := range paths {
		if _, ok := set[path]; !ok {
			return path, true
		}
	}
	return "", false
}

// refusedNote is what a refused finding becomes in the bound report. It is a
// note, so it blocks nothing and enters no fix loop, and it quotes the claim
// rather than replacing it, so the refusal is something a person can read and
// disagree with. Why it was refused, and the path the evidence set does not
// hold, are in the description.
//
// It carries the refused finding's location when that location's path is in
// the evidence set, and carries none when it is not. The question is asked of
// the location at hand and of nothing else: is this path supported, as things
// stand. It is deliberately not asked of how the refusal came about, which of
// the paths the finding named was the one outside the set or in what order
// they were scanned, because that is a fact about the refusal standing in for
// a fact about the value, and it stops being true as soon as another refusal
// path is added, while the present-tense question stays true however those
// grow. So a finding sitting in code the reviewer did read, refused for
// something it cites, still tells a person where to look, and a finding
// refused for its own location names no place, because the only place it named
// is the one nothing supports.
//
// A location cannot make this note read as a live finding: it is ActionNote by
// construction and there is no input that makes it anything else, so nothing
// deciding on the action sees a location here at all.
//
// It keeps the refused finding's identifier when it had one, so a person
// holding the reviewer's own numbering can find it.
func refusedNote(f Finding, path string, read map[string]struct{}) Finding {
	note := Finding{
		ID:       f.ID,
		Severity: SeverityInfo,
		Action:   ActionNote,
		Description: "Refused for want of evidence: this finding names " + path +
			", which is not among the " + strconv.Itoa(len(read)) + " " + pathWord(len(read)) +
			" the review declared reading, so nothing it read supports the claim. " +
			"It is recorded here and blocks nothing. The review said: " +
			strings.TrimSpace(f.Description),
	}
	if _, supported := read[strings.TrimSpace(f.Location.Path)]; supported {
		note.Location = f.Location
	}
	return note
}

// demotedNote is what a finding resting on nothing becomes. It keeps
// everything the reviewer wrote and adds why it informs rather than acts.
func demotedNote(f Finding) Finding {
	f.Action = ActionNote
	f.Description = strings.TrimSpace(f.Description) +
		" [Recorded as informational: the review gave this finding no location and cited " +
		"no path, so nothing it read supports it.]"
	return f
}

// evidenceNote is the comparison itself, reported every time. An evidence set
// equal to the touched paths is permitted, so the note for that case says so
// rather than complaining; what it may not be is absent, because a reviewer
// that never reads past the change is then a fact a person can see across runs
// instead of a possibility they have to assume away.
//
// Reading part of the change is permitted on the same terms and is told apart
// from reading all of it, because the two are different facts and only one of
// them is the answer a whole-diff reading gives. The note says which, and what
// each cost the reviewer's findings; neither is judged and neither refuses
// anything by itself.
func evidenceNote(b Binding) Finding {
	read := strconv.Itoa(len(b.Read)) + " " + pathWord(len(b.Read))
	var description string
	switch {
	case len(b.Read) == 0:
		description = "Evidence: the review declared reading no path at all, so every finding " +
			"it located anywhere was refused. Nothing here says the review read nothing; it " +
			"says nothing it reported can be checked against what it read."
	case len(b.Beyond) == 0 && len(b.Undeclared) == 0:
		description = "Evidence: the review declared reading " + read +
			", all of them touched by this change, and nothing beyond " +
			"the change itself. That is permitted and is what a small change needs. It also " +
			"means any finding reaching past the change was refused for want of evidence."
	case len(b.Beyond) == 0:
		description = "Evidence: the review declared reading " + read +
			", all of them touched by this change and nothing beyond it, and did not declare " +
			"reading " + strconv.Itoa(len(b.Undeclared)) + " of the " +
			strconv.Itoa(len(b.Touched)) + " " + pathWord(len(b.Touched)) +
			" this change touched: " + strings.Join(b.Undeclared, ", ") +
			". That is permitted and is reported rather than judged. It means any finding " +
			"about one of those paths, and any finding reaching past the change, was refused " +
			"for want of evidence."
	default:
		description = "Evidence: the review declared reading " + read + ", of which " +
			strconv.Itoa(len(b.Beyond)) + " " +
			isAre(len(b.Beyond)) + " not touched by this change: " + strings.Join(b.Beyond, ", ") +
			". A finding was allowed to reach that far past the change and no further."
		if len(b.Undeclared) > 0 {
			description += " It did not declare reading " + strconv.Itoa(len(b.Undeclared)) +
				" of the " + strconv.Itoa(len(b.Touched)) + " " + pathWord(len(b.Touched)) +
				" this change touched: " + strings.Join(b.Undeclared, ", ") + "."
		}
	}
	return Finding{Severity: SeverityInfo, Action: ActionNote, Description: description}
}

// pathWord agrees the noun with the count, so a report reads as prose rather
// than as a template.
func pathWord(n int) string {
	if n == 1 {
		return "path"
	}
	return "paths"
}

// isAre agrees the verb with the count, for the same reason.
func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// dedupePaths trims each path and returns the non-empty ones in their original
// order with repeats collapsed, so a path listed twice is one entry in the
// evidence set and one entry in a finding's citations.
func dedupePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}

// indexPaths indexes a deduplicated path list for lookup.
func indexPaths(paths []string) map[string]struct{} {
	out := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		out[path] = struct{}{}
	}
	return out
}
