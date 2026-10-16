package forge_test

import (
	"testing"

	"github.com/dayamjz/assistant/internal/forge"
)

func TestClassifyRemoteSeparatesGitHubLocalAndUnsupported(t *testing.T) {
	t.Parallel()
	cases := []struct {
		remote string
		want   forge.RemoteKind
	}{
		{"https://github.com/dayamjz/assistant.git", forge.RemoteGitHub},
		{"git@github.com:dayamjz/assistant.git", forge.RemoteGitHub},

		{"/srv/repos/assistant.git", forge.RemoteLocal},
		{"./remote.git", forge.RemoteLocal},
		{"../remote.git", forge.RemoteLocal},
		{"remote.git", forge.RemoteLocal},
		{"file:///srv/repos/assistant.git", forge.RemoteLocal},
		{"file://localhost/srv/repos/assistant.git", forge.RemoteLocal},
		{`C:\repos\assistant`, forge.RemoteLocal},
		{`C:/repos/assistant`, forge.RemoteLocal},

		{"https://gitlab.com/dayamjz/assistant.git", forge.RemoteUnsupported},
		{"git@gitlab.com:dayamjz/assistant.git", forge.RemoteUnsupported},
		{"https://github.example.com/dayamjz/assistant.git", forge.RemoteUnsupported},
		{"https://notgithub.com/dayamjz/assistant.git", forge.RemoteUnsupported},
		{"", forge.RemoteUnsupported},

		// Scheme-less host/path: not a filesystem path and not a form
		// GitHubRepository reads. Must not classify as local (#291 review).
		{"github.com/org/repo", forge.RemoteUnsupported},
		{"github.com/org/repo.git", forge.RemoteUnsupported},
		{"gitlab.com/org/repo", forge.RemoteUnsupported},
	}
	for _, c := range cases {
		t.Run(c.remote, func(t *testing.T) {
			t.Parallel()
			if got := forge.ClassifyRemote(c.remote); got != c.want {
				t.Fatalf("ClassifyRemote(%q) = %v, want %v", c.remote, got, c.want)
			}
		})
	}
}
