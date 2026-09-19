package executor

import (
	"net/http"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/codexticket"
	"github.com/tidwall/gjson"
)

func applyHostCodexTicket(auth *cliproxyauth.Auth, model string, headers http.Header) error {
	if auth == nil || codexAuthUsesAPIKey(auth) {
		return nil
	}
	return codexticket.Apply(auth.ID, model, headers)
}

func applyHostCodexTicketForBody(auth *cliproxyauth.Auth, fallbackModel string, body []byte, headers http.Header) error {
	model := gjson.GetBytes(body, "model").String()
	if model == "" {
		model = fallbackModel
	}
	return applyHostCodexTicket(auth, model, headers)
}

// A reused websocket keeps its handshake headers. Reconnect when its ticket or
// model changes; incremental-only requests use the existing replay-required path.
// The caller holds sess.reqMu, so no in-flight response is interrupted.
func (e *CodexWebsocketsExecutor) refreshTicketSession(sess *codexWebsocketSession, model string, headers http.Header) {
	if sess == nil || !codexticket.Active() {
		return
	}
	key := ""
	if ticket := headers.Get(codexticket.Header); ticket != "" {
		key = model + "\x00" + ticket
	}
	sess.connMu.Lock()
	changed, conn := sess.ticketKey != key, sess.conn
	sess.ticketKey = key
	sess.connMu.Unlock()
	if changed && conn != nil {
		e.invalidateUpstreamConnWithoutDisconnectNotify(sess, conn, "ticket_changed", nil)
	}
}
