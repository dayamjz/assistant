package raw

import "os/exec"

// Windows is a start only the Windows build holds.
func Windows() *exec.Cmd { return exec.Command("cmd") }
