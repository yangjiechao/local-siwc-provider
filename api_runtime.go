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


 // listenAPIListener checks specific-address collisions before a LAN wildcard bind.
 // macOS can allow 0.0.0.0 and 127.0.0.1 listeners to coexist, routing the
 // loopback connection to the other process even when the wildcard bind succeeds.
func listenAPIListener(port int, lan bool) (net.Listener, error) {
    addr := apiBindAddress(port, lan)
    if lan {
        seen := make(map[string]bool)
        addresses := append([]string{"127.0.0.1"}, localIPAddrs()...)
        for _, host := range addresses {
            if seen[host] { continue }
            seen[host] = true
            probe, err := net.Listen("tcp4", net.JoinHostPort(host, strconv.Itoa(port)))
            if err != nil {
                return nil, fmt.Errorf("API port %d is already in use on %s: %w", port, host, err)
            }
            _ = probe.Close()
        }
    }
    listener, err := net.Listen("tcp4", addr)
    if err != nil { return nil, fmt.Errorf("cannot listen at %s: %w", addr, err) }
    return listener, nil
}

func (a *APIRuntime) startLocked(port int, lan bool) error {
	if err := validateAPIPort(port, a.adminPort); err != nil {
		return err
	}
	if a.listener != nil {
		return nil
	}
	listener, err := listenAPIListener(port, lan)
	if err != nil {
		return err
	}
	a.adoptListenerLocked(listener)
	return nil
}

// adoptListenerLocked starts serving an already bound port. Configure uses this
// when switching ports so a busy new port cannot take down the old listener.
// The caller owns a.mu and transfers ownership of listener to the runtime.
func (a *APIRuntime) adoptListenerLocked(listener net.Listener) {
	srv := &http.Server{Handler: a.app.APIHandler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	a.listener, a.server = listener, srv
	a.app.apiEnabled.Store(true)
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// The dashboard remains available to diagnose an unexpected API failure.
			fmt.Fprintf(os.Stderr, "SIWC API listener failed: %v\n", err)
		}
	}()
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
		// Shutdown already closes the listening socket. Leaving an error here
		// would strand the runtime in the stopped state and prevent Configure
		// from rebinding the previous port. Force-close lingering connections
		// after the grace period and complete the listener transition.
		_ = srv.Close()
		fmt.Fprintf(os.Stderr, "SIWC API graceful shutdown expired, forced close: %v\n", err)
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

	// For different ports, bind the candidate BEFORE stopping the live API.
	// On EADDRINUSE, the original listener and settings remain untouched.
	// A same-port LAN toggle cannot prebind while the old socket exists;
	// that path still has a guarded rollback below.
	var candidate net.Listener
	if wasRunning && port != prior.APIPort {
		var err error
		candidate, err = listenAPIListener(port, lan)
		if err != nil {
			return fmt.Errorf("new API listener unavailable; previous port remains active: %w", err)
		}
		defer func() {
			if candidate != nil {
				_ = candidate.Close()
			}
		}()
	}

	// Save only once the candidate is known to be bindable. Failure must
	// leave the old listener untouched and close any reserved new socket.
	if err := a.store.saveAPISettings(port, lan); err != nil {
		return err
	}
	if !wasRunning {
		return nil
	}
	if err := a.stopLocked(); err != nil {
		_ = a.store.saveAPISettings(prior.APIPort, prior.LANEnabled)
		_ = a.startLocked(prior.APIPort, prior.LANEnabled)
		return err
	}
	if candidate != nil {
		a.adoptListenerLocked(candidate)
		candidate = nil
		return nil
	}
	if err := a.startLocked(port, lan); err != nil {
		_ = a.store.saveAPISettings(prior.APIPort, prior.LANEnabled)
		if restoreErr := a.startLocked(prior.APIPort, prior.LANEnabled); restoreErr != nil {
			_ = a.store.saveAPIEnabled(false)
			return fmt.Errorf("new API listener failed: %w; could not restore previous listener: %v", err, restoreErr)
		}
		return fmt.Errorf("new API listener failed, previous port restored: %w", err)
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
