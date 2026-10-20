// Command lean-interact provides the maintenance commands of LeanInteract:
//
//	lean-interact install-lean   install elan (the Lean toolchain manager)
//	lean-interact clear-cache    delete the LeanInteract cache directory
package main

import (
	"fmt"
	"os"

	leaninteract "github.com/ivigasin/lean-interact/go"
)

func usage() {
	fmt.Fprintf(os.Stderr, "usage: lean-interact <install-lean|clear-cache|cache-dir>\n")
	os.Exit(2)
}

func main() {
	if len(os.Args) != 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "install-lean":
		err = leaninteract.InstallLean()
	case "clear-cache", "clear-lean-cache":
		err = leaninteract.ClearCache()
		if err == nil {
			fmt.Println("removed", leaninteract.DefaultCacheDir())
		}
	case "cache-dir":
		fmt.Println(leaninteract.DefaultCacheDir())
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lean-interact:", err)
		os.Exit(1)
	}
}
