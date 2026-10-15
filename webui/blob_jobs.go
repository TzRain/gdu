package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// A job file is private to the machine. It records the exact local inventory
// before upload so cleanup can be resumed without broad recursive deletion.
type blobJob struct {
	ID          string       `json:"id"`
	Stage       string       `json:"stage"`
	Error       string       `json:"error,omitempty"`
	Source      string       `json:"source"`
	Destination string       `json:"destination"`
	NotePath    string       `json:"notePath"`
	LinkTarget  string       `json:"linkTarget,omitempty"`
	Mode        string       `json:"mode"`
	Target      blobTarget   `json:"target"`
	Entries     []localEntry `json:"entries"`
	Bytes       int64        `json:"bytes"`
	IsDir       bool         `json:"isDir"`
	AzCopyID    string       `json:"azcopyJobId,omitempty"`
	UpdatedAt   time.Time    `json:"updatedAt"`
}

func blobJobsDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gdu", "blob-jobs"), nil
}

func newBlobJob() (blobJob, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return blobJob{}, err
	}
	return blobJob{ID: hex.EncodeToString(b[:]), Stage: "copying"}, nil
}

func (j *blobJob) save() error {
	dir, err := blobJobsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	j.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".job-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, j.ID+".json"))
}

func loadBlobJob(id string) (blobJob, error) {
	if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
		return blobJob{}, errors.New("invalid job ID")
	}
	dir, err := blobJobsDir()
	if err != nil {
		return blobJob{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return blobJob{}, err
	}
	var job blobJob
	if err := json.Unmarshal(data, &job); err != nil {
		return blobJob{}, err
	}
	if job.ID != id {
		return blobJob{}, errors.New("job ID mismatch")
	}
	return job, nil
}

func listBlobJobs() ([]blobJob, error) {
	dir, err := blobJobsDir()
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []blobJob{}, nil
	}
	if err != nil {
		return nil, err
	}
	jobs := make([]blobJob, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		job, err := loadBlobJob(strings.TrimSuffix(file.Name(), ".json"))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", file.Name(), err)
		}
		// The API needs a summary, not a potentially enormous file inventory.
		job.Entries = nil
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (ui *UI) handleBlobJobs(w http.ResponseWriter, r *http.Request) {
	if !isLocalRequest(r) {
		writeError(w, http.StatusForbidden, "jobs are only available locally")
		return
	}
	jobs, err := listBlobJobs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

var azcopyJobRE = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)

type jobOutput struct {
	tailBuffer
	first []byte
	job   *blobJob
}

func (o *jobOutput) Write(p []byte) (int, error) {
	if len(o.first) < 65536 {
		room := 65536 - len(o.first)
		if len(p) < room {
			room = len(p)
		}
		o.first = append(o.first, p[:room]...)
		if o.job.AzCopyID == "" && strings.Contains(strings.ToLower(string(o.first)), "job") {
			if id := azcopyJobRE.FindString(string(o.first)); id != "" {
				o.job.AzCopyID = id
				if err := o.job.save(); err != nil {
					return 0, err
				}
			}
		}
	}
	return o.tailBuffer.Write(p)
}

func ensureJobNote(job *blobJob) error {
	data, err := os.ReadFile(job.NotePath)
	if err == nil {
		if !strings.Contains(string(data), "Transferred from: "+job.Source+"\n") ||
			!strings.Contains(string(data), "Azure Blob destination: "+job.Destination+"\n") {
			return errors.New("transfer note changed; cleanup stopped")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeTransferNote(job.NotePath, job.Source, job.Destination, job.Entries, job.Bytes)
}

func finishBlobJob(job *blobJob) error {
	if job.Stage != "uploaded" && job.Stage != "cleaning" {
		return errors.New("upload has not been verified")
	}
	if err := ensureJobNote(job); err != nil {
		return err
	}
	job.Stage, job.Error = "cleaning", ""
	if err := job.save(); err != nil {
		return err
	}
	if job.Mode == "symlink" {
		if info, err := os.Lstat(job.Source); err == nil && info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(job.Source)
			if err != nil || link != job.LinkTarget {
				return errors.New("source is an unexpected symlink")
			}
			job.Stage = "completed"
			return job.save()
		}
	}
	var tempLink string
	if job.Mode == "symlink" {
		f, err := os.CreateTemp(filepath.Dir(job.Source), ".gdu-link-*")
		if err != nil {
			return err
		}
		tempLink = f.Name()
		if err := f.Close(); err != nil {
			return err
		}
		if err := os.Remove(tempLink); err != nil {
			return err
		}
		if err := os.Symlink(job.LinkTarget, tempLink); err != nil {
			return err
		}
		defer os.Remove(tempLink)
	}
	if err := removeTransferred(job.Source, job.Entries, job.IsDir); err != nil {
		return err
	}
	if job.Mode == "symlink" {
		if job.IsDir {
			if err := os.Remove(job.Source); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := os.Rename(tempLink, job.Source); err != nil {
			return err
		}
	}
	job.Stage = "completed"
	return job.save()
}

func resumeAzcopyJob(job *blobJob) error {
	if job.AzCopyID == "" {
		return errors.New("AzCopy job ID was not recorded; inspect AzCopy logs and the remote files manually")
	}
	cmd := exec.CommandContext(context.Background(), azcopyBinary(), "jobs", "resume", job.AzCopyID, "--output-type=json")
	cmd.Env = append(os.Environ(), "AZCOPY_AUTO_LOGIN_TYPE=AZCLI")
	azCLIPath := os.Getenv("GDU_AZ_CLI_PATH")
	if azCLIPath == "" {
		if _, err := os.Stat("/usr/bin/az"); err == nil {
			azCLIPath = "/usr/bin/az"
		}
	}
	if azCLIPath != "" {
		cmd.Env = append(cmd.Env, "AZCOPY_AZ_CLI_PATH="+azCLIPath)
	}
	if job.Target.AzureConfigDir != "" {
		cmd.Env = append(cmd.Env, "AZURE_CONFIG_DIR="+job.Target.AzureConfigDir)
	}
	output := &jobOutput{job: job}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("AzCopy resume failed: %w: %s", err, string(output.data))
	}
	return completedAzcopyJob(output.data, countFiles(job.Entries), job.Bytes)
}

func (ui *UI) handleResumeBlobJob(w http.ResponseWriter, r *http.Request) {
	ui.actionMu.Lock()
	defer ui.actionMu.Unlock()
	ui.mu.RLock()
	noDelete, scanning := ui.noDelete, ui.scanning
	ui.mu.RUnlock()
	if reason, status := ui.deleteDisabledReason(noDelete, scanning); reason != "" {
		writeError(w, status, reason)
		return
	}
	var req struct {
		ID            string `json:"id"`
		ConfirmDelete bool   `json:"confirmDelete"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRevealBodyBytes)
	if json.NewDecoder(r.Body).Decode(&req) != nil || !req.ConfirmDelete {
		writeError(w, http.StatusBadRequest, "job ID and explicit deletion confirmation are required")
		return
	}
	job, err := loadBlobJob(req.ID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if job.Stage == "completed" {
		writeJSON(w, http.StatusOK, job)
		return
	}
	if job.Stage == "copying" || job.Stage == "copy_failed" {
		if err := checkLocalSource(job.Source); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		current, _, err := snapshotLocal(job.Source)
		if err != nil || !sameSnapshot(current, job.Entries) {
			writeError(w, http.StatusConflict, "source changed; inspect the job and Azure copy manually")
			return
		}
		if err := resumeAzcopyJob(&job); err != nil {
			job.Stage, job.Error = "copy_failed", err.Error()
			_ = job.save()
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		job.Stage, job.Error = "uploaded", ""
		if err := job.save(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if job.Stage != "uploaded" && job.Stage != "cleaning" {
		writeError(w, http.StatusConflict, "job requires manual review before local deletion")
		return
	}
	if job.Mode == "symlink" {
		// The mount may have disappeared while the upload was running.
		if _, err := mountedBlobPath(job.Target, ""); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	if err := finishBlobJob(&job); err != nil {
		job.Error = err.Error()
		_ = job.save()
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if parent, err := ui.findNode(filepath.Dir(job.Source)); err == nil {
		ui.refreshBlobParent(parent)
	}
	writeJSON(w, http.StatusOK, job)
}
