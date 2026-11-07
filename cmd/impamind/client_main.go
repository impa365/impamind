//go:build client

package main

import (
	"fmt"
	"os"

	"impamind/internal/client"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: impamind hook [event] | sync pull|push [--dir path] | version")
		os.Exit(2)
	}
	env := client.FromEnv()
	var err error
	switch os.Args[1] {
	case "hook":
		err = client.Hook(os.Args[2:], os.Stdin, os.Stdout, env)
	case "sync":
		err = client.Sync(os.Args[2:], env)
	case "version":
		fmt.Println("1.0.0")
	default:
		err = fmt.Errorf("unknown command %s", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
