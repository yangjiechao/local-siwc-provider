package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: siwc-provider <ui|login|status|key|serve|logout> [options]\nCommands:\n  ui      Open the local web control panel (API initially stopped)\n  login   Continue with ChatGPT (OAuth PKCE)\n  status  Show current authorization status\n  key     Display the local API key (keep private)\n  serve   Start localhost API and UI; --port 8787\n  logout  Revoke current renewable session")
}
func run() error {
	if len(os.Args) < 2 {
		os.Args = append(os.Args, "ui")
	}
	path := os.Getenv("SIWC_STATE_FILE")
	store, err := newStore(path)
	if err != nil {
		return err
	}
	auth := newAuth(store)
	switch os.Args[1] {
	case "login":
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		if err := auth.login(ctx); err != nil {
			return err
		}
		fmt.Println("OAuth authorization validated and saved locally")
	case "status":
		s := store.get()
		if s.Credential == nil || s.Credential.AccessToken == "" {
			fmt.Println("Signed in: no")
		} else {
			fmt.Printf("Signed in: yes\nAccount: %s\nClient: %s\nAccess token expires: %s\n", s.Credential.Email, s.Credential.ClientID, s.Credential.ExpiresAt.Format(time.RFC3339))
		}
	case "key":
		fmt.Println("This is a localhost-only secret; do not commit or share:")
		fmt.Println(store.get().LocalKey)
	case "logout":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := auth.logout(ctx); err != nil {
			return err
		}
		fmt.Println("Session revoked and credentials cleared")
	case "serve", "ui":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		port := flags.Int("port", 8787, "localhost TCP port")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *port < 1 || *port > 65535 {
			return fmt.Errorf("invalid port")
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
		listener, err := net.Listen("tcp4", addr)
		if err != nil {
			return err
		}
		svc := newServer(auth)
		if os.Args[1] == "ui" {
			svc.apiEnabled.Store(false)
		}
		svc.dashboard = newDashboard(svc)
		server := &http.Server{Handler: svc.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
		stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		go func() {
			<-stopCtx.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		}()
		log.Printf("SIWC Provider listening on http://%s (localhost only)", addr)
		if os.Args[1] == "ui" {
			// Fragment-only unlock secret is never sent to the local HTTP server.
			url := fmt.Sprintf("http://%s/ui/#unlock=%s", addr, store.get().AdminKey)
			go func() {
				time.Sleep(220 * time.Millisecond)
				if e := browserOpen(url); e != nil {
					log.Printf("Unable to open the browser automatically; run the ui command from a local desktop session: %v", e)
				}
			}()
		}
		err = server.Serve(listener)
		if err != http.ErrServerClosed {
			return err
		}
	default:
		usage()
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
