package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
	"github.com/warsmite/gamejanitor/validate"
)

func TestSettings_GetDefaults(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// auth_enabled defaults to false
	assert.False(t, svc.SettingsSvc.GetBool(service.SettingAuthEnabled))

	// port_range_start defaults to 27000
	assert.Equal(t, 27000, svc.SettingsSvc.GetInt(service.SettingPortRangeStart))

	// max_backups defaults to 10
	assert.Equal(t, 10, svc.SettingsSvc.GetInt(service.SettingMaxBackups))
}

func TestSettings_SetAndGet_Bool(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	assert.False(t, svc.SettingsSvc.GetBool(service.SettingAuthEnabled))

	err := svc.SettingsSvc.Set(service.SettingAuthEnabled, true)
	require.NoError(t, err)

	assert.True(t, svc.SettingsSvc.GetBool(service.SettingAuthEnabled))
}

func TestSettings_SetAndGet_Int(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Must set end first since 30000 > default end of 28999
	err := svc.SettingsSvc.Set(service.SettingPortRangeEnd, 31000)
	require.NoError(t, err)

	err = svc.SettingsSvc.Set(service.SettingPortRangeStart, 30000)
	require.NoError(t, err)

	assert.Equal(t, 30000, svc.SettingsSvc.GetInt(service.SettingPortRangeStart))
}

func TestSettings_SetAndGet_String(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	err := svc.SettingsSvc.Set(service.SettingConnectionAddress, "192.168.1.100")
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.100", svc.SettingsSvc.GetString(service.SettingConnectionAddress))
}

func TestSettings_Persistence(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	log := testutil.TestLogger()

	// Set a value with one service instance
	svc1 := service.NewSettingsService(db, log)
	err := svc1.Set(service.SettingAuthEnabled, true)
	require.NoError(t, err)

	// Create a new service instance on the same DB — value should persist
	svc2 := service.NewSettingsService(db, log)
	assert.True(t, svc2.GetBool(service.SettingAuthEnabled))
}

func TestSettings_ApplyConfig(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	svc.SettingsSvc.ApplyConfig(map[string]any{
		"auth_enabled":     true,
		"port_range_start": 25000,
	})

	assert.True(t, svc.SettingsSvc.GetBool(service.SettingAuthEnabled))
	assert.Equal(t, 25000, svc.SettingsSvc.GetInt(service.SettingPortRangeStart))
}

func TestSettings_Validation_RejectsInvalidPort(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	err := svc.SettingsSvc.Set(service.SettingPortRangeStart, -1)
	require.Error(t, err)
	var fe validate.FieldErrors
	assert.ErrorAs(t, err, &fe)
	assert.Contains(t, err.Error(), "must be between 1 and 65535")

	// Value should not have changed
	assert.Equal(t, 27000, svc.SettingsSvc.GetInt(service.SettingPortRangeStart))
}

func TestSettings_Validation_RejectsInvalidPortMode(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	err := svc.SettingsSvc.Set(service.SettingPortMode, "banana")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be one of")
}

func TestSettings_Validation_RejectsNegativeMaxBackups(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	err := svc.SettingsSvc.Set(service.SettingMaxBackups, -5)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be >= 0")
}

func TestSettings_Validation_RejectsPortRangeStartAboveEnd(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Default end is 28999, setting start to 29000 should fail
	err := svc.SettingsSvc.Set(service.SettingPortRangeStart, 29000)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be <= port_range_end")
}

func TestSettings_Validation_RejectsPortRangeEndBelowStart(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Default start is 27000, setting end to 26999 should fail
	err := svc.SettingsSvc.Set(service.SettingPortRangeEnd, 26999)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be >= port_range_start")
}

func TestSettings_BusinessMode_Defaults(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	log := testutil.TestLogger()

	svc := service.NewSettingsServiceWithMode(db, log, service.ModeBusiness)

	assert.True(t, svc.GetBool(service.SettingAuthEnabled))
	assert.False(t, svc.GetBool(service.SettingLocalhostBypass))
	assert.True(t, svc.GetBool(service.SettingRateLimitEnabled))
	assert.True(t, svc.GetBool(service.SettingRequireMemoryLimit))
	assert.True(t, svc.GetBool(service.SettingRequireCPULimit))
	assert.True(t, svc.GetBool(service.SettingRequireStorageLimit))

	// Non-overridden settings keep their base defaults
	assert.Equal(t, 27000, svc.GetInt(service.SettingPortRangeStart))
	assert.Equal(t, "business", svc.Mode())
}

func TestSettings_BusinessMode_OverridesStillWork(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	log := testutil.TestLogger()

	svc := service.NewSettingsServiceWithMode(db, log, service.ModeBusiness)

	// Business defaults auth to true, but operator can override to false
	err := svc.Set(service.SettingAuthEnabled, false)
	require.NoError(t, err)
	assert.False(t, svc.GetBool(service.SettingAuthEnabled))
}

func TestSettings_BusinessMode_ClearRevertsToBusinessDefault(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	log := testutil.TestLogger()

	svc := service.NewSettingsServiceWithMode(db, log, service.ModeBusiness)

	// Override auth to false
	err := svc.Set(service.SettingAuthEnabled, false)
	require.NoError(t, err)

	// Clear should revert to business default (true), not base default (false)
	err = svc.Clear(service.SettingAuthEnabled)
	require.NoError(t, err)
	assert.True(t, svc.GetBool(service.SettingAuthEnabled))
}

func TestSettings_DefaultMode_Unchanged(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	log := testutil.TestLogger()

	svc := service.NewSettingsServiceWithMode(db, log, service.ModeDefault)

	// Default mode should have newbie-friendly defaults
	assert.False(t, svc.GetBool(service.SettingAuthEnabled))
	assert.True(t, svc.GetBool(service.SettingLocalhostBypass))
	assert.False(t, svc.GetBool(service.SettingRateLimitEnabled))
	assert.Equal(t, "", svc.Mode())
}

func TestSettings_Validation_RejectsZeroRateLimit(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	err := svc.SettingsSvc.Set(service.SettingRateLimitPerIP, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be >= 1")
}
