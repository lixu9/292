package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/codexticket"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"golang.org/x/net/proxy"
)

const codexTicketClientVersion = "0.155.1"

type codexTicketConfig struct {
	Enabled              bool     `json:"enabled"`
	ImportOnly           bool     `json:"importOnly"`
	ProxyURL             string   `json:"proxyUrl"`
	Models               []string `json:"models"`
	TTLSeconds           int      `json:"ttlSeconds"`
	RefreshBeforeSeconds int      `json:"refreshBeforeSeconds"`
	FailClosed           bool     `json:"failClosed"`
}

type codexTicketRecord struct {
	State      string `json:"state"`
	Source     string `json:"source,omitempty"`
	CapturedAt int64  `json:"capturedAt"`
	ExpiresAt  int64  `json:"expiresAt"`
}

func (r codexTicketRecord) valid(now time.Time) bool {
	return validCodexTicketState(r.State) && r.CapturedAt > 0 && r.CapturedAt <= now.Add(30*time.Second).UnixMilli() && r.ExpiresAt > r.CapturedAt && now.UnixMilli() < r.ExpiresAt
}

// Status deliberately excludes the ticket and OAuth credentials.
type codexTicketStatus struct {
	AccountID      string `json:"accountId"`
	Model          string `json:"model"`
	Ready          bool   `json:"ready"`
	Source         string `json:"source"`
	Refreshing     bool   `json:"refreshing"`
	CapturedAt     int64  `json:"capturedAt"`
	ExpiresAt      int64  `json:"expiresAt"`
	LastAttemptAt  int64  `json:"lastAttemptAt"`
	LastHTTPStatus int    `json:"lastHttpStatus"`
	LastLength     int    `json:"lastLength"`
	LastError      string `json:"lastError"`
	Attempts       int    `json:"attempts"`
	nextAttemptAt  int64
	failures       int
}

type codexTicketManager struct {
	mu               sync.Mutex
	cfg              codexTicketConfig
	manifest         *manifest
	path             string
	client           *http.Client
	endpoint         string
	records          map[string]codexTicketRecord
	statuses         map[string]codexTicketStatus
	wake             chan map[string]bool
	persistenceError string
}

func newCodexTicketManager(cfg codexTicketConfig, m *manifest, path string) (*codexTicketManager, error) {
	t := &codexTicketManager{cfg: cfg, manifest: m, path: path, endpoint: "https://chatgpt.com/backend-api/codex/responses", records: map[string]codexTicketRecord{}, statuses: map[string]codexTicketStatus{}, wake: make(chan map[string]bool, 1)}
	if !cfg.Enabled {
		return t, nil
	}
	if cfg.TTLSeconds < 600 || cfg.TTLSeconds > 86400 || cfg.RefreshBeforeSeconds < 30 || cfg.RefreshBeforeSeconds >= cfg.TTLSeconds || len(cfg.Models) == 0 {
		return nil, errors.New("invalid 292 TTL, refresh window or models")
	}
	t.cfg.Models = normalizeStringList(cfg.Models)
	if !cfg.ImportOnly {
		u, err := url.Parse(strings.TrimSpace(cfg.ProxyURL))
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("292 harvest proxy must be an HTTP(S) or SOCKS5(h) URL")
		}
		transport, err := newCodexTicketTransport(u)
		if err != nil {
			return nil, errors.New("invalid 292 harvest proxy")
		}
		t.client = &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if b, err := os.ReadFile(path); err == nil {
		if json.Unmarshal(b, &t.records) != nil {
			t.persistenceError = "无法读取票据缓存，请重新导入或取票"
			t.records = map[string]codexTicketRecord{}
		}
	} else if !os.IsNotExist(err) {
		t.persistenceError = "无法读取票据缓存，请重新导入或取票"
	}
	if t.records == nil {
		t.records = map[string]codexTicketRecord{}
	}
	for key, r := range t.records {
		if !r.valid(time.Now()) {
			delete(t.records, key)
		}
	}
	return t, nil
}

// Harvest follows Sub2API's HTTP/1.1 policy. A fresh transport avoids inheriting
// HTTP/2 ALPN settings from http.DefaultTransport.Clone().
func newCodexTicketTransport(proxyURL *url.URL) (*http.Transport, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   false,
		TLSNextProto:        make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	if proxyURL == nil {
		return transport, nil
	}
	if proxyURL.Scheme == "socks5" || proxyURL.Scheme == "socks5h" {
		proxyDialer, err := proxy.FromURL(proxyURL, dialer)
		if err != nil {
			return nil, err
		}
		contextDialer, ok := proxyDialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("harvest proxy does not support cancellation")
		}
		transport.DialContext = contextDialer.DialContext
	} else {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	return transport, nil
}

func ticketKey(accountID, model string) string { return accountID + "\x00" + model }

func (t *codexTicketManager) account(authID string) *accountSpec {
	if t == nil || t.manifest == nil {
		return nil
	}
	a := t.manifest.accountByAuthID[strings.ToLower(authID)]
	if a == nil || a.AuthKind == "api_key" || (a.Provider != "" && a.Provider != "codex") {
		return nil
	}
	return a
}

func (t *codexTicketManager) gated(model string) bool {
	if t == nil || !t.cfg.Enabled {
		return false
	}
	for _, m := range t.cfg.Models {
		if m == model {
			return true
		}
	}
	return false
}

type codexTicketUnavailable struct{}

func (codexTicketUnavailable) Error() string {
	return "292 ticket unavailable for this account/model; waiting for harvest"
}
func (codexTicketUnavailable) StatusCode() int { return http.StatusServiceUnavailable }

func (t *codexTicketManager) Apply(authID, model string, h http.Header) error {
	a := t.account(authID)
	if a == nil || !t.gated(model) {
		return nil
	}
	// Never echo an unverified client ticket across accounts, even in permissive mode.
	for key := range h {
		if strings.EqualFold(key, codexticket.Header) {
			delete(h, key)
		}
	}
	t.mu.Lock()
	r := t.records[ticketKey(a.ID, model)]
	t.mu.Unlock()
	if r.valid(time.Now()) {
		h.Set(codexticket.Header, r.State)
		return nil
	}
	if t.cfg.FailClosed {
		return codexTicketUnavailable{}
	}
	return nil
}

func (t *codexTicketManager) snapshot(allowed map[string]bool) []codexTicketStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := []codexTicketStatus{}
	for _, a := range t.manifest.Accounts {
		if !allowed[a.ID] || t.account(a.AuthID) == nil {
			continue
		}
		for _, model := range t.cfg.Models {
			key := ticketKey(a.ID, model)
			s := t.statuses[key]
			s.AccountID, s.Model = a.ID, model
			r := t.records[key]
			s.Ready, s.CapturedAt, s.ExpiresAt = r.valid(time.Now()), r.CapturedAt, r.ExpiresAt
			s.Source = r.Source
			result = append(result, s)
		}
	}
	return result
}

func (t *codexTicketManager) start(ctx context.Context) {
	if !t.cfg.Enabled || t.cfg.ImportOnly {
		return
	}
	go func() {
		t.refresh(ctx, nil)
		timer := time.NewTicker(6 * time.Second)
		defer timer.Stop()
		defer t.client.CloseIdleConnections()
		for {
			select {
			case <-ctx.Done():
				return
			case ids := <-t.wake:
				t.refresh(ctx, ids)
			case <-timer.C:
				t.refresh(ctx, nil)
			}
		}
	}()
}

func (t *codexTicketManager) refresh(ctx context.Context, force map[string]bool) {
	if !t.cfg.Enabled || t.cfg.ImportOnly {
		return
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, a := range t.manifest.Accounts {
		if ctx.Err() != nil {
			break
		}
		if t.account(a.AuthID) == nil {
			continue
		}
		for _, model := range t.cfg.Models {
			key := ticketKey(a.ID, model)
			t.mu.Lock()
			r, s := t.records[key], t.statuses[key]
			now := time.Now()
			due := !r.valid(now) || r.ExpiresAt-now.UnixMilli() <= int64(t.cfg.RefreshBeforeSeconds)*1000
			should := !s.Refreshing && ((due && now.UnixMilli() >= s.nextAttemptAt) || force[a.ID])
			if should {
				s.Refreshing = true
				t.statuses[key] = s
			}
			t.mu.Unlock()
			if !should {
				continue
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			wg.Add(1)
			go func(a accountSpec, model string) { defer wg.Done(); defer func() { <-sem }(); t.probe(ctx, a, model) }(a, model)
		}
	}
	wg.Wait()
}

func (t *codexTicketManager) probe(ctx context.Context, a accountSpec, model string) {
	key := ticketKey(a.ID, model)
	t.mu.Lock()
	s := t.statuses[key]
	s.Attempts++
	s.LastAttemptAt = time.Now().UnixMilli()
	t.statuses[key] = s
	t.mu.Unlock()
	state, code, probeErr := t.requestTicket(ctx, a, model)
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	s = t.statuses[key]
	s.Refreshing = false
	s.LastHTTPStatus = code
	s.LastLength = len(state)
	s.LastError = ""
	if probeErr == nil && code == 200 && validCodexTicketState(state) {
		t.records[key] = codexTicketRecord{State: state, Source: "harvested", CapturedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Duration(t.cfg.TTLSeconds) * time.Second).UnixMilli()}
		s.failures = 0
		s.nextAttemptAt = 0
		if err := t.persistLocked(); err != nil {
			t.persistenceError = "票据已取得，但写入缓存失败；重启后需要重新取票"
		} else {
			t.persistenceError = ""
		}
	} else {
		s.failures++
		delay := min(120, 6<<min(s.failures-1, 5))
		s.nextAttemptAt = now.Add(time.Duration(delay) * time.Second).UnixMilli()
		if probeErr != nil {
			s.LastError = probeErr.Error()
		} else {
			s.LastError = fmt.Sprintf("未取得 292 票据（HTTP %d，长度 %d）", code, len(state))
		}
	}
	t.statuses[key] = s
}

func (t *codexTicketManager) requestTicket(ctx context.Context, a accountSpec, model string) (string, int, error) {
	auth, ok := t.manifest.authManager.GetByID(a.AuthID)
	if !ok || auth.Disabled {
		return "", 0, errors.New("账号不可用")
	}
	token, _ := auth.Metadata["access_token"].(string)
	if token == "" {
		return "", 0, errors.New("账号缺少访问凭证")
	}
	body, _ := json.Marshal(map[string]any{"model": model, "store": false, "stream": true, "instructions": "Reply with exactly: pong", "input": []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "input_text", "text": "ping"}}}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", 0, errors.New("创建取票请求失败")
	}
	req.Close = true
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-ID", codexAuthChatGPTAccountID(auth))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Session_id", uuid.NewString())
	req.Header.Set("User-Agent", "codex-tui/"+codexTicketClientVersion+" (Ubuntu 22.4.0; x86_64) xterm-256color")
	req.Header.Set("Version", codexTicketClientVersion)
	req.Header.Set("Originator", "codex-tui")
	resp, err := t.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", 0, errors.New("取票已停止")
		}
		return "", 0, errors.New("取票连接失败或超时，请检查专用代理和节点")
	}
	defer resp.Body.Close()
	return strings.TrimSpace(resp.Header.Get(codexticket.Header)), resp.StatusCode, nil
}

func (t *codexTicketManager) persistLocked() error {
	if t.path == "" {
		return nil
	}
	b, err := json.Marshal(t.records)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(t.path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(t.path), ".292-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), t.path)
}

func (s *relayServer) handleCodexTickets(c *gin.Context) {
	spec, ok := s.requireAPIKey(c)
	if !ok {
		return
	}
	if s.manifest == nil || s.manifest.tickets == nil {
		c.JSON(200, gin.H{"enabled": false, "tickets": []any{}})
		return
	}
	t := s.manifest.tickets
	allowed := map[string]bool{}
	for _, id := range spec.AccountIDs {
		allowed[id] = true
	}
	if c.Request.Method == http.MethodPost && t.cfg.Enabled && !t.cfg.ImportOnly {
		select {
		case t.wake <- allowed:
		default:
		}
	}
	t.mu.Lock()
	warning := t.persistenceError
	t.mu.Unlock()
	c.JSON(200, gin.H{"enabled": t.cfg.Enabled, "importOnly": t.cfg.ImportOnly, "tickets": t.snapshot(allowed), "warning": warning})
}

type codexTicketSelector struct {
	tickets  *codexTicketManager
	fallback coreauth.Selector
}

func (s *codexTicketSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	actual := model
	if source := s.tickets.manifest.aliasToSource[model]; source != "" {
		actual = source
	}
	actual = thinking.ParseSuffix(actual).ModelName
	if provider != "codex" || !s.tickets.gated(actual) || !s.tickets.cfg.FailClosed {
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}
	filtered := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth != nil && s.tickets.Apply(auth.ID, actual, http.Header{}) == nil {
			filtered = append(filtered, auth)
		}
	}
	if len(filtered) == 0 && len(auths) > 0 {
		return nil, codexTicketUnavailable{}
	}
	return s.fallback.Pick(ctx, provider, model, opts, filtered)
}
func (s *codexTicketSelector) OnResult(r coreauth.Result) { forwardAuthSelectionResult(s.fallback, r) }
func (s *codexTicketSelector) Stop() {
	if v, ok := s.fallback.(coreauth.StoppableSelector); ok {
		v.Stop()
	}
}

func (s *codexTicketSelector) ReportAuthSelectionFailure(ctx context.Context, provider, model string, auths []*coreauth.Auth, err error) error {
	if v, ok := s.fallback.(coreauth.AuthSelectionFailureReporter); ok {
		return v.ReportAuthSelectionFailure(ctx, provider, model, auths, err)
	}
	return nil
}
