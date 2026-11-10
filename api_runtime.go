package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const defaultAPIPort = 8087
const defaultAdminPort = 8787

// APIRuntime owns the API listener independently from the loopback-only dashboard.
// Restarting the API or changing its port never exposes the dashboard to the LAN.
type APIRuntime struct {
	mu        sync.Mutex
	app       *Server
	store     *Store
	adminPort int
	server    *http.Server
	listener  net.Listener
}

func newAPIRuntime(app *Server, adminPort int) *APIRuntime {
	return &APIRuntime{app: app, store: app.auth.store, adminPort: adminPort}
}

func validateAPIPort(port, adminPort int) error {
	if port < 1 || port > 65535 {
		return errors.New("API port must be between 1 and 65535")
	}
	if port == adminPort {
		return errors.New("API port cannot match the dashboard port")
	}
	return nil
}

func apiBindAddress(port int, lan bool) string {
	host := "127.0.0.1"
	if lan {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (a *APIRuntime) startLocked(port int, lan bool) error {
	if err := validateAPIPort(port, a.adminPort); err != nil {
		return err
	}
	if a.listener != nil {
		return nil
	}
	addr := apiBindAddress(port, lan)
	listener, err := net.Listen("tcp4", addr)
	if err != nil {
		return fmt.Errorf("cannot listen at %s: %w", addr, err)
	}
	srv := &http.Server{Handler: a.app.APIHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	a.listener, a.server = listener, srv
	a.app.apiEnabled.Store(true)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The dashboard remains available to diagnose an unexpected API failure.
			fmt.Fprintf(os.Stderr, "SIWC API listener failed: %v\n", err)
		}
	}()
	return nil
}

func (a *APIRuntime) stopLocked() error {
	if a.listener == nil {
		a.app.apiEnabled.Store(false)
		return nil
	}
	a.app.apiEnabled.Store(false)
	srv := a.server
	a.listener, a.server = nil, nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("stopping API: %w", err)
	}
	return nil
}

func (a *APIRuntime) Running() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.listener != nil
}

func (a *APIRuntime) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.store.get()
	if err := a.startLocked(s.APIPort, s.LANEnabled); err != nil {
		return err
	}
	return a.store.saveAPIEnabled(true)
}

func (a *APIRuntime) Stop() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.stopLocked(); err != nil {
		return err
	}
	return a.store.saveAPIEnabled(false)
}

// Close is used on process termination or restart and deliberately preserves
// the desired enabled state, so a running API is restored after a restart.
func (a *APIRuntime) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stopLocked()
}

func (a *APIRuntime) Restart() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.store.get()
	if err := a.stopLocked(); err != nil {
		return err
	}
	if err := a.startLocked(s.APIPort, s.LANEnabled); err != nil {
		_ = a.store.saveAPIEnabled(false)
		return err
	}
	return a.store.saveAPIEnabled(true)
}

// Configure applies the new port and LAN setting atomically as far as practical.
// On bind failures the old config/listener are restored.
func (a *APIRuntime) Configure(port int, lan bool) error {
	if err := validateAPIPort(port, a.adminPort); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	prior := a.store.get()
	if prior.APIPort == port && prior.LANEnabled == lan {
		return nil
	}
	wasRunning := a.listener != nil
	if wasRunning {
		if err := a.stopLocked(); err != nil {
			return err
		}
	}
	if err := a.store.saveAPISettings(port, lan); err != nil {
		if wasRunning {
			_ = a.startLocked(prior.APIPort, prior.LANEnabled)
		}
		return err
	}
	if wasRunning {
		if err := a.startLocked(port, lan); err != nil {
			_ = a.store.saveAPISettings(prior.APIPort, prior.LANEnabled)
			if restoreErr := a.startLocked(prior.APIPort, prior.LANEnabled); restoreErr != nil {
				_ = a.store.saveAPIEnabled(false)
				return fmt.Errorf("new API listener failed: %w; could not restore previous listener: %v", err, restoreErr)
			}
			return fmt.Errorf("new API listener failed, previous port restored: %w", err)
		}
	}
	return nil
}

func localHostLabel() string {
	if runtime.GOOS == "darwin" {
		if b, err := exec.Command("scutil", "--get", "LocalHostName").Output(); err == nil {
			if s := strings.TrimSpace(string(b)); s != "" {
				return strings.TrimSuffix(s, ".local")
			}
		}
	}
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSpace(name), ".local")
}

func localIPAddrs() []string {
	interfaces, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var hosts []string
	for _, addr := range interfaces {
		cidr, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := cidr.IP.To4()
		if ip != nil && ip.IsPrivate() && !ip.IsLoopback() {
			hosts = append(hosts, ip.String())
		}
	}
	return hosts
}

// For LAN HTTP requests, accept only local/Bonjour/private-IP targets. Auth is
// still mandatory and browser Origin headers are forbidden; users must keep their
// API key private because ordinary HTTP does not encrypt LAN traffic.
func allowedAPIHost(hostPort string) bool {
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		host = hostPort
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	name := strings.ToLower(localHostLabel())
	return name != "" && (host == name || host == name+".local")
}

func privateLANPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}
