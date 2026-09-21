package assembly

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"go_im_gateway/internal/handler"
)

func jwtFixture(t *testing.T, key string, method jwt.SigningMethod, uid uint, expiry *jwt.NumericDate) string {
	t.Helper()
	claims := handler.CustomClaims{UserID: uid, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: expiry}}
	token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestJWTConfiguration(t *testing.T) {
	for _, secret := range []string{"", "short", strings.Repeat(" ", 40), " " + strings.Repeat("x", 32)} {
		t.Setenv("JWT_SECRET", secret)
		if handler.ValidateJWTConfig() == nil {
			t.Fatal("invalid configuration accepted")
		}
		if _, err := handler.ParseToken("invalid"); err == nil {
			t.Fatal("unconfigured verifier accepted token")
		}
	}
	t.Setenv("JWT_SECRET", strings.Repeat("test-key-", 5))
	if err := handler.ValidateJWTConfig(); err != nil {
		t.Fatal(err)
	}
}

func TestJWTClaimsAndRotation(t *testing.T) {
	key := strings.Repeat("test-key-", 5)
	t.Setenv("JWT_SECRET", key)
	future := jwt.NewNumericDate(time.Now().Add(time.Hour))
	valid := jwtFixture(t, key, jwt.SigningMethodHS256, 42, future)
	claims, err := handler.ParseToken(valid)
	if err != nil || claims.UserID != 42 {
		t.Fatalf("claims=%v err=%v", claims, err)
	}
	for _, token := range []string{
		jwtFixture(t, "go_im_gateway_super_secret_key_2026", jwt.SigningMethodHS256, 42, future),
		jwtFixture(t, key, jwt.SigningMethodHS384, 42, future),
		jwtFixture(t, key, jwt.SigningMethodHS256, 0, future),
		jwtFixture(t, key, jwt.SigningMethodHS256, 42, nil),
		jwtFixture(t, key, jwt.SigningMethodHS256, 42, jwt.NewNumericDate(time.Now().Add(-time.Hour))),
	} {
		if _, err := handler.ParseToken(token); err == nil {
			t.Fatal("invalid token accepted")
		}
	}
	t.Setenv("JWT_SECRET", strings.Repeat("replacement-", 4))
	if _, err := handler.ParseToken(valid); err == nil {
		t.Fatal("rotated key accepted old token")
	}
}

func TestJWTEntrypointsRejectForgedTokens(t *testing.T) {
	t.Setenv("JWT_SECRET", strings.Repeat("entrypoint-test-", 3))
	forged := jwtFixture(t, "go_im_gateway_super_secret_key_2026", jwt.SigningMethodHS256, 42, jwt.NewNumericDate(time.Now().Add(time.Hour)))
	r := gin.New()
	r.GET("/http", handler.JWTAuthMiddleware(), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	// Rejection must occur before upgrading or touching any backend dependency.
	r.GET("/ws", handler.ConnectWSWithRunner(nil, nil, nil))
	r.GET("/rpc", handler.RPCConnect(nil))
	for _, path := range []string{"/http", "/ws", "/rpc"} {
		req := httptest.NewRequest(http.MethodGet, path+"?token="+forged, nil)
		req.Header.Set("Authorization", "Bearer "+forged)
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		if out.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d", path, out.Code)
		}
	}
	valid := jwtFixture(t, strings.Repeat("entrypoint-test-", 3), jwt.SigningMethodHS256, 42, jwt.NewNumericDate(time.Now().Add(time.Hour)))
	req := httptest.NewRequest(http.MethodGet, "/http", nil)
	req.Header.Set("Authorization", "Bearer "+valid)
	out := httptest.NewRecorder()
	r.ServeHTTP(out, req)
	if out.Code != http.StatusNoContent {
		t.Fatalf("configured token rejected: %d", out.Code)
	}
}
