package buildinfo

import "testing"

func TestWellFormedReleaseTagAcceptsReleaseVersions(t *testing.T) {
	t.Parallel()
	for _, version := range []string{
		"v0.1.0",
		"v1.2.3",
		"v10.20.30",
		"v1.0.0-alpha",
		"v1.0.0-alpha.1",
		"v1.0.0-rc.1",
		"v0.0.0-dryrun",
		"v0.0.0-dryrun.42",
	} {
		if !WellFormedReleaseTag(version) {
			t.Fatalf("%q is a release version", version)
		}
	}
}

func TestWellFormedReleaseTagRefusesMalformedPrereleasesAndCore(t *testing.T) {
	t.Parallel()
	for _, version := range []string{
		"",
		"1.2.3",
		"v01.2.3",
		"v1.02.3",
		"v1.2.03",
		"v1.2",
		"v1.2.3.4",
		"v1.2.3-",
		"v1.2.3-01",
		"v0.1.0-01",
		"v0.1.0-alpha..1",
		"v0.1.0-.1",
		"v0.1.0-alpha.",
		"v0.1.0-+bad",
	} {
		if WellFormedReleaseTag(version) {
			t.Fatalf("%q is not a release version", version)
		}
	}
}
