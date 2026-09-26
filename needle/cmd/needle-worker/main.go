// Command needle-worker is the child process that hosts libneedle.
//
// It is built once per machine when the parent program imports the needle
// package, then re-exec'd by every Needle value. Users do not invoke it
// directly.
//
// Protocol: length-prefixed JSON frames on stdin/stdout, identical to
// needle/_worker.py in the reference Python implementation. See the
// internal/worker package for details.
package main

import (
	"fmt"
	"os"

	"github.com/mhs003/needless/needle/internal/worker"
)

func main() {
	code := worker.Main()
	if code != 0 {
		fmt.Fprintf(os.Stderr, "needle-worker exited %d\n", code)
	}
	os.Exit(code)
}
