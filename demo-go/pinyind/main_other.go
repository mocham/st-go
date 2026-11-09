//go:build !linux

package main

import "fmt"

func main() { fmt.Println("govim-pinyind currently requires Linux abstract Unix sockets") }
