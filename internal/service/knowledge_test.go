package service_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// quinceneraSource is the checkout one test copies. The test never writes
// there, and it skips when this machine has no such checkout.
const quinceneraSource = "/Users/joelzambrano/Projects/Quincenera"

func TestQuinceneraKnowledgeStaysBehindTheOperatorDocument(t *testing.T) {
	principles.Cite(t, principles.P7)
	info, err := os.Stat(quinceneraSource)
	if err != nil || !info.IsDir() {
		t.Skipf("%s is not a directory on this machine", quinceneraSource)
	}
	sourceFile := filepath.Join(quinceneraSource, "assistant.json")
	before, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(sourceFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "allow_standing_consent") || !strings.Contains(string(before), "python3 -I -c") {
		t.Fatalf("the Quincenera assistant.json no longer carries the strings this test tells apart from operator policy:\n%s", before)
	}

	project := t.TempDir()
	if err := copyTree(quinceneraSource, project); err != nil {
		t.Fatal(err)
	}
	h := newHome(t)
	const operatorPolicy = "{\"supervision_beacon_interval\": \"1s\"}\n"
	if err := os.WriteFile(h.ConfigFile(), []byte(operatorPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	records := openRecords(t, h)
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: id, Shape: store.TaskInvestigation, Project: project, Mode: "interactive",
		WorktreePath: filepath.Join(t.TempDir(), "not-a-copy"),
	}, store.Disposition{}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: "venue-deposit", TaskID: store.Known(id), Subject: "decide the venue deposit",
	}); err != nil {
		t.Fatal(err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	const secret = "s3cret-token"
	const assertion = "agent-assertion: the venue allows a later curfew"
	rule := "operator-rule: keep the deposit note. see https://user:" + secret + "@example.test/repo.git"
	var adopted int64
	withService(t, h, func(running serviceUnderTest) {
		asserted, err := running.service.RecordTaskAssertion(t.Context(), project, id, assertion)
		if err != nil {
			t.Fatal(err)
		}
		if asserted.Author != store.AuthoredByTask() || asserted.Author.String() == store.ResolvedByPerson().String() {
			t.Fatalf("the assertion author is %s", asserted.Author)
		}
		written, err := running.service.RecordOperatorKnowledge(t.Context(), project, rule, 0)
		if err != nil {
			t.Fatal(err)
		}
		if written.Author != store.AuthoredByOperator() || written.Author.String() == store.ResolvedByPerson().String() {
			t.Fatalf("the operator author is %s, which claims a person", written.Author)
		}
		if strings.Contains(written.Body, secret) || !strings.Contains(written.Body, redact.Marker) {
			t.Fatalf("the stored rule is %q, want the credential removed", written.Body)
		}
		prompt, err := running.service.WorkerPrompt(t.Context(), project, project)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(prompt.Text, operatorPolicy) {
			t.Fatalf("the prompt does not start with the operator document:\n%s", prompt.Text)
		}
		if strings.Contains(prompt.Text, "allow_standing_consent") || strings.Contains(prompt.Text, "python3 -I -c") || strings.Contains(prompt.Text, assertion) || strings.Contains(prompt.Text, secret) {
			t.Fatalf("the repository file, the assertion, or the credential entered the prompt:\n%s", prompt.Text)
		}
		if !strings.Contains(prompt.Text, "operator-rule") || !strings.Contains(prompt.Text, "hold venue-deposit") || !strings.Contains(prompt.Text, "decide the venue deposit") || !strings.Contains(prompt.Text, id) {
			t.Fatalf("the operator rule or a pinned reference is missing:\n%s", prompt.Text)
		}
		if strings.Index(prompt.Text, "operator-rule") < len(operatorPolicy) {
			t.Fatalf("generated text is not after the operator document:\n%s", prompt.Text)
		}
		again, err := running.service.RecordOperatorKnowledge(t.Context(), project, "that assertion holds", asserted.ID)
		if err != nil {
			t.Fatal(err)
		}
		adopted = again.ID
		prompt, err = running.service.WorkerPrompt(t.Context(), project, project)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(prompt.Text, operatorPolicy) || !strings.Contains(prompt.Text, assertion) || strings.Contains(prompt.Text, "python3 -I -c") {
			t.Fatalf("adopting the assertion did not keep the operator document ahead of it:\n%s", prompt.Text)
		}
	})

	withService(t, h, func(running serviceUnderTest) {
		got, err := running.service.ProjectKnowledge(t.Context(), project)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0].Body != assertion || got[2].ID != adopted {
			t.Fatalf("after restart the history is %+v, want the assertion, the rule, and the adoption", got)
		}
		if strings.Contains(got[1].Body, secret) {
			t.Fatalf("the restarted rule still carries the credential: %s", got[1].Body)
		}
	})

	after, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(sourceFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) || afterInfo.Size() != beforeInfo.Size() {
		t.Fatal("the Quincenera checkout was modified")
	}
}

func TestKnowledgeReachesTheWorker(t *testing.T) {
	h := newHome(t)
	const operatorPolicy = "{\"supervision_beacon_interval\": \"1s\", \"relaunch_attempts\": 0}\n"
	if err := os.WriteFile(h.ConfigFile(), []byte(operatorPolicy), 0o600); err != nil {
		t.Fatal(err)
	}
	const project = "quince-scoped"
	const assertion = "agent-assertion: the venue allows a later curfew"
	const rule = "operator-rule: keep the venue deposit"
	withService(t, h, func(running serviceUnderTest) {
		id, err := home.NewTaskID()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := running.service.RecordTaskAssertion(t.Context(), project, id, assertion); err != nil {
			t.Fatal(err)
		}
		if _, err := running.service.RecordOperatorKnowledge(t.Context(), project, rule, 0); err != nil {
			t.Fatal(err)
		}
	})

	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTaskWith(t, h, repository, store.TaskDelivery, "task/one",
		store.Unknown[config.Delivery](), project, "Do the work the coordinator asked for.\n")
	o, agent := workerOptions(t, h)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		waitCapture(t, provider, launched, "the second check was read")
		text := strings.Join(workerCall(t, agent).Lines, "\n")
		if !strings.Contains(text, "Do the work the coordinator asked for.") || !strings.Contains(text, rule) || !strings.Contains(text, "supervision_beacon_interval") {
			t.Fatalf("the worker was not given the instructions, the operator document, and the rule:\n%s", text)
		}
		if strings.Contains(text, assertion) {
			t.Fatalf("the worker was given the unadopted assertion:\n%s", text)
		}
		if strings.Index(text, "supervision_beacon_interval") > strings.Index(text, rule) {
			t.Fatalf("the rule precedes the operator document in what the worker read:\n%s", text)
		}
	})
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if !strings.HasPrefix(target, dst+string(os.PathSeparator)) && target != dst {
			return errors.New("copy escaped the destination")
		}
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	})
}
