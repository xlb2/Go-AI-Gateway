//go:build knowledgeintegration

package knowledge_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	driver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"go_im_gateway/internal/knowledge"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func database(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("KNOWLEDGE_TEST_DSN")
	if dsn == "" {
		dsn = "root:123456@tcp(127.0.0.1:3307)/?charset=utf8mb4&parseTime=true"
	}
	cfg, err := driver.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid KNOWLEDGE_TEST_DSN")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	cfg.Timeout = 5 * time.Second
	admin, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("MySQL unavailable; start im_mysql or set KNOWLEDGE_TEST_DSN")
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adminSQL.Close() })
	name := "gw_knowledge_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4").Error; err != nil {
		t.Fatal(err)
	}
	// Only the freshly generated test database is ever dropped.
	t.Cleanup(func() {
		if err := admin.Exec("DROP DATABASE `" + name + "`").Error; err != nil {
			t.Errorf("test database cleanup: %v", err)
		}
	})
	cfg.DBName = name
	db, err := gorm.Open(mysql.Open(cfg.FormatDSN()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	for i := 0; i < 2; i++ {
		if err := knowledge.Migrate(db); err != nil {
			t.Fatalf("migration pass %d: %v", i+1, err)
		}
		var versions []int
		if err := db.Table("knowledge_schema_versions").Order("version ASC").Pluck("version", &versions).Error; err != nil {
			t.Fatal(err)
		}
		if len(versions) != 4 || versions[0] != 1 || versions[1] != 2 || versions[2] != 3 || versions[3] != 4 {
			t.Fatalf("migration pass %d: unexpected versions %v", i+1, versions)
		}
	}
	return db
}

func TestKnowledgeMySQLImportAndIsolation(t *testing.T) {
	db := database(t)
	store := knowledge.NewStore(db)
	ctx := context.Background()
	lib, err := store.CreateLibrary(ctx, 11, "Agent 学习")
	if err != nil {
		t.Fatal(err)
	}
	body := "\ufeff# 原文\r\n<script>bad()</script>\n```go\nfunc main() {}\n```"
	var wg sync.WaitGroup
	ids := make(chan uint64, 6)
	failures := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			src, _, err := store.Import(ctx, 11, lib.ID, "讲义", "md", body)
			if err != nil {
				failures <- err
			} else {
				ids <- src.ID
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	var id uint64
	for found := range ids {
		if id != 0 && id != found {
			t.Fatal("concurrent duplicate created")
		}
		id = found
	}
	if id == 0 {
		t.Fatal("no source created")
	}
	doc, err := store.Read(ctx, 11, lib.ID, id)
	if err != nil || doc.Version.Body != body || doc.Version.Version != 1 {
		t.Fatalf("roundtrip err=%v", err)
	}
	var count int64
	db.Model(&knowledge.Version{}).Count(&count)
	if count != 1 {
		t.Fatal("duplicate versions")
	}
	if _, err := store.Read(ctx, 12, lib.ID, id); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatal("cross-user read")
	}
	if _, _, err := store.Import(ctx, 12, lib.ID, "denied", "txt", "private"); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatal("cross-user import")
	}
	other, err := store.CreateLibrary(ctx, 11, "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, 11, other.ID, id); !errors.Is(err, knowledge.ErrNotFound) {
		t.Fatal("cross-library read")
	}
	// Fail the version insert after source creation: the source must roll back too.
	if err := db.Callback().Create().Before("gorm:create").Register("test:version_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "knowledge_source_versions" {
			tx.AddError(errors.New("injected version failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Import(ctx, 11, lib.ID, "rollback", "txt", "different content")
	db.Callback().Create().Remove("test:version_failure")
	if err == nil {
		t.Fatal("injected failure ignored")
	}
	items, err := store.Sources(ctx, 11, lib.ID, 0, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("partial source persisted: %v %v", items, err)
	}
}

func TestKnowledgeMySQLHTTPFlow(t *testing.T) {
	db := database(t)
	r := gin.New()
	knowledge.Register(r.Group("/api/v1", func(c *gin.Context) { c.Set("user_id", uint(21)) }), knowledge.NewStore(db))
	out := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/libraries", strings.NewReader(`{"name":"阅读"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(out, req)
	if out.Code != 201 {
		t.Fatal(out.Body.String())
	}
	var lib knowledge.Library
	if err := json.Unmarshal(out.Body.Bytes(), &lib); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "lesson.md")
	if err != nil {
		t.Fatal(err)
	}
	file.Write([]byte("# Hello\n中文正文"))
	writer.Close()
	out = httptest.NewRecorder()
	req = httptest.NewRequest("POST", fmt.Sprintf("/api/v1/libraries/%d/sources", lib.ID), &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	r.ServeHTTP(out, req)
	if out.Code != 201 {
		t.Fatalf("upload: %d %s", out.Code, out.Body.String())
	}
	var result struct {
		Source knowledge.Source `json:"source"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	out = httptest.NewRecorder()
	r.ServeHTTP(out, httptest.NewRequest("GET", fmt.Sprintf("/api/v1/libraries/%d/sources/%d", lib.ID, result.Source.ID), nil))
	if out.Code != 200 {
		t.Fatal(out.Body.String())
	}
	var doc knowledge.Document
	if err := json.Unmarshal(out.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version.Body != "# Hello\n中文正文" {
		t.Fatal("HTTP body changed")
	}
}
