package fixture

import (
	"fmt"
	"os"
	"path/filepath"
)

// buildRemoteAdvanced builds a subject whose remote is advanced out of band
// after a run has observed it. The advance itself cannot be planted here: a
// remote that is already ahead when the run starts is a remote the run
// observes ahead, and P6 is about the target moving after the observation. So
// the branch is published and a second clone is left ready, and the advance is
// deferred to AdvanceRemoteOutOfBand.
func buildRemoteAdvanced(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioRemoteAdvanced,
		"A push refused because the branch moved on the remote after the run observed it.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s, "go test ./..."); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggy); err != nil {
		return nil, nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "narrow the Total loop bound")
	if err != nil {
		return nil, nil, err
	}
	s.Commits["branch-change"] = commit
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}

	colleague := filepath.Join(s.Root, "colleague")
	if _, err := b.git.run(s.Root, "clone", "--quiet", s.Origin, colleague); err != nil {
		return nil, nil, err
	}
	if _, err := b.git.run(colleague, "checkout", "--quiet", s.Branch); err != nil {
		return nil, nil, err
	}
	s.Paths["colleague"] = colleague

	return s, []Condition{{
		ID:        "refusal-remote-advanced-out-of-band",
		Scenario:  s.Name,
		Kind:      KindRefusal,
		Principle: "P6",
		Planted: "A second clone of the same remote, checked out on the branch under validation and ready to " +
			"commit. The advance happens by a real push from it, so the commit the run would drop is one " +
			"somebody else actually landed rather than a reference written by hand.",
		Mechanism: "safety.Guard.Decide, over an anchor from Guard.Observe taken before the advance",
		Expect: Outcome{
			Summary: "The update is refused. The fresh read finds a commit the run's proposed commit does not " +
				"contain, so the reason is would-discard and the refusal names that commit rather than " +
				"reporting a bare move.",
			Sentinel: "safety.ErrRefused",
			Value:    "safety.ReasonWouldDiscard",
			MessageContains: []string{
				"safety: refused to update refs/heads/" + BranchUnderValidation,
				"(would-discard)",
				"holds commits that",
				"does not contain",
				"would discard ",
			},
			NamesAction: "The refusal names the commits that would be dropped, in Refusal.Discarded and in " +
				"the message, which is what an operator needs to decide whether to incorporate them.",
			ActionSucceeds: "Fetching and rebasing onto the advanced branch produces a proposed commit that " +
				"contains the colleague's commit, and the same decision then allows a fast-forward.",
		},
		Deferred: &Deferred{
			AppliedBy: "AdvanceRemoteOutOfBand",
			AppliedWhen: "After the run has observed the branch on the remote and before it submits its " +
				"update. Which point in the run that is belongs to the harness.",
			WhyNotAtBuild: "A remote that is already ahead when the run starts is a remote the run observes " +
				"ahead, and an anchor taken against it describes the target correctly. The condition is the " +
				"target moving after the observation, so planting it earlier would exercise nothing.",
		},
	}}, nil
}

// buildEmptyAfterRebase builds a branch whose change is already on the default
// branch. The two histories are planted; the rebase that empties the branch is
// a real rebase the run performs, because an empty commit planted directly is
// a state a rebase does not produce.
func buildEmptyAfterRebase(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioEmptyAfterRebase,
		"A branch with nothing left to validate once it is rebased.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s, "go test ./..."); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggy); err != nil {
		return nil, nil, err
	}
	branchCommit, err := b.git.commitAll(s.WorkingCopy, "narrow the Total loop bound")
	if err != nil {
		return nil, nil, err
	}
	s.Commits["branch-change"] = branchCommit
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}

	// The same edit lands on the default branch under a different author's
	// message, which is what somebody else shipping the change first looks
	// like.
	if _, err := b.git.run(s.WorkingCopy, "checkout", "--quiet", DefaultBranch); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggy); err != nil {
		return nil, nil, err
	}
	defaultCommit, err := b.git.commitAll(s.WorkingCopy, "stop Total before the last element")
	if err != nil {
		return nil, nil, err
	}
	s.Commits["default-branch-same-change"] = defaultCommit
	if _, err := b.git.run(s.WorkingCopy, "push", "--quiet", "origin", DefaultBranch); err != nil {
		return nil, nil, err
	}
	if _, err := b.git.run(s.WorkingCopy, "checkout", "--quiet", s.Branch); err != nil {
		return nil, nil, err
	}

	return s, []Condition{{
		ID:       "stage-no-diff-after-rebase",
		Scenario: s.Name,
		Kind:     KindStage,
		Planted: "The branch and the default branch carry the same edit to total.go as separate commits with " +
			"different messages. The two commits differ, so the branch is not already contained in the " +
			"default branch and the condition is not visible before the rebase runs.",
		Mechanism: "the rebase stage, which sets pipeline.KeyDiffEmpty, and the stage nodes that read it",
		Expect: Outcome{
			Summary: "The rebase succeeds and drops the branch's commit as already applied. The rebase stage " +
				"sets diff.empty, and the run then completes successfully with review, test, document, " +
				"lint, push, pr, and ci all skipped rather than failed: no body runs and no finding is " +
				"reported against a change that no longer exists.",
			Value: "pipeline.OutcomeSkipped for every stage after rebase, over a run that completed",
		},
	}}, nil
}

// buildUnparseableTrustedConfig puts a document that is not well-formed JSON
// at the configuration path on the default branch.
func buildUnparseableTrustedConfig(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioUnparseableTrustedConfig,
		"A trusted configuration document that cannot be parsed, so nothing may be launched.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s, "go test ./..."); err != nil {
		return nil, nil, err
	}
	// A trailing comma: valid to a reader, refused by a JSON decoder, and the
	// mistake somebody actually makes.
	malformed := `{
  "commands": {
    "test": "go test ./...",
    "lint": "go vet ./...",
  },
  "agent": "fixture-trusted-agent"
}
`
	if err := writeFile(s.WorkingCopy, ConfigPath, 0o644, malformed); err != nil {
		return nil, nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "leave a trailing comma in the configuration document")
	if err != nil {
		return nil, nil, err
	}
	s.Commits["default-branch-malformed-config"] = commit
	if _, err := b.git.run(s.WorkingCopy, "push", "--quiet", "origin", DefaultBranch); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "docs/behavior.md", 0o644,
		subjectDocsBehavior+"\nThis paragraph is the change under validation.\n"); err != nil {
		return nil, nil, err
	}
	if _, err := b.git.commitAll(s.WorkingCopy, "add a paragraph"); err != nil {
		return nil, nil, err
	}
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}

	return s, []Condition{{
		ID:        "refusal-unparseable-trusted-config",
		Scenario:  s.Name,
		Kind:      KindRefusal,
		Principle: "P7",
		Planted: "The configuration document on the default branch has a trailing comma, so no key in it can " +
			"be named. The branch under validation carries an ordinary change, so a run that got past this " +
			"would have work to do and the difference is observable.",
		Mechanism: "config.Parse over the trusted layer",
		Expect: Outcome{
			Summary: "Parsing returns a *config.DocumentError and nothing in the document is applied. The run " +
				"aborts before launching anything: no agent process starts and no configured command runs. " +
				"Falling back to defaults is the wrong answer, because the defaults are not what this " +
				"repository asked for and nothing establishes that they are safe here.",
			Sentinel:        "config.ErrMalformed",
			MessageContains: []string{"config: "},
			NamesAction: "The refusal names the document that could not be parsed and the decoding failure " +
				"underneath it, so the operator can find the line.",
			ActionSucceeds: "Fixing the document on the default branch and running again: the trusted copy is " +
				"read from the default branch on every run, so no state carries the refusal forward.",
		},
	}}, nil
}

// buildUnreadableTrustedConfig makes the configuration path on the default
// branch a directory. The document is then not merely refused, it cannot be
// read at all, which is the other half of what PRD section 10 aborts on.
func buildUnreadableTrustedConfig(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioUnreadableTrustedConfig,
		"A trusted configuration path that holds no document to read.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s, "go test ./..."); err != nil {
		return nil, nil, err
	}
	if err := os.Remove(filepath.Join(s.WorkingCopy, ConfigPath)); err != nil {
		return nil, nil, fmt.Errorf("fixture: removing the configuration document before replacing it "+
			"with a directory: %w", err)
	}
	if err := writeFile(s.WorkingCopy, ConfigPath+"/settings.json", 0o644, subjectConfig("go test ./...")); err != nil {
		return nil, nil, err
	}
	commit, err := b.git.commitAll(s.WorkingCopy, "make the configuration path a directory")
	if err != nil {
		return nil, nil, err
	}
	s.Commits["default-branch-config-is-a-directory"] = commit
	if _, err := b.git.run(s.WorkingCopy, "push", "--quiet", "origin", DefaultBranch); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "docs/behavior.md", 0o644,
		subjectDocsBehavior+"\nThis paragraph is the change under validation.\n"); err != nil {
		return nil, nil, err
	}
	if _, err := b.git.commitAll(s.WorkingCopy, "add a paragraph"); err != nil {
		return nil, nil, err
	}
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}

	return s, []Condition{{
		ID:        "refusal-unreadable-trusted-config",
		Scenario:  s.Name,
		Kind:      KindRefusal,
		Principle: "P7",
		Planted: "The configuration path on the default branch is a directory holding a document, not a " +
			"document. It resolves to an object and then fails to read as one, which is a different failure " +
			"from a path that is absent and a different failure from a document that will not parse.",
		Mechanism: "vcs.Repository.FileAt against the trusted commit",
		Expect: Outcome{
			Summary: "The read fails with a *vcs.CommandError carrying git's own message, not with " +
				"vcs.ErrPathNotFound: the path exists and is not a file whose bytes can be read. The run " +
				"aborts before launching anything rather than treating an unreadable trusted document as an " +
				"absent one and falling back to defaults.",
			MessageContains: []string{"vcs: file-at failed in", "git exited 128", "bad file"},
			NamesAction: "The refusal carries git's message and the operation, so the operator is told which " +
				"read failed rather than being told the repository is misconfigured.",
			ActionSucceeds: "Replacing the directory with a document on the default branch and running again.",
		},
	}}, nil
}

// buildHostileTemplate plants the git templates a gate must not be born from,
// and the configuration file that selects one. The environment variable half
// is planted as a value rather than exported here, because which process gets
// it is the harness's.
func buildHostileTemplate(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioHostileTemplate,
		"The gate's trust anchor: a template that chooses what a gate repository is born with.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s, "go test ./..."); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggy); err != nil {
		return nil, nil, err
	}
	if _, err := b.git.commitAll(s.WorkingCopy, "narrow the Total loop bound"); err != nil {
		return nil, nil, err
	}
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}

	// Two templates, because the gate's own documentation says the refusal
	// fires for every hook name on a creation and for every name except the
	// admission hook on a repair. One template exercises the refusal, the
	// other exercises the case that was closed rather than caught.
	templates := []struct {
		dir   string
		hooks map[string]string
	}{
		{"template-mixed", map[string]string{
			"pre-receive": "template-hook-pre-receive",
			"post-update": "template-hook-post-update",
			"update":      "template-hook-update",
		}},
		{"template-pre-receive-only", map[string]string{
			"pre-receive": "template-hook-pre-receive-only",
		}},
	}
	var quiet []string
	for _, t := range templates {
		dir := filepath.Join(s.Root, t.dir)
		for name, id := range t.hooks {
			if err := writeFile(dir, "hooks/"+name, 0o755,
				tripwireScript(id, s.Tripwire, "A hook a git template would put in a repository at birth.")); err != nil {
				return nil, nil, err
			}
			quiet = append(quiet, id)
		}
		config := filepath.Join(s.Root, "gitconfig-"+t.dir)
		content := "[init]\n\ttemplateDir = " + dir + "\n"
		if err := os.WriteFile(config, []byte(content), 0o600); err != nil {
			return nil, nil, fmt.Errorf("fixture: writing %s: %w", config, err)
		}
		s.Paths[t.dir] = dir
		s.Paths["gitconfig-"+t.dir] = config
	}

	return s, []Condition{
		{
			ID:        "refusal-template-hooks-at-birth",
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Principle: "P7",
			Planted: "A git template carrying pre-receive, post-update, and update, and a git configuration " +
				"file setting init.templateDir to it. internal/vcs deliberately keeps GIT_CONFIG_GLOBAL " +
				"and GIT_CONFIG_SYSTEM, so a configuration file reached that way still chooses what a gate " +
				"repository is born with. The template is selected through configuration, which is the " +
				"channel that is open, rather than through the environment variable that is closed.",
			Mechanism: "gate.Initialize against a home with no gate yet, with " +
				"GIT_CONFIG_GLOBAL naming gitconfig-template-mixed",
			Expect: Outcome{
				Summary: "Initialization refuses. The hooks that arrived are taken back out, and the gate is " +
					"left refusing every push rather than accepting every push with nothing checking them.",
				Sentinel: "gate.ErrTemplateHooks",
				MessageContains: []string{
					"gate: repository was created carrying hooks from a git template",
					"while being initialized",
					"init.templateDir",
				},
				NamesAction: "take the hook out of that template, or point init.templateDir elsewhere, and " +
					"initialize again",
				ActionSucceeds: "The gate refuses every push until an initialization succeeds, and an " +
					"initialization with no template hook arriving does succeed, so the named action ends " +
					"the state the reader is in rather than leading to a second refusal.",
				TripwiresQuiet: quiet,
			},
		},
		{
			ID:        "refusal-template-hooks-on-repair",
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Principle: "P7",
			Planted: "The same template, applied to a second initialization over a gate that already exists. " +
				"The channel is open on every initialization, not only on the first, and the refusal is " +
				"written against the operation rather than against creation.",
			Mechanism: "gate.Initialize over an existing gate, with GIT_CONFIG_GLOBAL naming " +
				"gitconfig-template-mixed",
			Expect: Outcome{
				Summary: "The repair refuses with the same sentinel, for post-update and update. The " +
					"pre-receive name is not what fires it: obtaining a gate seals it first, so that name " +
					"is already occupied by a hook the gate wrote.",
				Sentinel:        "gate.ErrTemplateHooks",
				MessageContains: []string{"post-update", "init.templateDir"},
				TripwiresQuiet:  quiet,
			},
		},
		{
			ID:        "closed-template-pre-receive-on-repair",
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Principle: "P7",
			Planted: "A template carrying only pre-receive, applied to a repair. This is the one name the " +
				"template channel can no longer reach, and it is planted so the closure is checked rather " +
				"than assumed.",
			Mechanism: "gate.Initialize over an existing gate, with GIT_CONFIG_GLOBAL naming " +
				"gitconfig-template-pre-receive-only",
			Expect: Outcome{
				Summary: "No refusal. The repair succeeds, the hook that runs on a push to the repaired gate " +
					"is the gate's own admission hook, and the template's pre-receive is neither installed " +
					"nor preserved into the chain at the .local name. A harness that saw ErrTemplateHooks " +
					"here has found a change in behavior, not a pass.",
				TripwiresQuiet: []string{"template-hook-pre-receive-only"},
			},
		},
		{
			ID:        "closed-git-template-dir-environment",
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Principle: "P7",
			Planted: "The same template directory, offered through GIT_TEMPLATE_DIR in the environment of " +
				"the process that initializes the gate. Its path is in the scenario's paths; exporting it " +
				"into a process is the harness's.",
			Mechanism: "internal/vcs, which removes GIT_TEMPLATE_DIR from the environment of every " +
				"invocation it makes",
			Expect: Outcome{
				Summary: "Initialization succeeds and no template hook arrives: the variable is stripped " +
					"before git is invoked, so this channel is closed rather than caught. This is the " +
					"negative case that gives the refusal above its meaning, and a harness that saw " +
					"ErrTemplateHooks here would be seeing the variable survive.",
				TripwiresQuiet: quiet,
			},
		},
	}, nil
}

// buildCopiedWorkingCopy builds a working copy that will be gated and then
// copied. The copy cannot be taken here: what makes it the condition is that
// its assistant remote was inherited from a gated original, and there is no
// gate until the product makes one. So the copy is deferred to
// CopyGatedWorkingCopy.
func buildCopiedWorkingCopy(b *builder) (*Scenario, []Condition, error) {
	s, err := b.newScenario(ScenarioCopiedWorkingCopy,
		"A project directory copied after it was gated, so the copy inherits the original's remote.")
	if err != nil {
		return nil, nil, err
	}
	if err := b.initSubject(s, "go test ./..."); err != nil {
		return nil, nil, err
	}
	if err := b.startBranch(s); err != nil {
		return nil, nil, err
	}
	if err := writeFile(s.WorkingCopy, "total.go", 0o644, subjectTotalGoBuggy); err != nil {
		return nil, nil, err
	}
	if _, err := b.git.commitAll(s.WorkingCopy, "narrow the Total loop bound"); err != nil {
		return nil, nil, err
	}
	if err := b.pushBranch(s); err != nil {
		return nil, nil, err
	}
	s.Paths["copy-destination"] = filepath.Join(s.Root, "work-copy")

	shared := "The original working copy, ready to be gated. The copy is taken by CopyGatedWorkingCopy " +
		"once a gate exists, so the copy's assistant remote is inherited byte for byte rather than written " +
		"into a fresh directory. Writing the remote directly would produce a state the product's own path " +
		"never reaches, and would leave the original absent, which is the fact the ownership question turns " +
		"on."
	deferred := &Deferred{
		AppliedBy: "CopyGatedWorkingCopy",
		AppliedWhen: "After gate.Initialize has succeeded against the original working copy and before " +
			"anything is asked of the copy.",
		WhyNotAtBuild: "There is no gate to inherit a remote from until the product makes one, and a remote " +
			"pointing at a gate that does not exist is a different state.",
	}

	return s, []Condition{
		{
			ID:       "refusal-copied-working-copy-removal",
			Scenario: s.Name,
			Kind:     KindRefusal,
			Planted:  shared,
			Mechanism: "gate.Remove against the copy, with the original still standing and still pointing at " +
				"the gate",
			Expect: Outcome{
				Summary: "Removal refuses. Nothing is deleted: not the gate repository, not the original's " +
					"binding, and not the copy's remote. The gate the copy inherited is the original's, and " +
					"the remote the copy carries is not evidence that it owns it.",
				Sentinel: "gate.ErrGateClaimed",
				MessageContains: []string{
					"gate: another working copy is bound to this gate",
					"nothing was removed",
					"inherits its assistant remote",
					"only a removal run there can act on it",
				},
				NamesAction: "removing the assistant remote here detaches it and always succeeds",
				ActionSucceeds: "Detaching takes nothing away from anybody, so no guard refuses it; after it " +
					"the copy has no gate to remove and an initialization gives it one of its own.",
			},
			Deferred: deferred,
		},
		{
			ID:        "adoption-copied-working-copy-initialize",
			Scenario:  s.Name,
			Kind:      KindRefusal,
			Planted:   shared,
			Mechanism: "gate.Initialize against the copy, with the original still standing",
			Expect: Outcome{
				Summary: "No refusal, and no sharing either. The copy holds the original's gate only through " +
					"an inherited remote, and there is a gate at the copy's own path hash to hand out, so " +
					"the copy is given its own gate and the original keeps everything recorded against " +
					"its own. A harness that saw the copy adopt the original's gate has found the failure " +
					"this condition exists for; one that saw ErrGateClaimed has found the refusal that " +
					"would have been the wrong answer, because refusing here would leave the copy with " +
					"nothing when there was a gate available.",
				MessageContains: nil,
			},
			Deferred: deferred,
		},
	}, nil
}
