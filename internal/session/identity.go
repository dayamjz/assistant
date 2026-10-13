package session

import "errors"

// errPlatform is startTime's answer on a platform none of the identity files
// builds for: no start time can be read there, so Create records zero and
// Probe answers Unobserved for any reference it does not hold.
var errPlatform = errors.New("session: this platform has no start-time read")

// startTime is implemented per platform. It reads the start time of the
// process pid in the platform's own units, compared for equality only. found
// is false when no running process has the identifier, which is the one
// answer that proves absence; a process that has exited and not yet been
// reaped counts as not running where the platform lets that be seen. err is
// every failure to read, and errPlatform where nothing can be read.
