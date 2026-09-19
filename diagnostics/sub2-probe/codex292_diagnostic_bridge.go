package service

import (
 "context"
 "time"
)

// DiagnosticCodex292Probe is a temporary build bridge for the local comparison
// runner. It does not touch the running server, its database, or cached tickets.
func DiagnosticCodex292Probe(ctx context.Context, upstream HTTPUpstream, account *Account, token, model, proxyURL string) (string, int, error) {
 gateway := &OpenAIGatewayService{httpUpstream: upstream}
 return gateway.fireOpenAICodexTicketProbe(ctx, account, token, model, proxyURL, 25*time.Second)
}
