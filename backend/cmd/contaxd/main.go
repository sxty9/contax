// Command contaxd is the holistic contacts service daemon. It exposes an HTTP surface under
// /api/services/contax/, validates the shared holistic session (a signed JWT in the h_access
// cookie) without any RPC to the holistic backend, and enforces admin live from the OS. Contact
// visibility is computed from Linux group overlap (hc_* groups privleg materialises); external
// contacts + each user's hidden set are the only state contax owns, kept in a JSON file. Runs
// unprivileged behind the holistic Caddy proxy.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"contax/internal/api"
	"contax/internal/auth"
	"contax/internal/contacts"
	"contax/internal/directory"
	"contax/internal/instance"
	"contax/internal/profile"
	"contax/internal/store"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8777", "address to listen on")
	flag.Parse()

	secret, err := auth.LoadSecret()
	if err != nil {
		log.Fatalf("contaxd: %v", err)
	}
	// Admin = membership in this group (the single Linux source of truth). The systemd unit sets
	// CONTAX_ADMIN_GROUP; the verifier defaults to "sudo" when it is empty.
	v := auth.NewVerifier(secret, os.Getenv("CONTAX_ADMIN_GROUP"))

	dataRoot := getenv("CONTAX_DATA", "/var/lib/contax")
	st, err := store.Open(filepath.Join(dataRoot, "contacts.json"))
	if err != nil {
		log.Fatalf("contaxd: open store: %v", err)
	}
	dir := directory.New(os.Getenv("CONTAX_ENUM_GROUP")) // defaults to smbusers
	prof := profile.New()
	inst := instance.New()
	svc := contacts.New(dir, prof, inst, st)

	srv := &http.Server{
		Handler:           api.New(v, svc).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind synchronously so an "address in use" surfaces here, not in a goroutine.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("contaxd: listen %s: %v", *listen, err)
	}
	go func() {
		log.Printf("contaxd listening on %s (data=%s, mailDomain=%q)", *listen, dataRoot, inst.MailDomain())
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatalf("contaxd: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Print("contaxd stopped")
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
