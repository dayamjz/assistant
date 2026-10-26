//go:build darwin || linux

package journey

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// buildScript starts its arguments in this shell's process group and kills
// that group when stdin closes. The shell is /bin/sh -c, which is not
// interactive, so the background watcher stays in the shell's group rather
// than a group of its own. procgroup starts that shell as the group leader,
// so the group is the shell, the watcher, and the build. This process holds
// the write end of stdin. The kernel closes it when this process exits,
// including when the testing alarm exits it before Run handles
// cancellation, and the watcher then kills the group. A normal exit of the
// build kills the watcher first, so the close after Run returns does not
// kill a group that already finished.
//
// The watcher reads the saved descriptor, not its own standard input. A
// background command in a shell without job control has its standard input
// taken from /dev/null, which would look closed at once and kill the group
// before the build starts. A descendant that leaves the group is not
// reached by the kill. That is the same limit procgroup states for its own
// signal.
const buildScript = `exec 3<&0
(
  while IFS= read -r _ <&3; do
    :
  done
  kill -KILL -$$
) >/dev/null 2>&1 &
watcher=$!
"$@"
status=$?
kill "$watcher" 2>/dev/null
wait "$watcher" 2>/dev/null
exit "$status"
`

// holdCommand starts args under buildScript. The closer holds the pipe the
// group is reading. This process exiting closes that pipe and the watcher
// kills the group. The command's directory, environment, and output are the
// caller's.
func holdCommand(args ...string) (*exec.Cmd, io.Closer, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd := procgroup.Command("/bin/sh", append([]string{"-c", buildScript, "_"}, args...)...)
	cmd.Stdin = r
	return cmd, &pipeLife{r: r, w: w}, nil
}

// buildCommand is the product-binary build. It is holdCommand, so this
// process exiting ends the build's group.
func buildCommand(built string) (*exec.Cmd, io.Closer, error) {
	return holdCommand("go", "build", "-o", built, "./cmd/assistant")
}

// RunHeld runs args in dir and returns what they wrote. env replaces the
// environment when it is not nil. The command is holdCommand's shell, so
// its group ends when this process exits, including when the testing alarm
// exits it before Run handles cancellation. A descendant that leaves the
// group is not reached.
func RunHeld(dir string, env []string, args ...string) ([]byte, error) {
	cmd, life, err := holdCommand(args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = life.Close() }()
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = procgroup.Run(context.Background(), cmd, buildGrace)
	return out.Bytes(), err
}

// pipeLife closes both ends of the pipe this process holds. The child
// already has its own read end once it has started.
type pipeLife struct {
	r *os.File
	w *os.File
}

func (p *pipeLife) Close() error {
	errR := p.r.Close()
	errW := p.w.Close()
	if errR != nil {
		return errR
	}
	return errW
}
