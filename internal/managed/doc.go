// Package managed hands a home's service to the operating system's own
// service manager, so it comes back after the user logs in again or the
// machine restarts: a launchd agent on macOS and a systemd user unit on
// Linux. It is what assistant service start --install and assistant service
// stop --uninstall act through, and it is the only package that writes a
// service definition or invokes launchctl or systemctl.
//
// # What it writes and what it starts
//
// A definition starts this program serving one home in the foreground, with
// --managed, which is the one thing that changes under a manager: a restart
// the service is asked for ends the process with a failure status for the
// manager to replace, rather than the process launching its successor, which
// a manager tracking the process would not know about. Spec says what is
// written, and systemdUnit and launchdPlist say which directives and keys and what each
// one asks of the manager. The definition is named from a digest of the home
// root, so two homes have two.
//
// Nothing from a repository reaches a definition. The program's path, the
// home root and the search path are the installing command's own, and PRD
// principle P7's rule about configuration that executes code is about a
// repository's documents, none of which is read here. The search path is the
// one variable carried from the installing environment, and no other variable,
// a credential among them, is written into a file.
//
// # One service per home is still the lock's
//
// A managed service is still one process taking the home's exclusive lock, so
// a second one cannot serve the home whoever starts it. What this package
// does not do is decide whether installing would start a second: the caller
// holds the readiness check and refuses an install while a service answers.
// A manager restarting a service that lost the lock to one started another
// way keeps failing and restarting until the other stops, at the pace
// RestartSec or ThrottleInterval asks for; nothing here detects that.
//
// # macOS's protected folders
//
// macOS keeps a program from reading Documents, Desktop and Downloads under a
// user's home unless the user has granted it access, and no-mistakes' launchd
// daemon failed exactly there (issue #234): it could not read repositories
// under Documents. A process launchd starts from a LaunchAgent is not started
// by the terminal the install ran in, and this package does not assume it
// holds any grant that terminal holds; whether and when it does was not
// verified for this change.
//
// So a launchd install refuses, with ReasonProtectedFolder naming the paths,
// when the home root, the program, or any checkout the home serves is one of
// protectedFolders or inside one. The remedy it names has two forms: move
// them out, or grant the program access - Apple documents Full Disk Access in
// System Settings, under Privacy & Security, as the grant - and install again
// with Spec.AccessGranted, which assistant service start sets from
// --full-disk-access-granted. That flag is the operator's statement and
// nothing here checks it: with no grant behind it, the service starts and a
// run under a protected folder fails there with git's own error.
//
// What was not verified, on the Linux machine this was written and tested on:
// that a Full Disk Access grant to the program reaches the process launchd
// starts, whether a rebuilt or replaced program keeps a grant given to the
// old one, and how launchd treats ThrottleInterval, AbandonProcessGroup and
// KeepAlive in practice. The definition is checked to parse as a property
// list, and the commands are checked against a stand-in launchctl; launchd
// itself never ran.
//
// Gaps in the check itself: it covers the three folders and not the other
// locations macOS protects; it holds the checkouts the home serves when the
// install runs, so a checkout gated later under a protected folder is not
// held to it; and it compares paths ignoring letter case and through symbolic
// links where they resolve, as protectedPaths states.
//
// # systemd
//
// The unit is written to the user's unit folder, XDG_CONFIG_HOME's or the
// home's .config, enabled and started with systemctl --user. It starts when
// the user's systemd instance does, which is at login, or at boot for a user
// with lingering enabled; nothing here turns lingering on, which systemd
// documents as what starts a user's instance without a login and keeps it
// past the last session. The unit was checked with
// systemd-analyze verify when this was written, and the tests drive a
// stand-in systemctl that starts what the unit's ExecStart says; a real
// systemd user instance never ran under them, because the machine they run on
// has none.
//
// # Windows and the rest
//
// ForThisPlatform refuses with ReasonUnsupported on any operating system but
// macOS and Linux. Windows has a service manager, and this package writes
// nothing for it.
package managed
