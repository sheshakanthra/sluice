// Package leakcheck runs goleak at the end of a TestMain that also manages
// testcontainers (P7 hygiene: no goroutine leaks). Goroutines owned by the
// container tooling itself (the Docker client, the Ryuk reaper) are ignored;
// everything else that is still running after the tests is a leak.
package leakcheck

import (
	"fmt"
	"os"

	"go.uber.org/goleak"
)

// ignores are goroutines of the test infrastructure, not of SLUICE code.
var ignores = []goleak.Option{
	// Docker / testcontainers HTTP keep-alive connections and the reaper.
	goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
	goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	goleak.IgnoreAnyFunction("github.com/testcontainers/testcontainers-go.(*Reaper).connect.func1"),
	goleak.IgnoreAnyFunction("github.com/testcontainers/testcontainers-go.(*reaperSpawner).reaper.func1"),
	// Windows: the Docker client's named-pipe I/O completion loop.
	goleak.IgnoreAnyFunction("github.com/Microsoft/go-winio.ioCompletionProcessor"),
	// OpenCensus/OTel workers started by the Docker client's dependencies.
	goleak.IgnoreAnyFunction("go.opencensus.io/stats/view.(*worker).start"),
}

// Check returns code unchanged if no goroutine leaked, else prints the
// leaks and returns 1. Call it after m.Run() and after closing containers.
func Check(code int) int {
	if err := goleak.Find(ignores...); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "leakcheck:", err)
		return 1
	}
	return code
}
