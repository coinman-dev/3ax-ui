//go:build ignore

package main

import (
	"fmt"
	"net"
	"net/http"
	"time"
)

type dummyListener struct{ net.Listener }

func main() {
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})}
	l4, _ := net.Listen("tcp4", "0.0.0.0:8081")
	l6, _ := net.Listen("tcp6", "[::]:8081")

	go srv.Serve(l6)
	go srv.Serve(l4)

	// Test ipv4
	resp, err := http.Get("http://127.0.0.1:8081")
	fmt.Println("IPv4:", err, resp)

	// Test ipv6
	resp6, err6 := http.Get("http://[::1]:8081")
	fmt.Println("IPv6:", err6, resp6)

	time.Sleep(1 * time.Second)
}
