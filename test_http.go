//go:build ignore

package main

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

func main() {
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintln(w, "Hello")
		}),
	}
	l1, _ := net.Listen("tcp", "127.0.0.1:8081")
	l2, _ := net.Listen("tcp", "127.0.0.1:8082")

	go func() {
		err := srv.Serve(l1)
		fmt.Println("l1 serve done:", err)
	}()
	go func() {
		err := srv.Serve(l2)
		fmt.Println("l2 serve done:", err)
	}()

	time.Sleep(1 * time.Second)
	fmt.Println("running")
}
