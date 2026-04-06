//go:build ignore

package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net/http"
)

func main() {
	reqStr := "GET /sub-kE9th6XDHblm/ly8f4t9d59q91zwx HTTP/1.1\r\n" +
		"Host: [2a10:9200:0:12e::]:2096\r\n"

	// Pad headers to 2050 bytes
	padLen := 2050 - len(reqStr) - 4 // 4 for \r\n\r\n
	pad := ""
	for i := 0; i < padLen; i++ {
		pad += "a"
	}
	reqStr += "X-Pad: " + pad + "\r\n\r\n"

	buf := []byte(reqStr)
	fmt.Println("Total size:", len(buf))

	firstBuf := buf[:2048]

	reader := bytes.NewReader(firstBuf)
	bufReader := bufio.NewReader(reader)
	req, err := http.ReadRequest(bufReader)
	fmt.Println("ReadRequest err:", err)
	if req != nil {
		fmt.Println("Req:", req.URL)
	}
}
