package config

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// remoteHost is one valid remote_hosts entry, which the tests below edit one
// field at a time so that each refusal is about the field it names.
const remoteHost = `{"host": "studio", "root": "/Users/me/assistant-remote", "bridge": "/Users/me/bin/assistant"}`

// refusesKey parses doc as origin and requires a *KeyError naming key, whose
// message names the key and contains every one of want.
func refusesKey(t *testing.T, origin Origin, doc string, key Key, want ...string) {
	t.Helper()
	_, err := Parse(origin, []byte(doc))
	var ke *KeyError
	if !errors.As(err, &ke) {
		t.Fatalf("Parse(%v, %s) = %v, want a *KeyError naming %s", origin, doc, err, key)
	}
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("Parse(%v, %s) = %v, which does not wrap ErrInvalid", origin, doc, err)
	}
	if ke.Key != key {
		t.Fatalf("Parse(%v, %s) named %q, want %q: %v", origin, doc, ke.Key, key, err)
	}
	if !strings.Contains(err.Error(), string(key)) {
		t.Errorf("the refusal %q does not name the key", err)
	}
	// An unrecognized key also names the key, so without this a schema that
	// lacked the key would pass every case here by refusing all of them.
	if strings.Contains(err.Error(), "unrecognized key") {
		t.Errorf("the refusal %q is of an unknown key, not of the value", err)
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal %q does not say %q", err, w)
		}
	}
}

// A repository file, trusted or pushed, naming remote_hosts,
// remote_disconnect_grace or placement.local is refused at parse time, and
// the refusal says the key is global-only (PRD section 13's configuration
// row, P7): each chooses where, and as what, a process starts with the
// operator's credentials. The values are valid, so the refusal is about the
// document's standing and not about the value.
func TestARepositoryFileNamingARemoteHostKeyIsRefused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	cases := []struct {
		key Key
		doc string
	}{
		{KeyRemoteHosts, `{"remote_hosts": [` + remoteHost + `]}`},
		{KeyRemoteHosts, `{"remote_hosts": []}`},
		{KeyRemoteDisconnectGrace, `{"remote_disconnect_grace": "30m"}`},
		{KeyPlacementLocal, `{"placement": {"local": {"max_workers": 4}}}`},
		{KeyPlacementLocal, `{"placement": {"local": {}}}`},
		{KeySessionProvider, `{"session_provider": "herdr"}`},
	}
	for _, c := range cases {
		for _, origin := range []Origin{OriginTrusted, OriginPushed} {
			refusesKey(t, origin, c.doc, c.key, "global-only")
		}
	}
}

// A pushed placement.arch has no effect: it parses, is dropped at the merge
// and reported as a rejection, and the run resolves as if it were not there.
// It is trusted-only, because it narrows which machines a task may go to and
// a branch must not choose where its own work runs (P7).
func TestAPushedPlacementArchHasNoEffect(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	pushed := mustParse(t, OriginPushed, `{"placement": {"arch": ["arm64"]}}`)
	res, err := ResolveRun(Absent(OriginGlobal), Absent(OriginTrusted), pushed)
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if !reflect.DeepEqual(res.Config, Defaults()) {
		t.Errorf("a pushed placement.arch changed the configuration:\n got %+v\nwant %+v", res.Config, Defaults())
	}
	want := []Rejection{{Key: KeyPlacementArch, Origin: OriginPushed, Trust: TrustTrusted}}
	if !reflect.DeepEqual(res.Rejected, want) {
		t.Errorf("Rejected = %v, want %v", res.Rejected, want)
	}
}

// A host named under a provider other than herdr refuses at parse time,
// whether the provider is the default or named, and the refusal names
// remote_hosts and the provider it was named under. PRD section 8: remote
// hosts require herdr, so the provider stays one choice for the whole
// service.
func TestARemoteHostUnderAnotherProviderIsRefused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	for doc, provider := range map[string]string{
		`{"remote_hosts": [` + remoteHost + `]}`:                             `"pipe"`,
		`{"session_provider": "pipe", "remote_hosts": [` + remoteHost + `]}`: `"pipe"`,
		`{"session_provider": "tmux", "remote_hosts": [` + remoteHost + `]}`: `"tmux"`,
	} {
		refusesKey(t, OriginGlobal, doc, KeyRemoteHosts, provider, `"herdr"`)
	}
	// The same refusal from the operator's YAML document, which is the format
	// the home's file is read in.
	_, err := ParseYAML(OriginGlobal, []byte("session_provider: tmux\nremote_hosts:\n  - host: studio\n    root: /r\n    bridge: /b\n"))
	var ke *KeyError
	if !errors.As(err, &ke) || ke.Key != KeyRemoteHosts {
		t.Errorf("ParseYAML of a host under tmux = %v, want a refusal naming remote_hosts", err)
	}
}

// Every value outside its bounds is refused, and the refusal names the key.
// Each document below differs from an accepted one in the one field the case
// is about.
func TestRemoteAndPlacementValuesOutsideTheirBoundsAreRefused(t *testing.T) {
	t.Parallel()
	const herdr = `"session_provider": "herdr", `
	host := func(fields string) string {
		return `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/r", "bridge": "/b"` + fields + `}]}`
	}
	local := func(fields string) string { return `{"placement": {"local": {` + fields + `}}}` }
	cases := []struct {
		name string
		doc  string
		key  Key
	}{
		{"grace below the floor", `{"remote_disconnect_grace": "59s"}`, KeyRemoteDisconnectGrace},
		{"grace above the ceiling", `{"remote_disconnect_grace": "24h0m1s"}`, KeyRemoteDisconnectGrace},
		{"grace zero", `{"remote_disconnect_grace": "0s"}`, KeyRemoteDisconnectGrace},
		{"grace negative", `{"remote_disconnect_grace": "-30m"}`, KeyRemoteDisconnectGrace},
		{"grace a bare number", `{"remote_disconnect_grace": 30}`, KeyRemoteDisconnectGrace},
		{"grace not a duration", `{"remote_disconnect_grace": "half an hour"}`, KeyRemoteDisconnectGrace},

		{"two hosts", `{` + herdr + `"remote_hosts": [` + remoteHost + `, {"host": "mini", "root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"hosts not a list", `{` + herdr + `"remote_hosts": ` + remoteHost + `}`, KeyRemoteHosts},
		{"host entry not an object", `{` + herdr + `"remote_hosts": ["studio"]}`, KeyRemoteHosts},
		{"host entry with an unknown field", host(`, "user": "me"`), KeyRemoteHosts},
		{"host missing", `{` + herdr + `"remote_hosts": [{"root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"root missing", `{` + herdr + `"remote_hosts": [{"host": "studio", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"bridge missing", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/r"}]}`, KeyRemoteHosts},
		{"host empty", `{` + herdr + `"remote_hosts": [{"host": "", "root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"host an ssh option", `{` + herdr + `"remote_hosts": [{"host": "-oProxyCommand=sh", "root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"host with a space", `{` + herdr + `"remote_hosts": [{"host": "my studio", "root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"host with a control character", `{` + herdr + `"remote_hosts": [{"host": "stu\u0007dio", "root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"host not a string", `{` + herdr + `"remote_hosts": [{"host": 7, "root": "/r", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"root relative", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "assistant", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"root not clean", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/r/../s", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"root with a trailing slash", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/r/", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"root the filesystem root", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/", "bridge": "/b"}]}`, KeyRemoteHosts},
		{"bridge relative", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/r", "bridge": "bin/assistant"}]}`, KeyRemoteHosts},
		{"bridge home-relative", `{` + herdr + `"remote_hosts": [{"host": "studio", "root": "/r", "bridge": "~/bin/assistant"}]}`, KeyRemoteHosts},

		{"host max_workers zero", host(`, "max_workers": 0`), KeyRemoteHosts},
		{"host max_workers negative", host(`, "max_workers": -1`), KeyRemoteHosts},
		{"host max_workers fractional", host(`, "max_workers": 1.5`), KeyRemoteHosts},
		{"host min_free_disk zero", host(`, "min_free_disk": "0GiB"`), KeyRemoteHosts},
		{"host min_free_disk a bare number", host(`, "min_free_disk": 1024`), KeyRemoteHosts},
		{"host min_free_disk no unit", host(`, "min_free_disk": "1024"`), KeyRemoteHosts},
		{"host min_free_disk unknown unit", host(`, "min_free_disk": "20XB"`), KeyRemoteHosts},
		{"host min_free_disk fractional", host(`, "min_free_disk": "1.5GiB"`), KeyRemoteHosts},
		{"host min_free_disk overflows", host(`, "min_free_disk": "9999999999TiB"`), KeyRemoteHosts},
		{"host max_load zero", host(`, "max_load": 0`), KeyRemoteHosts},
		{"host max_load negative", host(`, "max_load": -2`), KeyRemoteHosts},
		{"host max_load text", host(`, "max_load": "8"`), KeyRemoteHosts},

		{"local not an object", `{"placement": {"local": 4}}`, KeyPlacementLocal},
		{"local with an unknown field", local(`"max_cpus": 4`), KeyPlacementLocal},
		{"local max_workers zero", local(`"max_workers": 0`), KeyPlacementLocal},
		{"local max_workers negative", local(`"max_workers": -3`), KeyPlacementLocal},
		{"local max_workers text", local(`"max_workers": "4"`), KeyPlacementLocal},
		{"local min_free_disk zero", local(`"min_free_disk": "0B"`), KeyPlacementLocal},
		{"local min_free_disk with a space", local(`"min_free_disk": "20 GiB"`), KeyPlacementLocal},
		{"local min_free_disk negative", local(`"min_free_disk": "-20GiB"`), KeyPlacementLocal},
		{"local max_load zero", local(`"max_load": 0`), KeyPlacementLocal},
		{"local max_load negative", local(`"max_load": -0.5`), KeyPlacementLocal},

		{"arch not a list", `{"placement": {"arch": "arm64"}}`, KeyPlacementArch},
		{"arch entry not a string", `{"placement": {"arch": [64]}}`, KeyPlacementArch},
		{"arch entry empty", `{"placement": {"arch": [""]}}`, KeyPlacementArch},
		{"arch entry unknown", `{"placement": {"arch": ["amd46"]}}`, KeyPlacementArch},
		{"arch entry uname's spelling", `{"placement": {"arch": ["x86_64"]}}`, KeyPlacementArch},
		{"arch entry repeated", `{"placement": {"arch": ["arm64", "arm64"]}}`, KeyPlacementArch},

		{"provider misspelled", `{"session_provider": "Herdr"}`, KeySessionProvider},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			refusesKey(t, OriginGlobal, c.doc, c.key)
		})
	}
}

// A home that sets none of these keys places exactly as before they existed:
// no remote host, no limit, every architecture, and the PRD's 30m grace.
func TestTheRemoteAndPlacementDefaultsChangeNothing(t *testing.T) {
	t.Parallel()
	c := Defaults()
	if c.RemoteHosts != nil || c.PlacementLocal.Set() || c.PlacementArch != nil {
		t.Errorf("defaults = %+v, %+v, %v, want no host, no limit, and every architecture",
			c.RemoteHosts, c.PlacementLocal, c.PlacementArch)
	}
	if c.RemoteDisconnectGrace != 30*time.Minute {
		t.Errorf("RemoteDisconnectGrace = %v, want 30m", c.RemoteDisconnectGrace)
	}
	if c.SessionProvider != SessionProviderPipe {
		t.Errorf("SessionProvider = %q, want pipe", c.SessionProvider)
	}
}

// The accepting side of every refusal above, so none of them passes by
// refusing everything: each value at its bound, each unit, an empty list and
// an empty object, a host under herdr with its limits, and a trusted
// placement.arch, which takes effect where a pushed one does not.
func TestRemoteAndPlacementValuesWithinTheirBoundsAreAccepted(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	global := func(doc string) Config {
		t.Helper()
		return resolve(t, mustParse(t, OriginGlobal, doc), Absent(OriginTrusted)).Config
	}
	for doc, want := range map[string]time.Duration{
		`{"remote_disconnect_grace": "1m"}`:  time.Minute,
		`{"remote_disconnect_grace": "24h"}`: 24 * time.Hour,
	} {
		if got := global(doc).RemoteDisconnectGrace; got != want {
			t.Errorf("%s resolved to %v, want %v", doc, got, want)
		}
	}
	for _, doc := range []string{`{"remote_hosts": []}`, `{"session_provider": "tmux", "remote_hosts": []}`} {
		if got := global(doc).RemoteHosts; len(got) != 0 {
			t.Errorf("%s resolved to %v, want no host", doc, got)
		}
	}
	if got := global(`{"placement": {"local": {}}}`).PlacementLocal; got.Set() {
		t.Errorf("an empty placement.local resolved to %+v, want no limit", got)
	}
	c := global(`{"session_provider": "herdr", "remote_hosts": [{"host": "user@studio.local", "root": "/r",
		"bridge": "/opt/assistant/bin/assistant", "max_workers": 1, "min_free_disk": "1B", "max_load": 0.25}]}`)
	want := []RemoteHost{{Host: "user@studio.local", Root: "/r", Bridge: "/opt/assistant/bin/assistant",
		Limits: PlacementLimits{MaxWorkers: 1, MinFreeDiskBytes: 1, MaxLoad: 0.25}}}
	if !reflect.DeepEqual(c.RemoteHosts, want) || c.SessionProvider != SessionProviderHerdr {
		t.Errorf("a host under herdr resolved to %+v under %q, want %+v under herdr", c.RemoteHosts, c.SessionProvider, want)
	}
	for size, bytes := range map[string]int64{
		"7B": 7, "2KB": 2000, "3MB": 3e6, "4GB": 4e9, "5TB": 5e12,
		"2KiB": 2 << 10, "3MiB": 3 << 20, "4GiB": 4 << 30, "5TiB": 5 << 40,
	} {
		doc := `{"placement": {"local": {"min_free_disk": "` + size + `"}}}`
		if got := global(doc).PlacementLocal.MinFreeDiskBytes; got != bytes {
			t.Errorf("min_free_disk %q resolved to %d bytes, want %d", size, got, bytes)
		}
	}
	trusted := mustParse(t, OriginTrusted, `{"placement": {"arch": ["arm64"]}}`)
	pushed := mustParse(t, OriginPushed, `{"placement": {"arch": ["amd64"]}}`)
	res, err := ResolveRun(Absent(OriginGlobal), trusted, pushed)
	if err != nil {
		t.Fatalf("ResolveRun: %v", err)
	}
	if !slices.Equal(res.Config.PlacementArch, []string{"arm64"}) {
		t.Errorf("PlacementArch = %v, want the trusted copy's [arm64] and not the pushed copy's", res.Config.PlacementArch)
	}
}

// Architectures is exactly the GOARCH values the toolchain the tests run with
// lists for darwin and linux, so the closed set placement.arch is held to
// names every architecture a supported machine can report and nothing else.
// A toolchain that adds or drops one fails here.
//
// The listing runs as a process group of its own under a deadline that ends
// before the test binary's, as P15 asks of every process: a go that stalls is ended by this test,
// while this process is alive to end it, rather than outliving a binary the
// test deadline kills. Before it, a go blocked reading its GOENV file
// survived the binary's timeout with no parent.
func TestArchitecturesAreTheToolchainsDarwinAndLinuxTargets(t *testing.T) {
	t.Parallel()
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go on PATH to list the toolchain's targets: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), listingDeadline(t))
	defer cancel()
	var out, stderr bytes.Buffer
	cmd := procgroup.Command(goTool, "tool", "dist", "list")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := procgroup.Run(ctx, cmd, time.Second); err != nil {
		t.Fatalf("go tool dist list: %v (context: %v)\n%s", err, ctx.Err(), stderr.String())
	}
	var want []string
	for _, target := range strings.Fields(out.String()) {
		goos, goarch, _ := strings.Cut(target, "/")
		if (goos == "darwin" || goos == "linux") && !slices.Contains(want, goarch) {
			want = append(want, goarch)
		}
	}
	slices.Sort(want)
	if got := Architectures(); !slices.Equal(got, want) {
		t.Errorf("Architectures() = %v, want the toolchain's %v", got, want)
	}
}

// listingDeadline is how long the toolchain listing may take: a minute, or
// half of what is left before the test binary's own deadline when that is
// sooner, so the listing's group is ended and reaped while there is still
// time to report why.
func listingDeadline(t *testing.T) time.Duration {
	d := time.Minute
	if end, ok := t.Deadline(); ok {
		d = min(d, time.Until(end)/2)
	}
	return d
}
