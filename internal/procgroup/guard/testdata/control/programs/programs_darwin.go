package programs

import "os"

// Darwin writes a program only the darwin build holds.
func Darwin() error { return os.WriteFile("p", nil, 0o755) }
