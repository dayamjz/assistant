package judge

// WithMaxPiece sets the size a file's changed lines are first cut at, so a
// test can make a diff span several requests without building one of the
// shipped size. It is test-only: the shipped bound is not configurable.
func WithMaxPiece(n int) Option { return func(c *Client) { c.maxPiece = n } }
