// Command nimbus-fakeapi serves the in-memory fake control plane, so the CLI
// can be tried by hand before the real server exists. It isn't released.
//
//	go run ./cmd/nimbus-fakeapi
//	export NIMBUS_API_URL=http://127.0.0.1:8787
//	go run ./cmd/nimbus login
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/wceriale/nimbus-cli/internal/fakeapi"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "address to listen on")
	email := flag.String("email", "you@acme.com", "the user every login signs in as")
	autoApprove := flag.Bool("auto-approve", false, "approve logins without the browser step")
	flag.Parse()

	f := fakeapi.New(*email)
	f.AutoApprove = *autoApprove
	log.Printf("Fake Nimbus API for %s on http://%s", *email, *addr)
	log.Printf("export NIMBUS_API_URL=http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, f.Handler()))
}
