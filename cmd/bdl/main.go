package main

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func main() {
	bin, err := exec.LookPath("beadloom")
	if err != nil {
		fmt.Fprintln(os.Stderr, "bdl: beadloom not found on PATH")
		os.Exit(1)
	}
	if err := syscall.Exec(bin, append([]string{"beadloom"}, os.Args[1:]...), os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "bdl: %v\n", err)
		os.Exit(1)
	}
}
