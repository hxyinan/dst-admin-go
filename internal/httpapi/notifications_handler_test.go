package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"dont/internal/notifications"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
	_ "github.com/mattn/go-sqlite3"
)

type notificationTestMail struct{}

func (notificationTestMail) SendMail(context.Context, []string, string, string) error { return nil }

func TestNotificationAPIProtectsSecretsAndRevision(t *testing.T) {
	db, err := gorm.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.DB().SetMaxOpenConns(1)
	db.LogMode(false)
	store := notifications.NewStore(db, "test_", filepath.Join(t.TempDir(), "secrets"))
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	service, err := notifications.New(store, notificationTestMail{})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewNotificationsHandler(service).Register(router.Group("/api/v2"))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v2/system/notifications"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, r)
		if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "secret-header") {
			t.Fatal("API leaked credentials", w.Body.String())
		}
		return w
	}
	body := `{"name":"Hook","type":"webhook","enabled":true,"events":["custom"],"url":"https://example.com/hook?token=secret-token","headers":{"Authorization":"secret-header"}}`
	w := request(http.MethodPost, "/channels", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Data notifications.Channel `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	channel := response.Data
	if !channel.URLConfigured || !channel.HeadersConfigured {
		t.Fatal("missing credential indicators")
	}
	if w := request(http.MethodGet, "/channels", ""); w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code)
	}
	updated := fmt.Sprintf(`{"revision":%q,"name":"Hook renamed","type":"webhook","enabled":true,"events":["custom"]}`, channel.Revision)
	if w := request(http.MethodPut, "/channels/"+channel.ID, updated); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(http.MethodPut, "/channels/"+channel.ID, updated); w.Code != 409 {
		t.Fatal("stale update accepted", w.Code)
	}
	if w := request(http.MethodPost, "/channels/"+channel.ID+"/actions/test", "{}"); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/deliveries", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "queued") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(http.MethodPost, "/actions/send", `{"title":"Custom","message":"Text"}`); w.Code != 202 {
		t.Fatal(w.Code)
	}
	if w := request(http.MethodPost, "/channels", strings.Repeat("x", 65537)); w.Code != 422 {
		t.Fatal("oversized input accepted", w.Code)
	}
	channels, _ := service.Channels()
	if w := request(http.MethodDelete, "/channels/"+channel.ID, fmt.Sprintf(`{"revision":%q}`, channels[0].Revision)); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request(http.MethodPost, "/actions/send", `{"title":"Custom","message":"Text"}`); w.Code != 422 {
		t.Fatal("no-subscriber send accepted", w.Code)
	}
	if w := request(http.MethodGet, "/deliveries", ""); w.Code != 200 || strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatal("delete lost history")
	}
}
