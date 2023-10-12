package main

import (
	"fmt"
	"os"

	"github.com/openclarity/vmclarity/cmd/vmclarity-cr-discovery-server/cmd"
)

func main() {
	err := cmd.Execute()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
