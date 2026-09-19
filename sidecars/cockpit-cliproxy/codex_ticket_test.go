package main

import (
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCodexTicketHarvestUsesFreshHTTP1Connections(t *testing.T) {
	type observed struct {
		protocol int
		remote   string
	}
	requests := make(chan observed, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- observed{r.ProtoMajor, r.RemoteAddr}
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport, err := newCodexTicketTransport(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	transport.TLSClientConfig = &tls.Config{RootCAs: pool}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for i := 0; i < 2; i++ {
		resp, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	first, second := <-requests, <-requests
	if first.protocol != 1 || second.protocol != 1 {
		t.Fatal("harvest negotiated HTTP/2")
	}
	if first.remote == second.remote {
		t.Fatal("harvest reused its previous connection")
	}
}

func TestCodexTicketRecordRequiresVerified292(t *testing.T) {
	now := time.Now()
	valid := codexTicketRecord{State: test292State(1), CapturedAt: time.Now().Add(-time.Minute).UnixMilli(), ExpiresAt: now.Add(time.Minute).UnixMilli()}
	if !valid.valid(now) {
		t.Fatal("expected a 292-length gAAAAA ticket to be valid")
	}
	valid.State = "312"
	if valid.valid(now) {
		t.Fatal("accepted a non-292 ticket")
	}
}

func TestCodexTicketApplyIsAccountAndModelScoped(t *testing.T) {
	m := &manifest{accountByAuthID: map[string]*accountSpec{"auth-1": {ID: "account-1", AuthID: "auth-1"}}}
	ticket, err := newCodexTicketManager(codexTicketConfig{Enabled: true, ProxyURL: "http://127.0.0.1:1", Models: []string{"gpt-6-astra"}, TTLSeconds: 3600, RefreshBeforeSeconds: 600, FailClosed: true}, m, t.TempDir()+"/tickets.json")
	if err != nil {
		t.Fatal(err)
	}
	ticket.records[ticketKey("account-1", "gpt-6-astra")] = codexTicketRecord{State: test292State(1), CapturedAt: time.Now().Add(-time.Minute).UnixMilli(), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
	h := http.Header{}
	if err := ticket.Apply("auth-1", "gpt-6-astra", h); err != nil {
		t.Fatal(err)
	}
	if got := h.Get("X-Codex-Turn-State"); len(got) != 292 {
		t.Fatalf("injected ticket length = %d", len(got))
	}
	ticket.cfg.Models = append(ticket.cfg.Models, "gpt-5.6-sol")
	if err := ticket.Apply("auth-1", "gpt-5.6-sol", http.Header{}); err == nil {
		t.Fatal("expected missing configured model ticket to fail closed")
	}
}
