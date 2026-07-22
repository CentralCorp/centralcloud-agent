package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			return
		case "healthcheck":
			c := http.Client{Timeout: time.Second}
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:8080/up", nil)
			r, e := c.Do(req)
			if e != nil || r.StatusCode != 200 {
				os.Exit(1)
			}
			_ = r.Body.Close()
			return
		case "tcpcheck":
			if len(os.Args) != 3 {
				os.Exit(2)
			}
			dialer := net.Dialer{Timeout: 2 * time.Second}
			connection, err := dialer.DialContext(context.Background(), "tcp", os.Args[2]) //nolint:gosec // Test-only probe target is supplied by the integration harness.
			if err != nil {
				os.Exit(1)
			}
			_ = connection.Close()
			return
		}
	}
	health := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	}
	http.HandleFunc("/up", health)
	http.HandleFunc("/health", health)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "fake panel") })
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	if e := server.ListenAndServe(); e != nil {
		panic(e)
	}
}
