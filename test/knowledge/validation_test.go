package knowledge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go_im_gateway/internal/knowledge"
)

func TestKnowledgeDocumentValidation(t *testing.T) {
	for _, body := range []string{"", " \n", "\ufeff", string([]byte{0xff}), "bad\x00text", strings.Repeat("x", knowledge.MaxBodyBytes+1)} {
		if knowledge.ValidateDocument("Title", "md", body) == nil {
			t.Fatal("invalid body accepted")
		}
	}
	for _, format := range []string{"html", "pdf", ""} {
		if knowledge.ValidateDocument("Title", format, "body") == nil {
			t.Fatal("unsupported format accepted")
		}
	}
	if knowledge.ValidateDocument(strings.Repeat("字", 201), "md", "body") == nil {
		t.Fatal("long title accepted")
	}
	for _, body := range []string{"\ufeff# 中文\r\n```go\nfunc main() {}\n```", "<script>alert(1)</script>", strings.Repeat("x", knowledge.MaxBodyBytes)} {
		if err := knowledge.ValidateDocument("标题", "md", body); err != nil {
			t.Fatal(err)
		}
	}
}

func TestKnowledgeHTTPRejectsMalformedUpload(t *testing.T) {
	r := gin.New()
	knowledge.Register(r.Group("/api/v1"), knowledge.NewStore(nil))
	for _, path := range []string{"/api/v1/libraries/0/sources", "/api/v1/libraries/1/sources"} {
		req := httptest.NewRequest("POST", path, strings.NewReader("file=bad"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != 400 {
			t.Fatalf("status=%d", out.Code)
		}
	}
}

func TestKnowledgePageHeaders(t *testing.T) {
	r := gin.New()
	knowledge.RegisterPage(r)
	for _, path := range []string{"/knowledge", "/knowledge/app.js", "/knowledge/conversations.js", "/knowledge/style.css"} {
		out := httptest.NewRecorder()
		r.ServeHTTP(out, httptest.NewRequest(http.MethodGet, path, nil))
		if out.Code != 200 || out.Body.Len() == 0 {
			t.Fatalf("asset %s missing", path)
		}
		if !strings.Contains(out.Header().Get("Content-Security-Policy"), "object-src 'none'") {
			t.Fatal("missing CSP")
		}
	}
}
