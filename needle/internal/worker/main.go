package worker

import "os"

// Main is the entry point used when the worker is compiled as a standalone
// binary. It expects argv[1] == "--child".
func Main() int {
	if len(os.Args) < 2 || os.Args[1] != "--child" {
		return 2
	}
	return RunChild(os.Stdin, os.Stdout)
}
