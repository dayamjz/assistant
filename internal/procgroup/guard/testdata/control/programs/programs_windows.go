package programs

import "os"

// Windows writes a program only the Windows build holds, which the write
// scan does not read: a start there does not fork.
func Windows() error { return os.WriteFile("p", nil, 0o755) }
