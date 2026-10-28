package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// passStep is one write of the launch-retirement exclusion's interleaving
// test: a launch's attempt claim, launch claim and session record, and the
// retirement's offer and answer.
type passStep byte

const (
	stepAttempt passStep = 'A'
	stepLaunch  passStep = 'L'
	stepSession passStep = 'S'
	stepOffer   passStep = 'O'
	stepRetire  passStep = 'R'
)

// interleavings is every order of the launch's three writes and the
// retirement's two that keeps each side's own order.
func interleavings(launch, retire []passStep) [][]passStep {
	if len(launch) == 0 {
		return [][]passStep{append([]passStep(nil), retire...)}
	}
	if len(retire) == 0 {
		return [][]passStep{append([]passStep(nil), launch...)}
	}
	var out [][]passStep
	for _, rest := range interleavings(launch[1:], retire) {
		out = append(out, append([]passStep{launch[0]}, rest...))
	}
	for _, rest := range interleavings(launch, retire[1:]) {
		out = append(out, append([]passStep{retire[0]}, rest...))
	}
	return out
}

// PRD section 6's housekeeping exclusion, the invariant the store's one
// guard holds (housekeepinglaunch.go): no retirement hold stands, and no
// retirement is written, while a launch of the pass is under way, from its
// launch claim to the record of its session; and no launch is claimed or
// recorded while a retirement hold stands or once the pass is retired.
// Every interleaving of a launch (attempt claim, launch claim, session
// record) with a retirement (offer, retire) is played against a real store,
// each write deciding on what it reads then, as a loop reading the pass just
// before it writes would, and after every write the invariant is checked
// on the store's own records: a launch whose claim was written and whose
// session record has not yet run never coexists with an open retirement
// hold or a retirement. A refused write ends its side, as its caller would:
// a refused claim starts no worker, a refused record has its worker ended,
// and a refused offer leaves nothing to answer. The control is the order
// in which the launch ends before the offer: every write is accepted
// (P6, P10).
func TestALaunchUnderWayAndARetirementExcludeEachOther(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	principles.Cite(t, principles.P10)
	ctx := context.Background()
	orders := interleavings([]passStep{stepAttempt, stepLaunch, stepSession}, []passStep{stepOffer, stepRetire})
	if len(orders) != 10 {
		t.Fatalf("%d interleavings, want 10", len(orders))
	}
	for _, order := range orders {
		name := string(order)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := openStore(t)
			seedRepository(t, s)
			inc := incarnationOf(t, s)
			const pass = "taskhkxclude"
			mustPass(t, s, pass)
			// What each side has done: a step whose side was refused before
			// it is skipped, as its caller would never reach it.
			attempted, claimed, offered := false, false, ""
			launchOver, retireOver := false, false
			accepted := map[passStep]bool{}
			read := func() (int64, string) {
				t.Helper()
				attempts, err := s.TaskAttempts(ctx, pass)
				if err != nil {
					t.Fatal(err)
				}
				if n := len(attempts); n > 0 {
					return attempts[n-1].Sequence, attempts[n-1].SessionRef.Or("")
				}
				return 0, ""
			}
			standing := func() bool {
				t.Helper()
				if offered != "" {
					h, err := s.Hold(ctx, offered)
					if err != nil {
						t.Fatal(err)
					}
					if h.Open() {
						return true
					}
				}
				_, found, err := s.RetirementOf(ctx, pass)
				if err != nil {
					t.Fatal(err)
				}
				return found
			}
			for _, step := range order {
				var err error
				switch step {
				case stepAttempt:
					if launchOver {
						continue
					}
					_, err = s.BeginFirstTaskAttempt(ctx, pass, OnThisMachine())
					attempted = err == nil
				case stepLaunch:
					if launchOver || !attempted {
						continue
					}
					err = s.ClaimAttemptLaunch(ctx, pass, 1)
					claimed = err == nil
				case stepSession:
					if launchOver || !claimed {
						continue
					}
					err = s.SetAttemptSession(ctx, pass, 1, "pane-"+name, SessionHost{})
					// Recorded, or refused and its worker ended: either way
					// the launch is no longer under way.
					claimed = false
				case stepOffer:
					if retireOver {
						continue
					}
					attempt, session := read()
					key := fmt.Sprintf("task/%s/housekeeping-retire/1/%d/-", pass, attempt)
					_, err = s.OfferRetirementHold(ctx, inc, RetirementOffer{
						Hold:    Hold{Key: key, TaskID: Known(pass), Subject: "retire?", Detail: "d"},
						Attempt: attempt, Session: session, Ended: ended, Delivered: relayed,
					})
					if err == nil {
						offered = key
					}
				case stepRetire:
					if retireOver || offered == "" {
						continue
					}
					attempt, session := read()
					_, err = s.RetireHousekeepingPass(ctx, Retirement{Task: pass, Attempt: attempt, Session: session, Hold: offered,
						By: ResolvedByMachineInterface(), Relaunches: func(string) bool { return false }, Ended: ended, Delivered: relayed})
				}
				accepted[step] = err == nil
				if err != nil {
					if !errors.Is(err, ErrPassHeld) && !errors.Is(err, ErrPassRetired) && !errors.Is(err, ErrPassMoves) {
						t.Fatalf("%s: step %c was refused with %v, which is no refusal of the exclusion", name, step, err)
					}
					switch step {
					case stepAttempt, stepLaunch, stepSession:
						launchOver = true
					default:
						retireOver = true
					}
				}
				if claimed && standing() {
					t.Fatalf("%s: after step %c a launch is under way (claimed, its session not yet recorded) while a retirement hold stands or the pass is retired",
						name, step)
				}
			}
			if name == "ALSOR" {
				for _, step := range order {
					if !accepted[step] {
						t.Fatalf("%s: step %c was refused, want every write accepted when the launch ends before the offer", name, step)
					}
				}
			}
			if strings.Index(name, "L") < strings.Index(name, "O") && strings.Index(name, "O") < strings.Index(name, "S") && accepted[stepOffer] {
				t.Fatalf("%s: the offer was accepted between the launch's claim and its session's record", name)
			}
		})
	}
}
