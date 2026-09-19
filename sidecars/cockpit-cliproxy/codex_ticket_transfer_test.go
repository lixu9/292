package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func test292State(seed byte) string {
	raw := make([]byte, 217)
	raw[0] = 0x80
	for i := 9; i < len(raw); i++ {
		raw[i] = seed
	}
	return base64.URLEncoding.EncodeToString(raw)
}

func ticketTransferManager(t *testing.T, id string) *codexTicketManager {
	t.Helper()
	a := accountSpec{ID: id, AuthID: id + "-auth", ChatGPTAccountID: "chatgpt-same", Email: "same@example.com"}
	m := &manifest{Accounts: []accountSpec{a}, accountByAuthID: map[string]*accountSpec{a.AuthID: &a}}
	cfg := codexTicketConfig{Enabled: true, ImportOnly: true, Models: []string{"gpt-6-astra"}, TTLSeconds: 3600, RefreshBeforeSeconds: 600, FailClosed: true}
	manager, err := newCodexTicketManager(cfg, m, filepath.Join(t.TempDir(), "cache.json"))
	if err != nil {
		t.Fatal(err)
	}
	m.tickets = manager
	return manager
}

func transferableTicket() codexTicketTransferRecord {
	return codexTicketTransferRecord{ChatGPTAccountID: "chatgpt-same", Email: "same@example.com", Model: "gpt-6-astra", State: test292State(1), CapturedAt: time.Now().Add(-15 * time.Minute).UnixMilli(), ExpiresAt: time.Now().Add(45 * time.Minute).UnixMilli()}
}

func TestCodexTicketTransferAcrossDevicesAndRestart(t *testing.T) {
	source := ticketTransferManager(t, "source-local-id")
	incoming := transferableTicket()
	source.records[ticketKey("source-local-id", incoming.Model)] = codexTicketRecord{State: incoming.State, Source: "harvested", CapturedAt: incoming.CapturedAt, ExpiresAt: incoming.ExpiresAt}
	file := source.exportTickets(map[string]bool{"source-local-id": true})
	if len(file.Tickets) != 1 {
		t.Fatal("missing export")
	}
	encoded, _ := json.Marshal(file)
	for _, forbidden := range []string{"source-local-id", "access_token", "apiKey", "proxyUrl"} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatal("export contains local IDs or credentials")
		}
	}
	destination := ticketTransferManager(t, "different-local-id")
	result, err := destination.importTickets(file.Tickets, map[string]bool{"different-local-id": true})
	if err != nil || result.Imported != 1 {
		t.Fatalf("import failed: %+v %v", result, err)
	}
	got := destination.records[ticketKey("different-local-id", incoming.Model)]
	if got.CapturedAt != incoming.CapturedAt || got.ExpiresAt > incoming.ExpiresAt || got.Source != "imported" {
		t.Fatal("import changed time or source")
	}
	restarted, err := newCodexTicketManager(destination.cfg, destination.manifest, destination.path)
	if err != nil {
		t.Fatal(err)
	}
	restarted.start(context.Background())
	restarted.refresh(context.Background(), map[string]bool{"different-local-id": true})
	if restarted.client != nil || len(restarted.statuses) != 0 {
		t.Fatal("import-only mode attempted harvest")
	}
	headers := http.Header{}
	if err := restarted.Apply("different-local-id-auth", incoming.Model, headers); err != nil || headers.Get("X-Codex-Turn-State") != incoming.State {
		t.Fatal("restarted receiver did not inject imported ticket")
	}
	result, err = restarted.importTickets(file.Tickets, map[string]bool{"different-local-id": true})
	if err != nil || result.Imported != 0 || result.Unchanged != 1 {
		t.Fatal("reimport is not idempotent")
	}
}

func TestCodexTicketImportRejectsInvalidOrUnmatched(t *testing.T) {
	cases := []struct {
		name, reason string
		change       func(*codexTicketTransferRecord)
	}{
		{"312", "invalid_ticket", func(r *codexTicketTransferRecord) { r.State += strings.Repeat("A", 20) }},
		{"non-base64", "invalid_ticket", func(r *codexTicketTransferRecord) { r.State = "gAAAAA" + strings.Repeat("!", 286) }},
		{"newline", "invalid_ticket", func(r *codexTicketTransferRecord) { r.State = "gAAAAA\n" + strings.Repeat("A", 285) }},
		{"missing capture", "invalid_ticket", func(r *codexTicketTransferRecord) { r.CapturedAt = 0 }},
		{"future capture", "invalid_ticket", func(r *codexTicketTransferRecord) { r.CapturedAt = time.Now().Add(time.Hour).UnixMilli() }},
		{"expired", "expired", func(r *codexTicketTransferRecord) { r.ExpiresAt = time.Now().Add(-time.Minute).UnixMilli() }},
		{"other account", "account_not_matched", func(r *codexTicketTransferRecord) { r.ChatGPTAccountID = "different" }},
		{"no account identity", "account_not_matched", func(r *codexTicketTransferRecord) { r.ChatGPTAccountID = "" }},
		{"same workspace different user", "account_not_matched", func(r *codexTicketTransferRecord) { r.Email = "other@example.com" }},
		{"other model", "model_not_enabled", func(r *codexTicketTransferRecord) { r.Model = "unconfigured" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := ticketTransferManager(t, "target")
			r := transferableTicket()
			tc.change(&r)
			result, err := m.importTickets([]codexTicketTransferRecord{r}, map[string]bool{"target": true})
			if err != nil || result.Skipped != 1 || result.Reasons[tc.reason] != 1 || len(m.records) != 0 {
				t.Fatalf("incorrect rejection: %+v %v", result, err)
			}
		})
	}
	m := ticketTransferManager(t, "target")
	result, _ := m.importTickets([]codexTicketTransferRecord{transferableTicket()}, nil)
	if result.Imported != 0 || result.Skipped != 1 {
		t.Fatal("empty scope allowed import")
	}
	if len(m.exportTickets(nil).Tickets) != 0 {
		t.Fatal("empty scope allowed export")
	}
	// Two indistinguishable local accounts must never silently select one.
	m.manifest.Accounts = append(m.manifest.Accounts, m.manifest.Accounts[0])
	result, _ = m.importTickets([]codexTicketTransferRecord{transferableTicket()}, map[string]bool{"target": true})
	if result.Reasons["account_not_matched"] != 1 {
		t.Fatal("ambiguous identity was accepted")
	}
}

func TestCodexTicketImportKeepsNewerAndCapsTTL(t *testing.T) {
	m := ticketTransferManager(t, "target")
	r := transferableTicket()
	r.ExpiresAt = r.CapturedAt + 4*3600000
	scope := map[string]bool{"target": true}
	result, err := m.importTickets([]codexTicketTransferRecord{r}, scope)
	if err != nil || result.Imported != 1 {
		t.Fatal("initial import failed")
	}
	key := ticketKey("target", r.Model)
	saved := m.records[key]
	if saved.ExpiresAt != r.CapturedAt+3600000 {
		t.Fatal("local TTL was exceeded")
	}
	r.State = test292State(2)
	r.CapturedAt -= 1000
	result, _ = m.importTickets([]codexTicketTransferRecord{r}, scope)
	if result.Unchanged != 1 || m.records[key] != saved {
		t.Fatal("older ticket replaced newer")
	}
	r.State = saved.State
	r.CapturedAt += 5000
	result, _ = m.importTickets([]codexTicketTransferRecord{r}, scope)
	if result.Unchanged != 1 || m.records[key] != saved {
		t.Fatal("same ticket renewed its expiry")
	}
	r.State = test292State(3)
	result, _ = m.importTickets([]codexTicketTransferRecord{r}, scope)
	if result.Imported != 1 {
		t.Fatal("newer ticket not imported")
	}
}

func TestCodexTicketImportPersistenceFailureRollsBack(t *testing.T) {
	m := ticketTransferManager(t, "target")
	m.path = t.TempDir() // rename over a directory must fail
	_, err := m.importTickets([]codexTicketTransferRecord{transferableTicket()}, map[string]bool{"target": true})
	if err == nil || len(m.records) != 0 {
		t.Fatal("failed import mutated memory")
	}
	entries, _ := os.ReadDir(m.path)
	if len(entries) != 0 {
		t.Fatal("unexpected persisted ticket")
	}
}

func TestCodexTicketTransferHTTPPermissionsAndLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := ticketTransferManager(t, "target")
	m.manifest.TicketControlKey = "host-private"
	s := &relayServer{manifest: m.manifest}
	spec := &apiKeySpec{Key: "api-key", AccountIDs: []string{"target"}, Enabled: true}
	file := codexTicketFile{Format: codexTicketFileFormat, Tickets: []codexTicketTransferRecord{transferableTicket()}}
	body, _ := json.Marshal(file)
	call := func(path, remote, secret string, payload []byte, authenticated bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
		c.Request.RemoteAddr = remote
		c.Request.Header.Set(codexTicketControlHeader, secret)
		c.Request.Header.Set("X-Forwarded-For", "127.0.0.1")
		if authenticated {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), clientAPIKeyContextKey, spec))
		}
		if strings.HasSuffix(path, "export") {
			s.handleCodexTicketExport(c)
		} else {
			s.handleCodexTicketImport(c)
		}
		return w
	}
	for _, path := range []string{"/import", "/export"} {
		if w := call(path, "127.0.0.1:42", "host-private", body, false); w.Code != 401 {
			t.Fatal("unauthenticated control allowed")
		}
		if w := call(path, "127.0.0.1:42", "", body, true); w.Code != 403 {
			t.Fatal("ordinary API key allowed ticket control")
		}
		if w := call(path, "192.0.2.4:42", "host-private", body, true); w.Code != 403 {
			t.Fatal("remote control allowed via forwarded header")
		}
	}
	if w := call("/import", "127.0.0.1:42", "host-private", append(body, []byte(" {}")...), true); w.Code != 400 {
		t.Fatal("trailing document accepted")
	}
	oversized := append(bytes.Repeat([]byte(" "), codexTicketTransferLimit), body...)
	if w := call("/import", "127.0.0.1:42", "host-private", oversized, true); w.Code != 400 {
		t.Fatal("oversized file accepted")
	}
	w := call("/import", "127.0.0.1:42", "host-private", body, true)
	if w.Code != 200 || strings.Contains(w.Body.String(), file.Tickets[0].State) {
		t.Fatal("import failed or leaked ticket")
	}
	w = call("/export", "[::1]:42", "host-private", nil, true)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private loopback export failed")
	}
	exported := codexTicketFile{}
	if json.Unmarshal(w.Body.Bytes(), &exported) != nil || len(exported.Tickets) != 1 {
		t.Fatal("export payload invalid")
	}
}

// Middleware must not read a private file before authorization and size limits.
func TestCodexTicketTransferThroughRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	m := ticketTransferManager(t, "target")
	m.manifest.TicketControlKey = "host-private"
	spec := &apiKeySpec{Key: "api-key", AccountIDs: []string{"target"}, Enabled: true}
	m.manifest.apiKeyByValue = map[string]*apiKeySpec{"api-key": spec}
	s := &relayServer{manifest: m.manifest, policy: &requestPolicy{manifest: m.manifest}}
	router := s.router()
	tracked := &ticketUnreadBody{}
	req := httptest.NewRequest(http.MethodPost, "/v1/cockpit/tickets/import", tracked)
	req.RemoteAddr = "127.0.0.1:42"
	req.Header.Set("Authorization", "Bearer api-key")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || tracked.read {
		t.Fatalf("private file before authorization: code=%d body_read=%v", w.Code, tracked.read)
	}
	file := codexTicketFile{Format: codexTicketFileFormat, Tickets: []codexTicketTransferRecord{transferableTicket()}}
	body, _ := json.Marshal(file)
	req = httptest.NewRequest(http.MethodPost, "/v1/cockpit/tickets/import", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:42"
	req.Header.Set("Authorization", "Bearer api-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(codexTicketControlHeader, "host-private")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("production route failed: HTTP %d", w.Code)
	}
}

type ticketUnreadBody struct{ read bool }

func (b *ticketUnreadBody) Read(_ []byte) (int, error) { b.read = true; return 0, io.EOF }
func (b *ticketUnreadBody) Close() error               { return nil }
