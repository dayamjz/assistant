package judge

import (
	"errors"
	"math"
	"strings"
)

// Rate is a model's price in US dollars per million input and output tokens,
// with Source saying where the figures come from. Without a source, or with a
// figure that is not finite and at or above zero, it is not a rate.
type Rate struct {
	InputUSDPerMillionTokens, OutputUSDPerMillionTokens float64
	Source                                              string
}

// rates is the one place a model's price is set, keyed by model version. It is
// empty, as no doc in this tree states a price for ModelVersion, so no call is
// priced. TestEveryShippedRateNamesItsSource refuses an entry that is no Rate.
var rates = map[string]Rate{}

// valid says why r is not a rate, or nil when it is one.
func (r Rate) valid() error {
	in, out := r.InputUSDPerMillionTokens, r.OutputUSDPerMillionTokens
	if strings.TrimSpace(r.Source) == "" || !(in >= 0 && out >= 0) || math.IsInf(in+out, 1) {
		return errors.New("judge: a rate needs a source and two finite figures at or above zero")
	}
	return nil
}
