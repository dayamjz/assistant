// Package compilehold is the process whose owner watch the harness-compile
// test runs. Its test starts the compile the harness starts, and the test is
// built only on the platforms where that compile holds a life pipe.
package compilehold
