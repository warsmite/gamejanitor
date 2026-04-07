package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestFiles_PathTraversal_Rejected(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	gs := testutil.CreateTestGameserver(t, api.Services)

	resp, err := http.Get(api.Server.URL + "/api/gameservers/" + gs.ID + "/files?path=../../etc/passwd")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result apiErrorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Contains(t, result.Error, "must be within /data")
}

func TestFiles_List_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	gs := testutil.CreateTestGameserver(t, api.Services)

	resp, err := http.Get(api.Server.URL + "/api/gameservers/" + gs.ID + "/files?path=/data")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
}

func TestFiles_Read_NotFound(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	gs := testutil.CreateTestGameserver(t, api.Services)

	resp, err := http.Get(api.Server.URL + "/api/gameservers/" + gs.ID + "/files/content?path=/data/nonexistent.txt")
	require.NoError(t, err)
	defer resp.Body.Close()

	// File doesn't exist on the volume — service returns an error
	assert.NotEqual(t, http.StatusOK, resp.StatusCode)
}

func TestFiles_Write_SizeLimit(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	gs := testutil.CreateTestGameserver(t, api.Services)

	// Sending more than MaxFileWriteBytes (10 MB) should be rejected with 413.
	bigBody := strings.Repeat("A", 11*1024*1024) // 11 MB

	req, err := http.NewRequest("PUT", api.Server.URL+"/api/gameservers/"+gs.ID+"/files/content?path=/data/big.txt", bytes.NewReader([]byte(bigBody)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusRequestEntityTooLarge, resp.StatusCode)
}
