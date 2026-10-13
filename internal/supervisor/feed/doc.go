// Package feed is the I/O edge of internal/supervisor: it reads the beacon
// file, the instance file, the lock probe and the database, and hands what it
// found to that package's pure predicates. It exists as its own package so
// that internal/supervisor keeps the shape internal/graph has, no clock, no
// store and no file in it, while the three readers PRD section 5's P10 layers
// need, the pull guard, arm verification and the stop hook, share one reader
// rather than each re-cutting the files.
//
// # What it reads, and what each read can and cannot say
//
// The beacon is read whole and decoded by supervisor.DecodeBeacon. A read
// that does not decode is read once more before it is answered as no beacon,
// because the writer rewrites the record in place and a reader can see part
// of one write and part of the next; two torn reads in a row are answered as
// no beacon, which blocks, and nothing here distinguishes a torn read from a
// file of another shape. The instance file is read by internal/home, which
// wrote it. The lock is internal/home's probe, with that probe's gap: a lone
// Held may be another probe's instant, and the consumer rule there is that
// Held counts as live only beside a matching fresh beacon, which is what
// supervisor.Assess applies.
//
// The in-flight reading is taken through store.OpenReadOnly, so a hook or a
// command reads the record without being the service that owns it and
// without migrating it. A database that cannot be opened that way is
// supervisor.InFlightUnknown, never none, and Assess blocks on it. The states
// are resolved by internal/taskstate over that handle with no terminal probe
// and no advancing answer, because neither exists outside the service: a
// task whose latest attempt has a terminal is resolved as if nobody looked,
// and a run the service is driving is resolved as if nothing drives it. Both
// lean toward in flight or unknown, which is the safe direction, and a
// service reading its own fleet supplies its own reading through InFlightFrom
// rather than this one.
//
// # The writer
//
// Writer is the beacon's one writer, and its one caller is internal/service's
// supervisor loop, from its own select so that a wedged loop cannot keep a
// fresh beacon; that package's watchdog reads the beacon through ReadBeacon
// and never writes it. Each write is one WriteAt of the
// whole record at offset zero through a handle kept open, so the file is
// never truncated and never renamed, and a reader on any platform the
// module builds for can open it beside the writer.
//
// # Residual gaps
//
//   - The retry is one, chosen rather than measured. A writer that rewrites
//     faster than a reader reads twice makes both reads torn, and the reader
//     then blocks falsely, which is the safe direction.
//   - Nothing authenticates the beacon. A 32-bit checksum says the record was
//     written whole, and a process that can write the home's files can write
//     a beacon that reads as live.
//   - VerifyArm polls the beacon on a fraction of the interval and reads the
//     lock and the instance file on every look; a service that comes up
//     between looks is seen on the next one, and a lock that is let go and
//     retaken between looks is not seen at all.
//   - VerifyArm's one caller over the socket is internal/service's
//     supervision.assert, which refuses a task's worker from the worker
//     group set before it reads anything; a verification from inside the
//     service that holds the lock reads Held from its own probe, so what it
//     adds there is whether the loop is writing.
package feed
