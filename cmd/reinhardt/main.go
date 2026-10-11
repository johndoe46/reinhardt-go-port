package main

import (
	"fmt"
	"os"

	"go-reinhardt/internal/reinhardt"
)

func main() {
	if err := reinhardt.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
