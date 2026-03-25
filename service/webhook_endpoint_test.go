package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestWebhookEndpoint_Create_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	log := testutil.TestLogger()
	whSvc := service.NewWebhookEndpointService(svc.DB, log)

	result, err := whSvc.Create("https://example.com/hook", "my hook", "secret", []string{"*"}, true)
	require.NoError(t, err)
	assert.NotEmpty(t, result.Endpoint.ID)
	assert.Equal(t, "https://example.com/hook", result.Endpoint.URL)
	assert.True(t, result.Endpoint.SecretSet)
	assert.True(t, result.Endpoint.Enabled)
	assert.Equal(t, []string{"*"}, result.Endpoint.Events)
}

func TestWebhookEndpoint_Create_EmptyEvents_DefaultsToWildcard(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	result, err := whSvc.Create("https://example.com/hook", "", "", nil, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, result.Endpoint.Events)
}

func TestWebhookEndpoint_Create_InvalidURL_Rejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	_, err := whSvc.Create("not-a-url", "", "", nil, true)
	require.Error(t, err)
}

func TestWebhookEndpoint_Create_EmptyURL_Rejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	_, err := whSvc.Create("", "", "", nil, true)
	require.Error(t, err)
}

func TestWebhookEndpoint_Create_InvalidEventFilter_Rejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	// Invalid glob pattern
	_, err := whSvc.Create("https://example.com/hook", "", "", []string{"[invalid"}, true)
	require.Error(t, err)
}

func TestWebhookEndpoint_SecretHidden_InView(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	result, err := whSvc.Create("https://example.com/hook", "", "my-secret", nil, true)
	require.NoError(t, err)

	// The view should indicate a secret is set but not expose it
	assert.True(t, result.Endpoint.SecretSet)

	// Get should also hide it
	fetched, err := whSvc.Get(result.Endpoint.ID)
	require.NoError(t, err)
	assert.True(t, fetched.SecretSet)
}

func TestWebhookEndpoint_Update(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	result, err := whSvc.Create("https://example.com/hook", "original", "", nil, true)
	require.NoError(t, err)

	newDesc := "updated"
	updated, err := whSvc.Update(result.Endpoint.ID, &newDesc, nil, nil, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "updated", updated.Description)
	assert.Equal(t, "https://example.com/hook", updated.URL) // URL unchanged
}

func TestWebhookEndpoint_Delete(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	result, err := whSvc.Create("https://example.com/hook", "", "", nil, true)
	require.NoError(t, err)

	require.NoError(t, whSvc.Delete(result.Endpoint.ID))

	_, err = whSvc.Get(result.Endpoint.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestWebhookEndpoint_Delete_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	err := whSvc.Delete("nonexistent")
	require.Error(t, err)
}

func TestWebhookEndpoint_List(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	whSvc.Create("https://a.com/hook", "", "", nil, true)
	whSvc.Create("https://b.com/hook", "", "", nil, true)

	list, err := whSvc.List()
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestWebhookEndpoint_Get_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	whSvc := service.NewWebhookEndpointService(svc.DB, testutil.TestLogger())

	_, err := whSvc.Get("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
