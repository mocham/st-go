//go:build !linux

package main

import (
	"fmt"
	"io"
	"os"
)

func run(options, *os.File, io.Writer) error { return fmt.Errorf("gov requires Linux st-fb") }
