package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go_im_gateway/internal/harness/runstate"
)

func isOpenCodeEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "opencode.ai")
}

func openCodeSession(ctx context.Context) (string, error) {
	ref, ok := runstate.FromContext(ctx)
	if !ok {
		uid, err := getUserID(ctx)
		if err != nil {
			return "", fmt.Errorf("OpenCode requires session identity: %w", err)
		}
		ref = runstate.Ref{UserID: uid, SessionID: runstate.LegacySessionID(uid)}
	}
	if ref.UserID == 0 || ref.SessionID == "" {
		return "", fmt.Errorf("OpenCode requires a nonempty session and owner")
	}
	// Ignore run IDs: turns, child calls and summaries belong to the same conversation.
	digest := sha256.Sum256([]byte(fmt.Sprintf("go-ai-gateway/%d/%s", ref.UserID, ref.SessionID)))
	return fmt.Sprintf("gw-%x", digest), nil
}

type openCodeTransport struct {
	base    http.RoundTripper
	session string
}

func (t openCodeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !isOpenCodeEndpoint(req.URL.String()) {
		return t.base.RoundTrip(req)
	}
	// Clone so redirects and concurrent requests do not inherit mutated headers.
	copy := req.Clone(req.Context())
	copy.Header.Set("x-opencode-session", t.session)
	copy.Header.Set("User-Agent", "go-ai-gateway/0.1")
	return t.base.RoundTrip(copy)
}
