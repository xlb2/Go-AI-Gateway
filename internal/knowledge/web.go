package knowledge

import (
	"embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/*
var webFiles embed.FS

func RegisterPage(r *gin.Engine) {
	for route, asset := range map[string]string{"/knowledge": "index.html", "/knowledge/app.js": "app.js", "/knowledge/conversations.js": "conversations.js", "/knowledge/style.css": "style.css"} {
		name := asset
		r.GET(route, func(c *gin.Context) {
			body, err := webFiles.ReadFile("web/" + name)
			if err != nil {
				c.Status(http.StatusInternalServerError)
				return
			}
			contentType := "text/html; charset=utf-8"
			if name == "app.js" || name == "conversations.js" {
				contentType = "text/javascript; charset=utf-8"
			}
			if name == "style.css" {
				contentType = "text/css; charset=utf-8"
			}
			c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Cache-Control", "no-store")
			c.Data(200, contentType, body)
		})
	}
}
