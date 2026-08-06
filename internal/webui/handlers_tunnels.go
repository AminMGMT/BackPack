package webui

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/backpack/backpack/internal/manage"
)

var (
	errTunnelNameRequired = errors.New("tunnel name is required")
	errTunnelNotFound     = errors.New("tunnel not found")
)

// handleTunnelRestart restarts one configured tunnel. It is registered behind
// requireAuth rather than requireReadAuth: the remote monitoring token may
// inspect a server, but it must never be able to change one.
func (s *server) handleTunnelRestart(w http.ResponseWriter, r *http.Request) {
	handleTunnelRestartWith(w, r, manage.List, func(service string) error {
		return restartAndWait(service, manage.RestartService, manage.WaitServiceActive)
	})
}

// restartAndWait does not report success merely because systemd accepted the
// restart job. A service whose process immediately fails must be shown as a
// failed restart in the panel, not as a brief success followed by a red card.
func restartAndWait(
	service string,
	restart func(string) error,
	waitActive func(string, time.Duration) bool,
) error {
	if err := restart(service); err != nil {
		return err
	}
	if !waitActive(service, 10*time.Second) {
		return errors.New("service did not become active")
	}
	return nil
}

// handleTunnelRestartWith keeps the systemd operation injectable for tests.
// The service name comes from manage.List, not from request input, so an
// operator can only restart a tunnel Backpack actually manages.
func handleTunnelRestartWith(
	w http.ResponseWriter,
	r *http.Request,
	list func() []manage.Tunnel,
	restart func(string) error,
) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read request", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	service, err := restartServiceFor(name, list())
	if err != nil {
		switch {
		case errors.Is(err, errTunnelNameRequired):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, errTunnelNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	if err := restart(service); err != nil {
		http.Error(w, "could not restart tunnel", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "restarted", "name": name})
}

func restartServiceFor(name string, tunnels []manage.Tunnel) (string, error) {
	if name == "" {
		return "", errTunnelNameRequired
	}
	for _, tunnel := range tunnels {
		if tunnel.Name == name {
			if tunnel.Service == "" {
				return "", fmt.Errorf("%w: %s has no service", errTunnelNotFound, name)
			}
			return tunnel.Service, nil
		}
	}
	return "", fmt.Errorf("%w: %s", errTunnelNotFound, name)
}
