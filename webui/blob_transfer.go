package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dundee/gdu/v5/pkg/analyze"
	"github.com/dundee/gdu/v5/pkg/fs"
)

// Blob targets contain names and paths only. Authentication remains in Azure CLI.
type blobTarget struct {
	Name           string `json:"name"`
	Account        string `json:"account"`
	Container      string `json:"container"`
	DefaultPrefix  string `json:"defaultPrefix"`
	AzureConfigDir string `json:"azureConfigDir"`
	MountPath      string `json:"mountPath"`
}

var (
	blobAccountRE   = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	blobContainerRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,61}[a-z0-9])?$`)
)

func validateBlobTarget(t blobTarget) error {
	if strings.TrimSpace(t.Name) == "" || len(t.Name) > 80 {
		return errors.New("target name must contain 1–80 characters")
	}
	if !blobAccountRE.MatchString(t.Account) || !blobContainerRE.MatchString(t.Container) {
		return errors.New("invalid storage account or container name")
	}
	if _, err := cleanBlobPrefix(t.DefaultPrefix); err != nil {
		return err
	}
	if t.AzureConfigDir != "" && !filepath.IsAbs(t.AzureConfigDir) {
		return errors.New("Azure CLI configuration directory must be absolute")
	}
	if t.MountPath != "" && !filepath.IsAbs(t.MountPath) {
		return errors.New("mount path must be absolute")
	}
	return nil
}

func cleanBlobPrefix(prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	if strings.ContainsAny(prefix, "?#\\") {
		return "", errors.New("blob prefix cannot contain ?, # or backslashes")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == ".." || segment == "." {
			return "", errors.New("blob prefix cannot contain . or .. segments")
		}
	}
	return prefix, nil
}

func destinationPath(template, source string) (string, error) {
	clean, err := cleanBlobPrefix(template)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(source)
	if err != nil {
		return "", err
	}
	realPath, err = filepath.Abs(realPath)
	if err != nil {
		return "", err
	}
	relative := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(realPath)), "/")
	if strings.Contains(clean, "{path}") {
		return strings.ReplaceAll(clean, "{path}", relative), nil
	}
	if clean == "" {
		return filepath.Base(source), nil
	}
	return clean + "/" + filepath.Base(source), nil
}

func blobTargetFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "gdu", "blob-targets.json"), nil
}

func readBlobTargets() ([]blobTarget, error) {
	path, err := blobTargetFile()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []blobTarget{}, nil
	}
	if err != nil {
		return nil, err
	}
	var targets []blobTarget
	if err := json.Unmarshal(data, &targets); err != nil {
		return nil, fmt.Errorf("reading blob targets: %w", err)
	}
	return targets, nil
}

func saveBlobTargets(targets []blobTarget) error {
	seen := map[string]bool{}
	for _, target := range targets {
		if err := validateBlobTarget(target); err != nil {
			return fmt.Errorf("%s: %w", target.Name, err)
		}
		if seen[target.Name] {
			return fmt.Errorf("duplicate target name: %s", target.Name)
		}
		seen[target.Name] = true
	}
	path, err := blobTargetFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".blob-targets-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (ui *UI) handleBlobTargets(w http.ResponseWriter, r *http.Request) {
	if !isLocalRequest(r) {
		writeError(w, http.StatusForbidden, "Blob targets are only available locally")
		return
	}
	ui.actionMu.Lock()
	defer ui.actionMu.Unlock()
	if r.Method == http.MethodGet {
		targets, err := readBlobTargets()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, targets)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRevealBodyBytes)
	var targets []blobTarget
	if err := json.NewDecoder(r.Body).Decode(&targets); err != nil {
		writeError(w, http.StatusBadRequest, "invalid target list")
		return
	}
	if len(targets) > 100 {
		writeError(w, http.StatusBadRequest, "too many targets")
		return
	}
	if err := saveBlobTargets(targets); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type localEntry struct {
	Rel     string
	Size    int64
	Mode    os.FileMode
	ModTime time.Time
	Dir     bool
	Dev     uint64
	Ino     uint64
}

func snapshotLocal(path string) ([]localEntry, int64, error) {
	entries := []localEntry{}
	var total int64
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("unsupported symlink or special file: %s", p)
		}
		rel, err := filepath.Rel(path, p)
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		entries = append(entries, localEntry{rel, info.Size(), info.Mode(), info.ModTime(), info.IsDir(), uint64(stat.Dev), stat.Ino})
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return entries, total, err
}

func sameSnapshot(a, b []localEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func quoteBlobPath(parts ...string) string {
	segments := []string{}
	for _, part := range parts {
		for _, s := range strings.Split(strings.Trim(part, "/"), "/") {
			if s != "" {
				segments = append(segments, url.PathEscape(s))
			}
		}
	}
	return strings.Join(segments, "/")
}

func azcopyBinary() string {
	if path := os.Getenv("GDU_AZCOPY_PATH"); path != "" {
		return path
	}
	// The snap package cannot read AZURE_CONFIG_DIR outside its sandbox.
	if home, err := os.UserHomeDir(); err == nil {
		standalone := filepath.Join(home, ".local/opt/azcopy/azcopy")
		if info, err := os.Stat(standalone); err == nil && info.Mode()&0111 != 0 {
			return standalone
		}
	}
	return "azcopy"
}

type tailBuffer struct{ data []byte }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.data = append(b.data, p...)
	if len(b.data) > 128*1024 {
		b.data = append([]byte(nil), b.data[len(b.data)-128*1024:]...)
	}
	return n, nil
}

func completedAzcopyJob(output []byte, fileCount int, bytes int64) error {
	var end struct {
		MessageType    string `json:"MessageType"`
		MessageContent string `json:"MessageContent"`
	}
	for _, line := range strings.Split(string(output), "\n") {
		var event struct {
			MessageType    string `json:"MessageType"`
			MessageContent string `json:"MessageContent"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.MessageType == "EndOfJob" {
			end = event
		}
	}
	if end.MessageType != "EndOfJob" {
		return errors.New("AzCopy did not emit a final job summary")
	}
	var summary struct {
		JobStatus             string `json:"JobStatus"`
		FileTransfers         string `json:"FileTransfers"`
		TransfersFailed       string `json:"TransfersFailed"`
		TransfersSkipped      string `json:"TransfersSkipped"`
		TotalBytesTransferred string `json:"TotalBytesTransferred"`
	}
	if err := json.Unmarshal([]byte(end.MessageContent), &summary); err != nil {
		return err
	}
	files, err := strconv.Atoi(summary.FileTransfers)
	if err != nil {
		return err
	}
	transferred, err := strconv.ParseInt(summary.TotalBytesTransferred, 10, 64)
	if err != nil {
		return err
	}
	if summary.JobStatus != "Completed" || summary.TransfersFailed != "0" || summary.TransfersSkipped != "0" || files != fileCount || transferred != bytes {
		return fmt.Errorf("AzCopy summary did not match the local inventory (status=%s, files=%d/%d, bytes=%d/%d, failed=%s, skipped=%s)", summary.JobStatus, files, fileCount, transferred, bytes, summary.TransfersFailed, summary.TransfersSkipped)
	}
	return nil
}

func copyToBlob(ctx context.Context, source, destination string, target blobTarget, isDir bool, fileCount int, bytes int64, job *blobJob) error {
	args := []string{"copy", source, destination, "--from-to=LocalBlob", "--overwrite=false", "--check-length=true", "--output-type=json"}
	if isDir {
		args = append(args, "--recursive=true", "--as-subdir=false")
	}
	cmd := exec.CommandContext(ctx, azcopyBinary(), args...)
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
	if target.AzureConfigDir != "" {
		cmd.Env = append(cmd.Env, "AZURE_CONFIG_DIR="+target.AzureConfigDir)
	}
	output := &jobOutput{job: job}
	cmd.Stdout, cmd.Stderr = output, output
	err := cmd.Run()
	if err != nil {
		// AzCopy's output can list every file and may be enormous. Keep only a
		// short diagnostic; the complete job log remains in AzCopy's log dir.
		lines := strings.Split(strings.TrimSpace(string(output.data)), "\n")
		if len(lines) > 3 {
			lines = lines[len(lines)-3:]
		}
		return fmt.Errorf("AzCopy failed: %w: %s", err, strings.Join(lines, " "))
	}
	return completedAzcopyJob(output.data, fileCount, bytes)
}

func writeTransferNote(path, source, destination string, entries []localEntry, total int64) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Transferred from: %s\nAzure Blob destination: %s\nTransferred at (UTC): %s\nTotal file size: %d bytes\nFile count: %d\n\nFirst-level contents:\n", source, destination, time.Now().UTC().Format(time.RFC3339), total, countFiles(entries))
	shown := 0
	for _, e := range entries {
		if e.Rel == "." || strings.Contains(e.Rel, string(os.PathSeparator)) {
			continue
		}
		if shown >= 50 {
			b.WriteString("... more entries omitted\n")
			break
		}
		size := e.Size
		if e.Dir {
			size = 0
			for _, child := range entries {
				if !child.Dir && strings.HasPrefix(child.Rel, e.Rel+string(os.PathSeparator)) {
					size += child.Size
				}
			}
		}
		fmt.Fprintf(&b, "- %s%s (%d bytes)\n", e.Rel, map[bool]string{true: "/", false: ""}[e.Dir], size)
		shown++
	}
	if len(entries) == 1 {
		fmt.Fprintf(&b, "- %s (%d bytes)\n", filepath.Base(source), total)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := io.WriteString(f, b.String()); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func countFiles(entries []localEntry) int {
	n := 0
	for _, e := range entries {
		if !e.Dir {
			n++
		}
	}
	return n
}

func mountedBlobPath(target blobTarget, remotePath string) (string, error) {
	if target.MountPath == "" {
		return "", errors.New("this Blob target has no mount path configured")
	}
	mount, err := filepath.EvalSymlinks(target.MountPath)
	if err != nil {
		return "", fmt.Errorf("Blob mount is unavailable: %w", err)
	}
	cmd := exec.Command("findmnt", "-T", mount, "-n", "-o", "FSTYPE")
	output, err := cmd.Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(output)), "fuse") {
		return "", errors.New("Blob mount is not active; mount it before using symlink mode")
	}
	return filepath.Join(mount, filepath.FromSlash(remotePath)), nil
}

// Do not copy from a FUSE mount or descend into another filesystem. A local
// cleanup on a Blobfuse path would otherwise delete the remote copy too.
func checkLocalSource(path string) error {
	output, err := exec.Command("findmnt", "-T", path, "-n", "-o", "FSTYPE").Output()
	if err != nil {
		return fmt.Errorf("checking source filesystem: %w", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(output)), "fuse") {
		return errors.New("transfers from FUSE mounts are disabled")
	}
	root, err := os.Lstat(path)
	if err != nil {
		return err
	}
	device := root.Sys().(*syscall.Stat_t).Dev
	return filepath.WalkDir(path, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Sys().(*syscall.Stat_t).Dev != device {
			return fmt.Errorf("source crosses a filesystem boundary: %s", p)
		}
		return nil
	})
}

func removeTransferred(path string, entries []localEntry, isDir bool) error {
	// Remove only snapshotted entries, deepest first. os.Remove on directories
	// refuses to remove any newly added content.
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if isDir && e.Rel == "." {
			continue // Keep the selected top-level directory and its note.
		}
		p := path
		if e.Rel != "." {
			p = filepath.Join(path, e.Rel)
		}
		info, err := os.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue // Already removed before an interrupted cleanup.
		}
		if err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		if uint64(stat.Dev) != e.Dev || stat.Ino != e.Ino || info.IsDir() != e.Dir || info.Mode() != e.Mode || (!e.Dir && (info.Size() != e.Size || !info.ModTime().Equal(e.ModTime))) {
			return fmt.Errorf("source changed during cleanup: %s", p)
		}
		if err := os.Remove(p); err != nil {
			return err
		}
	}
	return nil
}

func (ui *UI) handleBlobTransfer(w http.ResponseWriter, r *http.Request) {
	ui.actionMu.Lock()
	defer ui.actionMu.Unlock()
	ui.mu.RLock()
	noDelete, scanning := ui.noDelete, ui.scanning
	ui.mu.RUnlock()
	if reason, status := ui.deleteDisabledReason(noDelete, scanning); reason != "" {
		writeError(w, status, reason)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRevealBodyBytes)
	var req struct {
		Path   string `json:"path"`
		Target string `json:"target"`
		Mode   string `json:"mode"`
		Prefix string `json:"prefix"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid transfer request")
		return
	}
	node, err := ui.findNode(req.Path)
	if err != nil {
		writeNodeError(w, err)
		return
	}
	parent := node.GetParent()
	if parent == nil || analyze.IsVirtualRootDir(parent) || isArchiveDescendant(node) {
		writeError(w, http.StatusBadRequest, "cannot transfer a scanned root or archive entry")
		return
	}
	if err := checkNotRelocated(node); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	targets, err := readBlobTargets()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	i := slices.IndexFunc(targets, func(t blobTarget) bool { return t.Name == req.Target })
	if i < 0 {
		writeError(w, http.StatusBadRequest, "unknown Blob target")
		return
	}
	target := targets[i]
	if req.Mode != "direct" && req.Mode != "symlink" {
		writeError(w, http.StatusBadRequest, "mode must be direct or symlink")
		return
	}
	path := node.GetPath()
	if err := checkLocalSource(path); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	template := req.Prefix
	if template == "" {
		template = target.DefaultPrefix
	}
	remotePath, err := destinationPath(template, path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var linkTarget string
	if req.Mode == "symlink" {
		linkTarget, err = mountedBlobPath(target, remotePath)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
	}
	entries, total, err := snapshotLocal(path)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if countFiles(entries) == 0 {
		writeError(w, http.StatusBadRequest, "empty directories have no files to transfer")
		return
	}
	notePath := path + ".gdu-blob-transfer.txt"
	if node.IsDir() && req.Mode == "direct" {
		notePath = filepath.Join(path, ".gdu-blob-transfer.txt")
	}
	if _, err := os.Lstat(notePath); err == nil {
		writeError(w, http.StatusConflict, "a transfer note already exists at this location")
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	destination := fmt.Sprintf("https://%s.blob.core.windows.net/%s/%s", target.Account, target.Container, quoteBlobPath(remotePath))
	job, err := newBlobJob()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	job.Source, job.Destination, job.NotePath = path, destination, notePath
	job.LinkTarget, job.Mode, job.Target = linkTarget, req.Mode, target
	job.Entries, job.Bytes, job.IsDir = entries, total, node.IsDir()
	if err := job.save(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := copyToBlob(context.Background(), path, destination, target, node.IsDir(), countFiles(entries), total, &job); err != nil {
		job.Stage, job.Error = "copy_failed", err.Error()
		_ = job.save()
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	after, _, err := snapshotLocal(path)
	if err != nil || !sameSnapshot(entries, after) {
		job.Stage, job.Error = "review_required", "source changed after upload"
		_ = job.save()
		writeError(w, http.StatusConflict, "source changed during transfer; Azure copy exists but local files were retained at "+path)
		return
	}
	job.Stage = "uploaded"
	if err := job.save(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := finishBlobJob(&job); err != nil {
		job.Error = err.Error()
		_ = job.save()
		writeError(w, http.StatusInternalServerError, "Azure copy exists; local cleanup needs review. Job "+job.ID+": "+err.Error())
		return
	}
	ui.refreshBlobParent(parent)
	writeJSON(w, http.StatusOK, map[string]any{"destination": destination, "notePath": notePath, "bytes": total, "mode": req.Mode, "jobId": job.ID})
}

func (ui *UI) refreshBlobParent(parent fs.Item) {
	// AnalyzeDir closes its progress channels. Reset them before reusing the
	// configured analyzer, while retaining scan options and filters.
	ui.Analyzer.ResetProgress()
	fresh := ui.Analyzer.AnalyzeDir(parent.GetPath(), ui.CreateIgnoreFunc(), ui.CreateFileTypeFilter())
	grand := parent.GetParent()
	fresh.SetParent(grand)
	if grand == nil {
		ui.mu.Lock()
		ui.topDir = fresh
		ui.mu.Unlock()
	} else {
		grand.RemoveFileByName(parent.GetName())
		grand.AddFile(fresh)
	}
	for current := fs.Item(fresh); current != nil; current = current.GetParent() {
		if ui.IsFilteringFiles() {
			current.UpdateStatsWithFileFiltering(ui.linkedItems)
		} else {
			current.UpdateStats(ui.linkedItems)
		}
	}
}
