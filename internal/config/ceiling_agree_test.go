package config_test

import (
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/machine"
)

// The usage ceiling's window is the usage view's window. internal/config does
// not import internal/machine, so the two readers are copies, and this holds
// them to one result on the shapes both claim to read: empty, a number of
// days, a duration, whitespace around one, and the refusals for a span that
// is not positive, not a window, or too long to measure. A duration string
// outside this set is not compared.
func TestUsageCeilingWindowAgreesWithTheUsageView(t *testing.T) {
	if config.DefaultUsageCeilingWindow != machine.DefaultUsageWindow {
		t.Fatalf("DefaultUsageCeilingWindow = %q, want the usage view's %q",
			config.DefaultUsageCeilingWindow, machine.DefaultUsageWindow)
	}
	samples := []string{
		"", "7d", "24h", "90m", "1h30m", "1.5h", " 24h ",
		"0s", "0d", "-1h", "soon", "1", "24D", "100000000000d",
	}
	for _, sample := range samples {
		doc := fmt.Sprintf("{\"usage_ceiling\": {\"window\": %q}}", sample)
		layer, err := config.Parse(config.OriginGlobal, []byte(doc))
		want, wantErr := machine.ParseUsageWindow(sample)
		if (err == nil) != (wantErr == nil) {
			t.Errorf("%q: config accepted %v, usage view accepted %v", sample, err == nil, wantErr == nil)
			continue
		}
		if err != nil {
			continue
		}
		res, err := config.Resolve(layer, config.Absent(config.OriginTrusted))
		if err != nil {
			t.Fatalf("%q: Resolve: %v", sample, err)
		}
		got := res.Config.UsageCeiling.Window
		again, againErr := machine.ParseUsageWindow(got)
		if againErr != nil || again.Span() != want.Span() || again.String() != want.String() || got != want.String() {
			t.Errorf("%q: config kept %q (%v, %v), usage view kept %q spanning %v",
				sample, got, again.Span(), againErr, want.String(), want.Span())
		}
	}
}
