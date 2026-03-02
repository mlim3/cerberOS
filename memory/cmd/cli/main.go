

package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("memory-cli: no command provided")
		fmt.Println("usage: memory-cli <command>")
		os.Exit(1)
	}

	command := os.Args[1]

	switch command {
	case "health":
		fmt.Println("memory-cli: OK")
	default:
		fmt.Printf("memory-cli: unknown command '%s'\n", command)
		os.Exit(1)
	}
}