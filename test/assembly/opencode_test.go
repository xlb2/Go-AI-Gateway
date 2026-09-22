package assembly_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"go_im_gateway/internal/harness/agent"
	"go_im_gateway/internal/harness/runstate"
)

type modelTransport func(*http.Request) (*http.Response, error)

func (f modelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Intercept the real SDK request without using credentials or contacting a provider.
func captureModelRequests(t *testing.T) *[]http.Header {
	t.Helper()
	var headers []http.Header
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = modelTransport(func(r *http.Request) (*http.Response, error) {
		defer r.Body.Close()
		headers = append(headers, r.Header.Clone())
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		body := `{"id":"reply","object":"chat.completion","model":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
		contentType := "application/json"
		if request.Stream {
			contentType = "text/event-stream"
			body = "data: {\"id\":\"reply\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	t.Setenv("AGENT_LOOP", "own")
	t.Setenv("VOLC_ACCESS_KEY", "fixture-key")
	t.Setenv("VOLC_ENDPOINT_ID", "fixture")
	return &headers
}

func TestOpenCodeSessionHeaders(t *testing.T) {
	headers := captureModelRequests(t)
	t.Setenv("VOLC_BASE_URL", "https://opencode.ai/zen/go/v1")
	contexts := []context.Context{
		runstate.WithRef(context.Background(), runstate.Ref{UserID: 7, SessionID: "user:7", RunID: "first"}),
		runstate.WithRef(context.Background(), runstate.Ref{UserID: 7, SessionID: "user:7", RunID: "second"}),
		runstate.WithRef(context.Background(), runstate.Ref{UserID: 7, SessionID: "user:7", RunID: "child", ParentRunID: "first"}),
		context.WithValue(context.Background(), "user_id", uint(7)),
		runstate.WithRef(context.Background(), runstate.Ref{UserID: 8, SessionID: "user:8", RunID: "other"}),
		runstate.WithRef(context.Background(), runstate.Ref{UserID: 7, SessionID: "knowledge:99", RunID: "web"}),
	}
	for _, ctx := range contexts {
		// Reconstructing the factory must not reset the identity.
		cfg, err := agent.RuntimeConfigFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		m, err := cfg.NewModel(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for range 2 {
			stream, err := m.Stream(ctx, []*schema.Message{schema.UserMessage("hello")})
			if err != nil {
				t.Fatal(err)
			}
			for {
				_, err = stream.Recv()
				if err != nil {
					break
				}
			}
			stream.Close()
			if err != io.EOF {
				t.Fatal(err)
			}
		}
		if _, err := agent.Summarize(ctx, []string{"history"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(*headers) != 18 {
		t.Fatalf("requests=%d", len(*headers))
	}
	first := (*headers)[0].Get("x-opencode-session")
	if first == "" {
		t.Fatal("missing session header")
	}
	for i, h := range *headers {
		if h.Get("User-Agent") != "go-ai-gateway/0.1" || h.Get("Authorization") != "Bearer fixture-key" {
			t.Fatalf("incorrect client or auth at request %d", i)
		}
		id := h.Get("x-opencode-session")
		if i < 12 && id != first {
			t.Fatalf("session changed across steps, turns, child or summary: %d", i)
		}
		if i >= 12 && (id == "" || id == first) {
			t.Fatalf("sessions not separated: %d", i)
		}
	}
}

func TestOpenCodeRequiresSessionIdentity(t *testing.T) {
	_ = captureModelRequests(t)
	t.Setenv("VOLC_BASE_URL", "https://opencode.ai/zen/v1")
	cfg, err := agent.RuntimeConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.NewModel(context.Background()); err == nil {
		t.Fatal("missing identity accepted")
	}
}

func TestOpenCodeHeadersDoNotAffectOtherProviders(t *testing.T) {
	headers := captureModelRequests(t)
	for _, endpoint := range []string{"https://ark.cn-beijing.volces.com/api/v3", "https://opencode.ai.example.com/v1"} {
		t.Setenv("VOLC_BASE_URL", endpoint)
		cfg, err := agent.RuntimeConfigFromEnv()
		if err != nil {
			t.Fatal(err)
		}
		m, err := cfg.NewModel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Generate(context.Background(), []*schema.Message{schema.UserMessage("hello")}); err != nil {
			t.Fatal(err)
		}
	}
	for _, h := range *headers {
		if h.Get("x-opencode-session") != "" || h.Get("User-Agent") == "go-ai-gateway/0.1" {
			t.Fatal("provider-specific headers leaked")
		}
	}
}
