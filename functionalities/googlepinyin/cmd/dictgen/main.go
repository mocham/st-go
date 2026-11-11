package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"st-go/googlepinyin"
)

func run() error {
	rawPath := flag.String("raw", "", "upstream UTF-16LE raw dictionary")
	validPath := flag.String("valid", "", "upstream UTF-16LE valid-character list")
	outputPath := flag.String("output", "", "generated gzip dictionary")
	flag.Parse()
	if flag.NArg() != 0 || *rawPath == "" || *validPath == "" || *outputPath == "" {
		return fmt.Errorf("requires -raw, -valid, and -output")
	}
	raw, err := os.Open(*rawPath)
	if err != nil {
		return err
	}
	defer raw.Close()
	valid, err := os.Open(*validPath)
	if err != nil {
		return err
	}
	defer valid.Close()
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0755); err != nil {
		return err
	}
	temporary := *outputPath + ".next"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	err = googlepinyin.BuildDictionary(raw, valid, output)
	if closeErr := output.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, *outputPath)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dictgen:", err)
		os.Exit(1)
	}
}
