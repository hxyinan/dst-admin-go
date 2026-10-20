package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dont/internal/aicontrol"
	"dont/internal/apicontract"
	"dont/internal/authn"
	"dont/internal/automation"
	"dont/internal/backups"
	"dont/internal/jobs"
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

type aiPolicy struct{}

func (aiPolicy) Policy(string) (backups.Policy, error) { return backups.Policy{}, nil }
func (aiPolicy) SavePolicy(string, backups.PolicyRequest) (backups.Policy, error) {
	return backups.Policy{}, nil
}

func TestGeneratedAPIUsesRealAuthenticationAndPersistsSharedBackupSchedule(t *testing.T) {
	db, err := gorm.Open("sqlite3", filepath.Join(t.TempDir(), "flow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.DB().SetMaxOpenConns(1)
	db.LogMode(false)
	auth := authn.NewService(db)
	if err = auth.Migrate(); err != nil {
		t.Fatal(err)
	}
	session, cookie, err := auth.Setup("admin", "testpassword", "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	as := automation.NewStore(db, "flow_")
	if err = as.Migrate(); err != nil {
		t.Fatal(err)
	}
	js := jobs.NewStore(db, "flow_")
	if err = js.Migrate(); err != nil {
		t.Fatal(err)
	}
	jobService, err := jobs.NewService(js, jobs.NewBroker())
	if err != nil {
		t.Fatal(err)
	}
	automationService, err := automation.NewService(automationHTTPRooms{}, as, jobService, automationHTTPExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	files := aicontrol.NewStore(db, "flow_", t.TempDir())
	if err = files.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err = files.SaveConfig(aicontrol.ConfigInput{Config: aicontrol.Config{Revision: "initial", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(Recovery(), RequestContext(), RequireSession(auth), NewIdempotencyStore(time.Hour, 100).Middleware())
	backend := &AIBackend{}
	backend.ConfigureAPI(router, files)
	aiService := aicontrol.New(files, backend)
	catalog, _ := apicontract.Default()
	aiService.ConfigureAPI(catalog)
	NewBackupScheduleHandler(automationService, aiPolicy{}, nil).Register(router.Group("/api/v2"))
	principal := aicontrol.Principal{ID: "page"}
	request := httptest.NewRequest("POST", "/api/v2/system/ai/chat", nil)
	request.AddCookie(&http.Cookie{Name: authn.SessionCookieName, Value: cookie})
	meta := aicontrol.RequestAccess(request, "127.0.0.1")
	ctx := aicontrol.WithRequestMetadata(context.Background(), meta)
	first, err := aiService.Invoke(ctx, principal, "api_configureRoomBackupSchedule", []byte(`{"path":{"roomId":"room"},"body":{"enabled":true},"requestId":"no-csrf-0001"}`))
	if err != nil || first.Status != "failed" || !strings.Contains(string(first.Data), "CSRF_INVALID") {
		t.Fatal("actual CSRF guard bypassed", first, err)
	}
	meta.Credentials.Set("X-CSRF-Token", session.CSRFToken)
	first, err = aiService.Invoke(ctx, principal, "api_configureRoomBackupSchedule", []byte(`{"path":{"roomId":"room"},"body":{"enabled":true},"requestId":"enable-backup-001"}`))
	if err != nil || first.Status != "succeeded" || !strings.Contains(string(first.Data), `"effectiveEnabled":true`) {
		t.Fatal(first, err)
	}
	tasks, _ := automationService.Tasks("room")
	if len(tasks) != 1 || !tasks[0].Enabled {
		t.Fatal("did not persist actual task", tasks)
	}
	before := tasks[0].ID
	second, err := aiService.Invoke(ctx, principal, "api_listRoomBackupSchedules", []byte(`{"path":{"roomId":"room"}}`))
	if err != nil || !strings.Contains(string(second.Data), before) {
		t.Fatal("shared read did not see AI write", second, err)
	}
	if err = auth.Logout(cookie); err != nil {
		t.Fatal(err)
	}
	third, err := aiService.Invoke(ctx, principal, "api_configureRoomBackupSchedule", []byte(`{"path":{"roomId":"room"},"body":{"enabled":false},"requestId":"after-logout-001"}`))
	if err != nil || third.Status != "failed" || !strings.Contains(string(third.Data), "SESSION_INVALID") {
		t.Fatal("expired login reached management action", third, err)
	}
	tasks, _ = automationService.Tasks("room")
	if !tasks[0].Enabled {
		t.Fatal("unauthenticated change persisted")
	}
}

func TestGenericMultipartAndDownloadUseAuthenticatedOriginalRouter(t *testing.T) {
	db, err := gorm.Open("sqlite3", filepath.Join(t.TempDir(), "files.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.LogMode(false)
	files := aicontrol.NewStore(db, "files_", t.TempDir())
	files.Migrate()
	files.SaveConfig(aicontrol.ConfigInput{Config: aicontrol.Config{Revision: "initial", Enabled: true}})
	router := gin.New()
	token, secret, err := files.CreateToken(aicontrol.TokenInput{Name: "file test", Days: 1})
	if err != nil {
		t.Fatal(err)
	}
	router.Use(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer "+secret {
			c.AbortWithStatus(401)
			return
		}
		c.Next()
	})
	var received string
	router.POST("/api/v2/rooms/:roomId/backups/upload", func(c *gin.Context) {
		f, err := c.FormFile("file")
		if err != nil {
			t.Error(err)
			c.AbortWithStatus(400)
			return
		}
		if c.Request.MultipartForm != nil {
			defer c.Request.MultipartForm.RemoveAll()
		}
		source, _ := f.Open()
		defer source.Close()
		data, _ := io.ReadAll(source)
		received = string(data)
		Success(c, 201, gin.H{"name": c.PostForm("name")})
	})
	router.GET("/api/v2/backups/:backupId/download", func(c *gin.Context) {
		c.Header("Content-Disposition", `attachment; filename="test.zip"`)
		c.Data(200, "application/zip", []byte("downloaded-archive"))
	})
	backend := &AIBackend{}
	backend.ConfigureAPI(router, files)
	service := aicontrol.New(files, backend)
	h := NewAIHandler(service)
	h.ConfigureResources(router)
	// Test principal is a cookie caller; download auth remains the test router's Bearer.
	h.resourceRouter = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { router.ServeHTTP(w, r) })
	router.GET("/api/v2/system/ai/resources/:resourceId", h.resource)
	catalog, _ := apicontract.Default()
	p := aicontrol.Principal{ID: token.ID}
	item, err := files.SaveAttachment(context.Background(), p, "upload.zip", strings.NewReader("uploaded-archive"))
	if err != nil {
		t.Fatal(err)
	}
	op, _ := catalog.Operation("uploadRoomBackup")
	input := apicontract.Input{Path: map[string]any{"roomId": "room"}, Body: map[string]any{"file": map[string]any{"attachmentId": item.ID}, "name": "saved archive"}, RequestID: "upload-archive-001"}
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	ctx := aicontrol.WithRequestMetadata(context.Background(), aicontrol.RequestAccess(req, "127.0.0.1"))
	result, err := backend.InvokeAPI(ctx, p, op, input)
	if err != nil || received != "uploaded-archive" || !strings.Contains(string(result), "saved archive") {
		t.Fatal(string(result), received, err)
	}
	op, _ = catalog.Operation("downloadBackup")
	result, err = backend.InvokeAPI(ctx, p, op, apicontract.Input{Path: map[string]any{"backupId": "backup"}})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Resource struct {
			URL string `json:"url"`
		}
	}
	if err = json.Unmarshal(result, &response); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(response.Resource.URL, "/api/v2/system/ai/resources/")
	resource, err := files.Resource(p, id)
	if err != nil {
		t.Fatal(err)
	}
	if resource.OperationID != op.ID {
		t.Fatal("resource lost original operation")
	}
	uri := response.Resource.URL
	download := httptest.NewRequest("GET", uri, nil)
	download.Header.Set("Authorization", "Bearer "+secret)
	out := httptest.NewRecorder()
	router.ServeHTTP(out, download)
	if out.Code != 200 || out.Body.String() != "downloaded-archive" {
		t.Fatal(out.Code, out.Body)
	}
	denied := httptest.NewRecorder()
	router.ServeHTTP(denied, httptest.NewRequest("GET", uri, nil))
	if denied.Code != 401 {
		t.Fatal("download authentication bypass")
	}
	if _, err = files.Resource(aicontrol.Principal{ID: "other"}, id); err == nil {
		t.Fatal("cross-caller download exposed")
	}
	// Real HTTP upload streams rather than materializing an in-memory file.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", "ui.zip")
	part.Write([]byte("ui bytes"))
	writer.Close()
	upload := httptest.NewRequest("POST", "/upload", &body)
	upload.Header.Set("Content-Type", writer.FormDataContentType())
	public := gin.New()
	public.POST("/upload", h.upload)
	out = httptest.NewRecorder()
	public.ServeHTTP(out, upload)
	if out.Code != 201 {
		t.Fatal(out.Code, out.Body)
	}
}

func TestMutationWithOversizedResponseIsUncertainAndNeverReexecuted(t *testing.T) {
	db, err := gorm.Open("sqlite3", filepath.Join(t.TempDir(), "large.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.LogMode(false)
	store := aicontrol.NewStore(db, "large_", t.TempDir())
	if err = store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.SaveConfig(aicontrol.ConfigInput{Config: aicontrol.Config{Revision: "initial", Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	var executed atomic.Int32
	router := gin.New()
	router.Use(RequestContext())
	router.POST("/api/v2/rooms/:roomId/backup-schedules/actions/configure", func(c *gin.Context) { executed.Add(1); Success(c, 200, gin.H{"content": strings.Repeat("x", 300<<10)}) })
	router.POST("/api/v2/rooms/:roomId/actions/:action", func(c *gin.Context) {
		if !c.GetBool("dst_admin.ai_action") {
			t.Error("AI action audit source lost")
		}
		Success(c, 202, gin.H{"id": "actual-save-job"})
	})
	backend := &AIBackend{}
	backend.ConfigureAPI(router, store)
	service := aicontrol.New(store, backend)
	catalog, _ := apicontract.Default()
	service.ConfigureAPI(catalog)
	meta := &aicontrol.RequestMetadata{Credentials: http.Header{"Cookie": []string{"test=valid"}}}
	ctx := aicontrol.WithRequestMetadata(context.Background(), meta)
	for _, action := range []string{"stop", "save"} {
		data, err := backend.Invoke(ctx, "room_control", aicontrol.Arguments{RoomID: "room", Action: action, RequestID: "compat-" + action + "-001"}, "")
		if err != nil || !strings.Contains(string(data), "actual-save-job") {
			t.Fatal("compatibility control did not reach shared route", action, string(data), err)
		}
	}
	p := aicontrol.Principal{ID: "page"}
	raw := []byte(`{"path":{"roomId":"room"},"body":{"enabled":true},"requestId":"large-response-001"}`)
	first, err := service.Invoke(ctx, p, "api_configureRoomBackupSchedule", raw)
	if err != nil || first.Status != "uncertain" {
		t.Fatal(first, err)
	}
	second, err := service.Invoke(ctx, p, "api_configureRoomBackupSchedule", raw)
	if err != nil || second.CallID != first.CallID || second.Status != "uncertain" || executed.Load() != 1 {
		t.Fatal(second, err, executed.Load())
	}
}
