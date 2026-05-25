package main

import (
	"fmt"
	"os"

	"github.com/befabri/goqual/internal/quality"
)

func main() {
	code, err := quality.Run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(code)
}
