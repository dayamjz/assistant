package judge

import "github.com/dayamjz/assistant/internal/store"

// Rate is a model's price in US dollars per million input and output tokens,
// with Source saying where the figures come from. It is the store's
// DecisionRate, and whether a call is priced at one is store.PricedAt's to
// say, so the rule has one owner.
type Rate = store.DecisionRate

// rates is the one place a model's price is set, keyed by model version. It is
// empty, as no doc in this tree states a price for ModelVersion, so no call is
// priced. TestEveryShippedRateNamesItsSource refuses an entry that is no Rate.
var rates = map[string]Rate{}
