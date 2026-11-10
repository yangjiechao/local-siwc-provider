package main

import (
	"context"
	"errors"
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

type lifecycleAction string

const (
	lifecycleRestart lifecycleAction = "restart"
	lifecycleQuit    lifecycleAction = "quit"
)

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: siwc-provider <ui|login|status|key|serve|logout> [options]\nCommands:\n  ui      Open local dashboard (default dashboard port 8787, Agent API port 8087)\n  login   Continue with ChatGPT (OAuth PKCE)\n  status  Show authorization status\n  key     Display the Agent API key (keep private)\n  serve   Start dashboard + Agent API; --port overrides Agent API port\n  logout  Revoke current renewable session")
}

func runServers(auth *Auth, mode string, adminPort int, cliAPIPort int) (bool, error) {
	if adminPort < 1 || adminPort > 65535 {
		return false, fmt.Errorf("invalid dashboard port %d", adminPort)
	}
	store := auth.store
	service := newServer(auth)
	service.apiEnabled.Store(false)
	api := newAPIRuntime(service, adminPort)
	done := make(chan lifecycleAction, 1)
	dash := newDashboard(service)
	dash.runtime = api
	dash.adminPort = adminPort
	dash.control = done
	service.dashboard = dash

	adminAddr := net.JoinHostPort("127.0.0.1", strconv.Itoa(adminPort))
	adminListener, err := net.Listen("tcp4", adminAddr)
	if err != nil {
		return false, fmt.Errorf("dashboard port %d unavailable: %w", adminPort, err)
	}
	defer adminListener.Close()
	adminSrv := &http.Server{Handler: service.AdminHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}

	if mode == "serve" && cliAPIPort > 0 {
		current := store.get()
		if err = api.Configure(cliAPIPort, current.LANEnabled); err != nil {
			return false, err
		}
	}
	if mode == "serve" || store.get().APIEnabled {
		if err = api.Start(); err != nil {
			// Leave the dashboard online to let the user correct occupied ports.
			log.Printf("Agent API could not start; open the dashboard to fix it: %v", err)
		}
	}
	go func() {
		if err := adminSrv.Serve(adminListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("dashboard listener stopped unexpectedly: %v", err)
		}
	}()
	log.Printf("Local SIWC dashboard listening at http://%s/ui/", adminAddr)
	state := store.get()
	log.Printf("Agent API: port %d (LAN: %t, running: %t)", state.APIPort, state.LANEnabled, api.Running())
	if mode == "ui" {
		// URL fragment does not travel to HTTP server or log files.
		link := fmt.Sprintf("http://%s/ui/#unlock=%s", adminAddr, state.AdminKey)
		go func() {
			time.Sleep(220 * time.Millisecond)
			if e := browserOpen(link); e != nil {
				log.Printf("Unable to open browser automatically: %v", e)
			}
		}()
	}
	stopCtx, cancelStop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancelStop()
	var action lifecycleAction
	select {
	case <-stopCtx.Done():
		action = lifecycleQuit
	case action = <-done:
	}
	if err := api.Close(); err != nil {
		log.Printf("error closing Agent API: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := adminSrv.Shutdown(ctx); err != nil {
		_ = adminSrv.Close()
	}
	return action == lifecycleRestart, nil
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
		fmt.Println("Agent API key, keep it private:")
		fmt.Println(store.get().LocalKey)
	case "logout":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := auth.logout(ctx); err != nil {
			return err
		}
		fmt.Println("Session revoked and credentials cleared")
	case "ui", "serve":
		flags := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
		adminPort := flags.Int("dashboard-port", defaultAdminPort, "loopback-only web dashboard port")
		var cliPort *int
		if os.Args[1] == "serve" {
			cliPort = flags.Int("port", 0, "Agent API port (default: saved configuration / 8087)")
		} else {
			cliPort = flags.Int("port", 0, "deprecated: use --dashboard-port for dashboard; API port is configured in the UI")
		}
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if *cliPort < 0 || *cliPort > 65535 {
			return fmt.Errorf("invalid API port")
		}
		// Keep old `ui --port` launchers working for dashboard port selection.
		if os.Args[1] == "ui" && *cliPort != 0 {
			*adminPort = *cliPort
			*cliPort = 0
		}
		for {
			restart, err := runServers(auth, os.Args[1], *adminPort, *cliPort)
			if err != nil {
				return err
			}
			if !restart {
				return nil
			}
			// After a restart, read saved settings instead of reapplying --port.
			*cliPort = 0
			auth = newAuth(store)
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
