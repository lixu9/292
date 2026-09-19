package main

import (
	"net/http"
	"testing"
	"time"
)

func TestCodexTicketRecordRequiresVerified292(t *testing.T) {
	now := time.Now()
	valid := codexTicketRecord{State: "gAAAAA" + string(make([]byte, 286)), ExpiresAt: now.Add(time.Minute).UnixMilli()}
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
	ticket.records[ticketKey("account-1", "gpt-6-astra")] = codexTicketRecord{State: "gAAAAA" + string(make([]byte, 286)), ExpiresAt: time.Now().Add(time.Minute).UnixMilli()}
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
