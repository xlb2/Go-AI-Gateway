package knowledge

import (
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func Register(group *gin.RouterGroup, store *Store) {
	registerChunkIndexes(group, store)
	group.GET("/libraries", func(c *gin.Context) {
		items, err := store.Libraries(c.Request.Context(), c.GetUint("user_id"), cursor(c), 50)
		if respondError(c, err) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	group.POST("/libraries", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
		var input struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&input); err != nil {
			respondError(c, ErrInvalid)
			return
		}
		lib, err := store.CreateLibrary(c.Request.Context(), c.GetUint("user_id"), input.Name)
		if respondError(c, err) {
			return
		}
		c.JSON(http.StatusCreated, lib)
	})
	group.GET("/libraries/:library_id/sources", func(c *gin.Context) {
		id, ok := resourceID(c, "library_id")
		if !ok {
			return
		}
		items, err := store.Sources(c.Request.Context(), c.GetUint("user_id"), id, cursor(c), 50)
		if respondError(c, err) {
			return
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
	group.POST("/libraries/:library_id/sources", func(c *gin.Context) {
		id, ok := resourceID(c, "library_id")
		if !ok {
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxBodyBytes+64*1024)
		if err := c.Request.ParseMultipartForm(MaxBodyBytes + 64*1024); err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				c.JSON(413, gin.H{"error": "upload too large"})
				return
			}
			respondError(c, ErrInvalid)
			return
		}
		if c.Request.MultipartForm == nil {
			respondError(c, ErrInvalid)
			return
		}
		defer c.Request.MultipartForm.RemoveAll()
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			respondError(c, ErrInvalid)
			return
		}
		defer file.Close()
		body, err := io.ReadAll(io.LimitReader(file, MaxBodyBytes+1))
		if err != nil {
			respondError(c, ErrInvalid)
			return
		}
		if len(body) > MaxBodyBytes {
			c.JSON(413, gin.H{"error": "file exceeds 2 MiB"})
			return
		}
		format := strings.TrimPrefix(strings.ToLower(filepath.Ext(header.Filename)), ".")
		title := strings.TrimSpace(c.Request.FormValue("title"))
		if title == "" {
			title = header.Filename
		}
		source, duplicate, err := store.Import(c.Request.Context(), c.GetUint("user_id"), id, title, format, string(body))
		if respondError(c, err) {
			return
		}
		status := http.StatusCreated
		if duplicate {
			status = http.StatusOK
		}
		c.JSON(status, gin.H{"source": source, "duplicate": duplicate})
	})
	group.GET("/libraries/:library_id/sources/:source_id", func(c *gin.Context) {
		library, ok := resourceID(c, "library_id")
		if !ok {
			return
		}
		source, ok := resourceID(c, "source_id")
		if !ok {
			return
		}
		doc, err := store.Read(c.Request.Context(), c.GetUint("user_id"), library, source)
		if respondError(c, err) {
			return
		}
		c.JSON(http.StatusOK, doc)
	})
}

func cursor(c *gin.Context) uint64 { n, _ := strconv.ParseUint(c.Query("after"), 10, 64); return n }
func resourceID(c *gin.Context, key string) (uint64, bool) {
	n, err := strconv.ParseUint(c.Param(key), 10, 64)
	if err != nil || n == 0 {
		respondError(c, ErrInvalid)
		return 0, false
	}
	return n, true
}
func respondError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	status, message := http.StatusInternalServerError, "资料服务暂时不可用"
	if errors.Is(err, ErrInvalid) {
		status, message = 400, "输入无效：名称和正文不能为空，仅支持不超过 2 MiB 的 UTF-8 .md/.txt"
	}
	if errors.Is(err, ErrNotFound) {
		status, message = 404, "资料或资料库不存在"
	}
	c.JSON(status, gin.H{"error": message})
	return true
}
