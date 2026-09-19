// Package codexticket connects the host-owned ticket cache to Codex executors.
package codexticket

import (
	"net/http"
	"sync"
)

const Header = "X-Codex-Turn-State"

type Provider interface {
	Apply(authID, model string, headers http.Header) error
}

var bridge struct {
	sync.RWMutex
	provider Provider
}

// SetProvider is called once by the embedding host, before serving requests.
// Keeping the hook outside configuration preserves it across config reloads.
func SetProvider(provider Provider) {
	bridge.Lock()
	bridge.provider = provider
	bridge.Unlock()
}

func Active() bool {
	bridge.RLock()
	defer bridge.RUnlock()
	return bridge.provider != nil
}

func Apply(authID, model string, headers http.Header) error {
	bridge.RLock()
	provider := bridge.provider
	bridge.RUnlock()
	if provider == nil {
		return nil
	}
	return provider.Apply(authID, model, headers)
}
