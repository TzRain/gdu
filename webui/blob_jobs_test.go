package webui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobCleanupCanResumeWithoutDeletingNewContent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "archive")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "first.txt")
	second := filepath.Join(dir, "second.txt")
	for _, p := range []string{first, second} {
		if err := os.WriteFile(p, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, total, err := snapshotLocal(dir)
	if err != nil {
		t.Fatal(err)
	}
	job, err := newBlobJob()
	if err != nil {
		t.Fatal(err)
	}
	job.Stage, job.Source, job.Destination = "uploaded", dir, "https://example.blob.core.windows.net/archive/path"
	job.NotePath, job.Entries, job.Bytes, job.IsDir = filepath.Join(dir, ".gdu-blob-transfer.txt"), entries, total, true
	if err := job.save(); err != nil {
		t.Fatal(err)
	}
	// Simulate a process exit after the first file was removed.
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if err := finishBlobJob(&job); err != nil {
		t.Fatal(err)
	}
	if job.Stage != "completed" {
		t.Fatalf("stage = %s", job.Stage)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatalf("second file remains: %v", err)
	}
	data, err := os.ReadFile(job.NotePath)
	if err != nil || !strings.Contains(string(data), job.Destination) {
		t.Fatalf("note missing destination: %v", err)
	}
	stored, err := loadBlobJob(job.ID)
	if err != nil || stored.Stage != "completed" {
		t.Fatalf("stored job: %v, %v", stored.Stage, err)
	}
}

func TestBlobCleanupStopsForChangedFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, total, err := snapshotLocal(path)
	if err != nil {
		t.Fatal(err)
	}
	job, err := newBlobJob()
	if err != nil {
		t.Fatal(err)
	}
	job.Stage, job.Source, job.Destination = "uploaded", path, "https://example.blob.core.windows.net/archive/sample.txt"
	job.NotePath, job.Entries, job.Bytes = path+".gdu-blob-transfer.txt", entries, total
	if err := os.WriteFile(path, []byte("new data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := finishBlobJob(&job); err == nil {
		t.Fatal("changed local file was deleted")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "new data" {
		t.Fatalf("source changed: %q, %v", data, err)
	}
}

func TestBlobCleanupCreatesSymlinkBesideNote(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dir := t.TempDir()
	source := filepath.Join(dir, "data")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "file.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	entries, total, err := snapshotLocal(source)
	if err != nil {
		t.Fatal(err)
	}
	job, err := newBlobJob()
	if err != nil {
		t.Fatal(err)
	}
	job.Stage, job.Source, job.Destination = "uploaded", source, "https://example.blob.core.windows.net/archive/data"
	job.LinkTarget, job.Mode = "/mnt/archive/data", "symlink"
	job.NotePath, job.Entries, job.Bytes, job.IsDir = source+".gdu-blob-transfer.txt", entries, total, true
	if err := finishBlobJob(&job); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(source)
	if err != nil || link != job.LinkTarget {
		t.Fatalf("link = %q, %v", link, err)
	}
	if _, err := os.Stat(job.NotePath); err != nil {
		t.Fatal(err)
	}
	job.Stage = "cleaning" // Simulate a crash after the rename but before the journal update.
	if err := finishBlobJob(&job); err != nil {
		t.Fatal(err)
	}
}

func TestAzcopySkippedTransferCannotAuthorizeCleanup(t *testing.T) {
	summary, _ := json.Marshal(map[string]string{
		"JobStatus": "CompletedWithSkipped", "FileTransfers": "1", "TransfersFailed": "0",
		"TransfersSkipped": "1", "TotalBytesTransferred": "0",
	})
	output, _ := json.Marshal(map[string]string{"MessageType": "EndOfJob", "MessageContent": string(summary)})
	if err := completedAzcopyJob(output, 1, 8); err == nil {
		t.Fatal("skipped transfer was accepted")
	}
}
