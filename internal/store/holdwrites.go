package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// HoldWritesForTest begins a write transaction on the store's one writer
// connection and holds it, writing nothing, until the returned function is
// called, so every write through this store waits behind it the way it
// waits behind a long write: inTx's BeginTx queues for the connection until
// the hold ends or its own context is done. It is a contention injector and
// nothing else: no production path holds one, and a caller outside this
// package has no other way to make a real write wait, because this is the
// only package that opens the database.
//
// The hold is taken under a context that ctx's cancellation does not end,
// because database/sql rolls a transaction back when its context is done and
// the hold would then end on its own; only the returned function ends it,
// and calling it again does nothing. A test calls it before it finishes: a
// hold that outlives its test holds the connection until the process ends.
// It takes a testing.TB and refuses a nil one, so a production path does
// not reach it by accident; that does not stop a deliberate caller that
// builds a testing.T of its own.
func (s *Store) HoldWritesForTest(ctx context.Context, tb testing.TB) (func() error, error) {
	if tb == nil {
		return nil, fmt.Errorf("store: HoldWritesForTest is a test's contention injector and takes the test")
	}
	tb.Helper()
	if err := s.live(); err != nil {
		return nil, err
	}
	if s.write == nil {
		return nil, ErrReadOnly
	}
	tx, err := s.write.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return nil, fmt.Errorf("store: holding the writer: %w", err)
	}
	var once sync.Once
	var released error
	return func() error {
		once.Do(func() {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				released = fmt.Errorf("store: releasing the held writer: %w", err)
			}
		})
		return released
	}, nil
}
