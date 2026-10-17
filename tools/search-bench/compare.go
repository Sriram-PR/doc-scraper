package main

import "io"

type comparison struct{}

func compare(base, cur runFile, seed uint64) comparison { return comparison{} }

func printComparison(w io.Writer, c comparison) {}
