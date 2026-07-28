package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"

	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "serve-release":
			serveRelease()
			return
		case "rpm-manager":
			// Prints DetectRPMManager() directly so CI legs that claim a
			// specific RPM front-end (e.g. via matrix rpm_frontend) can
			// assert the manager termp would actually pick, rather than
			// only asserting the broader install method (#406).
			fmt.Println(updatepkg.DetectRPMManager())
			return
		}
	}
	fmt.Println(updatepkg.DetectInstallMethod())
}

func serveRelease() {
	if len(os.Args) != 6 {
		log.Fatal("usage: detect-install serve-release CERT KEY READY REQUESTS")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:443")
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(os.Args[4], []byte("ready\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests, err := os.OpenFile(os.Args[5], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		_, writeErr := fmt.Fprintln(requests, request.URL.Path)
		closeErr := requests.Close()
		if writeErr != nil || closeErr != nil {
			http.Error(writer, "record request", http.StatusInternalServerError)
			return
		}
		if request.URL.Path != "/repos/polter-dev/discord_terminal_presence/releases/latest" {
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintln(writer, `{"tag_name":"v99.0.0"}`)
	})
	server := &http.Server{Handler: handler}
	log.Fatal(server.ServeTLS(listener, os.Args[2], os.Args[3]))
}
