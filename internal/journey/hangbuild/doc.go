// Package hangbuild is the child process the product-build timeout test
// spawns and then ends the way the testing alarm ends a process. Its test
// asks journey.Binary for the product with a PATH whose go never finishes,
// and it does nothing unless JOURNEY_HANG_BUILD is set, so a normal suite
// run skips it. This file gives the package a non-test file, so it has
// export data and the procgroup guard can load it on every platform.
package hangbuild
