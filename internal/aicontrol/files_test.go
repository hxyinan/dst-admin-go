package aicontrol

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAttachmentStreamsAndEnforcesOwnerExpiryAndPermissions(t *testing.T) {
	_, s := fixture(t, nil)
	p := Principal{ID: "page"}
	v, err := s.SaveAttachment(context.Background(), p, "../../a.zip", strings.NewReader("archive-bytes"))
	if err != nil || v.Name != "a.zip" || v.Size != 13 || len(v.SHA256) != 64 {
		t.Fatal(v, err)
	}
	info, err := os.Stat(s.filePath("attachments", v.ID, ".data"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	if _, _, err = s.OpenAttachment(Principal{ID: "other"}, v.ID); err == nil {
		t.Fatal("cross-caller attachment exposed")
	}
	_, f, err := s.OpenAttachment(p, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "archive-bytes" {
		t.Fatal(string(data))
	}
	if err = s.DeleteAttachment(Principal{ID: "other"}, v.ID); err == nil {
		t.Fatal("cross-caller deletion allowed")
	}
	if _, err = s.Attachment(p, "../../provider.key"); err == nil {
		t.Fatal("path escaped store")
	}
	if err = s.DeleteAttachment(p, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Attachment(p, v.ID); err == nil {
		t.Fatal("deleted attachment still exists")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.SaveAttachment(ctx, p, "cancel.zip", strings.NewReader("anything")); err == nil {
		t.Fatal("canceled upload succeeded")
	}
	items, _ := os.ReadDir(filepath.Join(s.root, "attachments"))
	if len(items) != 0 {
		t.Fatal("partial upload was not cleaned up", items)
	}
	old, err := s.SaveAttachment(context.Background(), p, "old.zip", strings.NewReader("old"))
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-25 * time.Hour)
	for _, ext := range []string{".json", ".data"} {
		if err = os.Chtimes(s.filePath("attachments", old.ID, ext), past, past); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.SaveAttachment(context.Background(), p, "new.zip", strings.NewReader("new")); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(s.filePath("attachments", old.ID, ".data")); !os.IsNotExist(err) {
		t.Fatal("expired file not pruned", err)
	}
}

func TestPrivateResultIsEncryptedAndCannotCrossCallers(t *testing.T) {
	_, store := fixture(t, nil)
	p := Principal{ID: "page"}
	raw := []byte(`{"secret":"fresh-one-time-token","token":{"id":"new-token"}}`)
	if !HasPrivateResult(raw) || HasPrivateResult([]byte(`{"enabled":true}`)) {
		t.Fatal("private result classification is wrong")
	}
	id, err := store.SavePrivateResult(p, raw)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(store.filePath("private-results", id, ".json"))
	if err != nil || strings.Contains(string(stored), "fresh-one-time-token") {
		t.Fatal("result stored as plaintext", err)
	}
	value, err := store.PrivateResult(p, id)
	if err != nil || string(value) != string(raw) {
		t.Fatal("private result could not be recovered", err)
	}
	if _, err = store.PrivateResult(Principal{ID: "other"}, id); err == nil {
		t.Fatal("private result exposed to other caller")
	}
	if _, err = store.PrivateResult(p, "../../provider.key"); err == nil {
		t.Fatal("private result traversal")
	}
}

func TestAttachmentQuotaIncludesInterruptedUploads(t *testing.T) {
	_, store := fixture(t, nil)
	dir := filepath.Join(store.root, "attachments")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, ".upload-interrupted"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(MaxAttachmentBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err = store.SaveAttachment(context.Background(), Principal{ID: "page"}, "next.zip", strings.NewReader("new")); !errors.Is(err, ErrBusy) {
		t.Fatal("interrupted file escaped storage quota", err)
	}
	if _, err = store.SaveAttachment(context.Background(), Principal{ID: "page"}, "small.zip", strings.NewReader("new"), 3); err != nil {
		t.Fatal("small known-size upload cannot use remaining quota", err)
	}
}
