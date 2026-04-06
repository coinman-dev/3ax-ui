//go:build ignore

package main

import (
	"fmt"
	"net"
)

func main() {
	l4, err := net.Listen("tcp4", "0.0.0.0:8080")
	if err != nil {
		fmt.Println("l4 err:", err)
		return
	}
	defer l4.Close()
	fmt.Println("l4 bound")

	l6, err6 := net.Listen("tcp6", "[::]:8080")
	if err6 != nil {
		fmt.Println("l6 err:", err6)
		return
	}
	defer l6.Close()
	fmt.Println("l6 bound")
}
