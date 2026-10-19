package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
)

const knowledgeTask = "abcdefghijkl"

func TestKnowledgeAppendsAndDoesNotRevise(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	first, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "first rule",
	})
	if err != nil {
		t.Fatalf("appending the first row: %v", err)
	}
	second, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "second rule",
	})
	if err != nil {
		t.Fatalf("appending the second row: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("the second append reused id %d", first.ID)
	}
	got, err := s.ProjectKnowledge(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Body != "first rule" || got[1].Body != "second rule" || got[0].ID != first.ID {
		t.Fatalf("the history is %+v, want both rows with the first body unchanged", got)
	}
	for _, verb := range []string{
		`UPDATE knowledge SET body = 'rewritten' WHERE id = ?`,
		`DELETE FROM knowledge WHERE id = ?`,
	} {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, verb, first.ID)
			return err
		})
		if err == nil {
			t.Fatalf("%q was accepted", verb)
		}
	}
	got, err = s.ProjectKnowledge(ctx, "proj")
	if err != nil || len(got) != 2 || got[0].Body != "first rule" {
		t.Fatalf("after the refused writes the history is %+v, %v", got, err)
	}
}

func TestKnowledgeRefusesAnAssertionThatAdopts(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	assertion, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByTask(), SourceTask: knowledgeTask, Body: "the venue allows a later curfew",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByTask(), SourceTask: knowledgeTask, Body: "make it a rule", Adopts: assertion.ID,
	}); !errors.Is(err, ErrKnowledgeAdopts) {
		t.Fatalf("a task assertion adopting was %v, want %v", err, ErrKnowledgeAdopts)
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "named a task", SourceTask: knowledgeTask,
	}); err == nil {
		t.Fatal("an operator row naming a source task was accepted")
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "adopts nothing real", Adopts: assertion.ID + 100,
	}); !errors.Is(err, ErrKnowledgeAdopts) {
		t.Fatalf("adopting a missing row was %v, want %v", err, ErrKnowledgeAdopts)
	}
	other, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "other", Author: AuthoredByTask(), SourceTask: knowledgeTask, Body: "elsewhere",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "cross project", Adopts: other.ID,
	}); !errors.Is(err, ErrKnowledgeAdopts) {
		t.Fatalf("adopting another project's assertion was %v, want %v", err, ErrKnowledgeAdopts)
	}
	adopted, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "this assertion holds", Adopts: assertion.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, ok := adopted.Adopts.Get()
	if !ok || id != assertion.ID || adopted.Author != AuthoredByOperator() {
		t.Fatalf("the operator row is %+v, want an adoption of %d", adopted, assertion.ID)
	}
	if adopted.Author.String() == ResolvedByPerson().String() {
		t.Fatal("the operator author is the person resolver")
	}
}

func TestKnowledgeAuthorsAreTaskAndOperator(t *testing.T) {
	got := knowledgeAuthors()
	if len(got) != 2 || got[0] != AuthoredByTask() || got[1] != AuthoredByOperator() {
		t.Fatalf("authors are %+v, want task and operator", got)
	}
	for _, author := range got {
		if author.String() == "person" || author.String() == ResolvedByPerson().String() {
			t.Fatalf("author %s witnesses a person", author)
		}
	}
	if _, err := knowledgeAuthorText(KnowledgeAuthor{}); !errors.Is(err, ErrNoKnowledgeAuthor) {
		t.Fatalf("the zero author was %v", err)
	}
}

func TestKnowledgeRedactsBeforeItStores(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const secret = "s3cret-token"
	row, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(),
		Body: "see https://operator:" + secret + "@example.test/repo.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.Body, secret) {
		t.Fatalf("the stored body still carries the credential: %s", row.Body)
	}
	got, err := s.ProjectKnowledge(ctx, "proj")
	if err != nil || len(got) != 1 || strings.Contains(got[0].Body, secret) {
		t.Fatalf("reading it back is %+v, %v", got, err)
	}
}

func TestKnowledgeRefusesAnEmptyOrOversizedBody(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	for _, body := range []string{"", "   ", "\n"} {
		if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
			Project: "proj", Author: AuthoredByOperator(), Body: body,
		}); !errors.Is(err, ErrKnowledgeEmpty) {
			t.Fatalf("body %q was %v, want %v", body, err, ErrKnowledgeEmpty)
		}
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: strings.Repeat("a", MaxKnowledgeBody+1),
	}); !errors.Is(err, ErrKnowledgeBound) {
		t.Fatalf("an oversized body was %v, want %v", err, ErrKnowledgeBound)
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "", Author: AuthoredByOperator(), Body: "x",
	}); err == nil {
		t.Fatal("an empty project was accepted")
	}
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByTask(), SourceTask: "nope", Body: "x",
	}); !errors.Is(err, home.ErrTaskID) {
		t.Fatalf("a bad task identifier was %v, want %v", err, home.ErrTaskID)
	}
	got, err := s.ProjectKnowledge(ctx, "proj")
	if err != nil || len(got) != 0 {
		t.Fatalf("refused writes left %+v, %v", got, err)
	}
}

func TestKnowledgeSurvivesReopenAndRejectsAForeignAuthor(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	s := openStoreAt(t, path)
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "kept",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path, WithRedactor(workingRedactor()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.ProjectKnowledge(ctx, "proj")
	if err != nil || len(got) != 1 || got[0].Body != "kept" || got[0].Author != AuthoredByOperator() {
		t.Fatalf("reopened history is %+v, %v", got, err)
	}
	if _, err := reopened.write.ExecContext(ctx,
		`INSERT INTO knowledge (project, author, body, recorded_at) VALUES ('proj', 'person', 'forged', '2020-01-01T00:00:00.000000000Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.ProjectKnowledge(ctx, "proj"); !errors.Is(err, ErrUnknownKnowledgeAuthor) {
		t.Fatalf("a person author read back as %v, want %v", err, ErrUnknownKnowledgeAuthor)
	}
	reader, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.AppendKnowledge(ctx, KnowledgeInput{
		Project: "proj", Author: AuthoredByOperator(), Body: "from a reader",
	}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only append was %v, want %v", err, ErrReadOnly)
	}
}

func TestKnowledgeAppendsUnderConcurrentWriters(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.AppendKnowledge(ctx, KnowledgeInput{
				Project: "proj", Author: AuthoredByOperator(), Body: fmt.Sprintf("row %d", i),
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ProjectKnowledge(ctx, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("concurrent appends left %d rows, want %d", len(got), n)
	}
	seen := map[int64]bool{}
	for _, row := range got {
		if seen[row.ID] {
			t.Fatalf("id %d was recorded twice", row.ID)
		}
		seen[row.ID] = true
	}
}

func TestKnowledgeProjectsDoNotShareRows(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.AppendKnowledge(ctx, KnowledgeInput{
		Project: "one", Author: AuthoredByOperator(), Body: "only one",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ProjectKnowledge(ctx, "two")
	if err != nil || len(got) != 0 {
		t.Fatalf("the other project is %+v, %v", got, err)
	}
}
