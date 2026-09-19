package main

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const codexTicketFileFormat = "cockpit-codex-292/v1"
const codexTicketTransferLimit = 1024 * 1024
const codexTicketTransferMaxRecords = 1024
const codexTicketControlHeader = "X-Cockpit-Ticket-Control"

// The portable file contains ticket secrets, but no OAuth tokens or API keys.
// Local Cockpit IDs are deliberately excluded: they can differ across devices.
type codexTicketTransferRecord struct {
	ChatGPTAccountID string `json:"chatgptAccountId"`
	Email            string `json:"email,omitempty"`
	Model            string `json:"model"`
	State            string `json:"state"`
	CapturedAt       int64  `json:"capturedAt"`
	ExpiresAt        int64  `json:"expiresAt"`
}

type codexTicketFile struct {
	Format  string                      `json:"format"`
	Tickets []codexTicketTransferRecord `json:"tickets"`
}

type codexTicketImportResult struct {
	Imported  int            `json:"imported"`
	Unchanged int            `json:"unchanged"`
	Skipped   int            `json:"skipped"`
	Reasons   map[string]int `json:"reasons"`
}

// This validates the local envelope, not the upstream signature or usability.
func validCodexTicketState(state string) bool {
	if len(state) != 292 || !strings.HasPrefix(state, "gAAAAA") || strings.ContainsAny(state, "\r\n\t ") {
		return false
	}
	_, err := base64.URLEncoding.Strict().DecodeString(state)
	return err == nil
}

func (t *codexTicketManager) exportTickets(allowed map[string]bool) codexTicketFile {
	result := codexTicketFile{Format: codexTicketFileFormat, Tickets: []codexTicketTransferRecord{}}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, a := range t.manifest.Accounts {
		if !allowed[a.ID] || t.account(a.AuthID) == nil || strings.TrimSpace(a.ChatGPTAccountID) == "" {
			continue
		}
		for _, model := range t.cfg.Models {
			r := t.records[ticketKey(a.ID, model)]
			if !r.valid(time.Now()) {
				continue
			}
			result.Tickets = append(result.Tickets, codexTicketTransferRecord{
				ChatGPTAccountID: a.ChatGPTAccountID, Email: a.Email, Model: model,
				State: r.State, CapturedAt: r.CapturedAt, ExpiresAt: r.ExpiresAt,
			})
		}
	}
	return result
}

func (t *codexTicketManager) importTickets(records []codexTicketTransferRecord, allowed map[string]bool) (codexTicketImportResult, error) {
	result := codexTicketImportResult{Reasons: map[string]int{}}
	if t == nil || t.manifest == nil || !t.cfg.Enabled {
		return result, errors.New("请先启用 292 票据功能并保存设置")
	}
	skip := func(reason string) { result.Skipped++; result.Reasons[reason]++ }
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	original := t.records
	next := maps.Clone(original)
	if next == nil {
		next = map[string]codexTicketRecord{}
	}
	for _, item := range records {
		if !t.gated(item.Model) {
			skip("model_not_enabled")
			continue
		}
		if !validCodexTicketState(item.State) || item.CapturedAt <= 0 || item.CapturedAt > now.Add(30*time.Second).UnixMilli() || item.ExpiresAt <= item.CapturedAt || item.ExpiresAt-item.CapturedAt > 86400000 {
			skip("invalid_ticket")
			continue
		}
		// Import cannot reset the capture time or extend either device's TTL.
		expiresAt := min(item.ExpiresAt, item.CapturedAt+int64(t.cfg.TTLSeconds)*1000)
		if expiresAt <= now.UnixMilli() {
			skip("expired")
			continue
		}
		identity := strings.TrimSpace(item.ChatGPTAccountID)
		var account *accountSpec
		matches := 0
		for i := range t.manifest.Accounts {
			a := &t.manifest.Accounts[i]
			if identity == "" || !allowed[a.ID] || t.account(a.AuthID) == nil || !strings.EqualFold(identity, strings.TrimSpace(a.ChatGPTAccountID)) {
				continue
			}
			if item.Email != "" && !strings.EqualFold(strings.TrimSpace(item.Email), strings.TrimSpace(a.Email)) {
				continue
			}
			account = a
			matches++
		}
		if matches != 1 {
			skip("account_not_matched")
			continue
		}
		key := ticketKey(account.ID, item.Model)
		current := next[key]
		// An older copy (or the same opaque ticket with edited dates) cannot
		// overwrite a newer valid local ticket or renew its cache lifetime.
		if current.valid(now) && (current.CapturedAt >= item.CapturedAt || current.State == item.State) {
			result.Unchanged++
			continue
		}
		next[key] = codexTicketRecord{State: item.State, Source: "imported", CapturedAt: item.CapturedAt, ExpiresAt: expiresAt}
		result.Imported++
	}
	if result.Imported == 0 {
		return result, nil
	}
	t.records = next
	if err := t.persistLocked(); err != nil {
		t.records = original
		return codexTicketImportResult{}, errors.New("写入票据缓存失败，未应用本次导入")
	}
	t.persistenceError = ""
	return result, nil
}

// Exporting upstream secrets needs the host's private control key as well as
// API account scope. Do not trust X-Forwarded-For for the loopback check.
func (s *relayServer) ticketTransferScope(c *gin.Context) (map[string]bool, bool) {
	spec, _ := c.Request.Context().Value(clientAPIKeyContextKey).(*apiKeySpec)
	if spec == nil {
		writeCodexTicketError(c, http.StatusUnauthorized, "missing or invalid API key", "invalid_api_key")
		return nil, false
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || !ip.IsLoopback() || s.manifest == nil || s.manifest.TicketControlKey == "" || subtle.ConstantTimeCompare([]byte(c.GetHeader(codexTicketControlHeader)), []byte(s.manifest.TicketControlKey)) != 1 {
		writeCodexTicketError(c, http.StatusForbidden, "请通过本机 Cockpit 导入或导出票据", "ticket_control_forbidden")
		return nil, false
	}
	if s.manifest.tickets == nil || !s.manifest.tickets.cfg.Enabled {
		writeCodexTicketError(c, http.StatusConflict, "请先启用 292 票据功能并保存设置", "tickets_disabled")
		return nil, false
	}
	allowed := map[string]bool{}
	for _, id := range spec.AccountIDs {
		allowed[id] = true
	}
	c.Header("Cache-Control", "no-store")
	return allowed, true
}

func (s *relayServer) handleCodexTicketExport(c *gin.Context) {
	allowed, ok := s.ticketTransferScope(c)
	if !ok {
		return
	}
	file := s.manifest.tickets.exportTickets(allowed)
	if len(file.Tickets) == 0 {
		writeCodexTicketError(c, http.StatusConflict, "当前账号池没有可导出的未过期 292 票据", "no_tickets")
		return
	}
	if len(file.Tickets) > codexTicketTransferMaxRecords {
		writeCodexTicketError(c, http.StatusBadRequest, "单次最多导出 1024 张票据，请缩小账号池", "too_many_tickets")
		return
	}
	c.JSON(http.StatusOK, file)
}

func (s *relayServer) handleCodexTicketImport(c *gin.Context) {
	allowed, ok := s.ticketTransferScope(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, codexTicketTransferLimit)
	var file codexTicketFile
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&file); err != nil || file.Format != codexTicketFileFormat || len(file.Tickets) == 0 || len(file.Tickets) > codexTicketTransferMaxRecords {
		writeCodexTicketError(c, http.StatusBadRequest, "请选择 Cockpit 导出的 292 票据文件（最多 1 MB / 1024 张）", "invalid_ticket_file")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeCodexTicketError(c, http.StatusBadRequest, "票据文件包含多余内容", "invalid_ticket_file")
		return
	}
	result, err := s.manifest.tickets.importTickets(file.Tickets, allowed)
	if err != nil {
		writeCodexTicketError(c, http.StatusInternalServerError, err.Error(), "ticket_import_failed")
		return
	}
	c.JSON(http.StatusOK, result)
}

// Generic inference error rendering inspects the request body for streaming.
// A control error must never decode an unauthenticated ticket file.
func writeCodexTicketError(c *gin.Context, status int, message, code string) {
	c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"message": message, "code": code, "type": "invalid_request_error"}})
}
