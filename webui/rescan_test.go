package webui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRescanUpdatesTreeWithoutRestart(t *testing.T) {
	ui := newTestUI()
	root := makeTree(t)
	scan(t, ui, root)
	oldPath := filepath.Join(root, "small.txt")
	newPath := filepath.Join(root, "new.txt")
	require.NoError(t, os.Remove(oldPath))
	require.NoError(t, os.WriteFile(newPath, []byte("new"), 0600))

	srv := httptest.NewServer(ui.routes())
	defer srv.Close()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/rescan", nil)
	require.NoError(t, err)
	req.Header.Set(actionHeader, ui.actionToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	deadline := time.Now().Add(5 * time.Second)
	for ui.buildStatus().State != scanStateDone && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, scanStateDone, ui.buildStatus().State)
	require.EqualValues(t, 1, ui.buildStatus().ScanVersion)
	_, err = ui.findNode(oldPath)
	require.ErrorIs(t, err, errNotFound)
	_, err = ui.findNode(newPath)
	require.NoError(t, err)
}

func TestRescanRejectsConcurrentActionAndReports(t *testing.T) {
	ui := newTestUI()
	root := makeTree(t)
	scan(t, ui, root)
	srv := httptest.NewServer(ui.routes())
	defer srv.Close()

	post := func(token string) int {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/rescan", nil)
		require.NoError(t, err)
		req.Header.Set(actionHeader, token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		return resp.StatusCode
	}
	require.Equal(t, http.StatusForbidden, post(""))
	ui.actionMu.Lock()
	require.Equal(t, http.StatusConflict, post(ui.actionToken))
	ui.actionMu.Unlock()

	require.NoError(t, ui.ReadAnalysis(strings.NewReader(sampleReport)))
	require.Equal(t, http.StatusConflict, post(ui.actionToken))
}
