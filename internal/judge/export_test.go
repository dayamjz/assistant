package judge

// WithMaxPiece sets the size a file's changed lines are first cut at, so a
// test can make a diff span several requests without building one of the
// shipped size. It is test-only: the shipped bound is not configurable.
func WithMaxPiece(n int) Option { return func(c *Client) { c.maxPiece = n } }

// RecordWithRates is RecordOf pricing from table instead of the shipped rates,
// so a test can price a call while the shipped table is empty.
func RecordWithRates(run, stage, batch string, r Result, finding string, table map[string]Rate) Record {
	return recordAt(run, stage, batch, r, finding, table)
}

// ShippedRates is the shipped rate table.
func ShippedRates() map[string]Rate { return rates }
