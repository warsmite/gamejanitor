package validate

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFieldErrors_NilWhenEmpty(t *testing.T) {
	var fe FieldErrors
	assert.Nil(t, fe.Err())
}

func TestFieldErrors_SingleError(t *testing.T) {
	var fe FieldErrors
	fe.Add("name", "must not be empty")
	err := fe.Err()
	require.Error(t, err)
	assert.Equal(t, "name: must not be empty", err.Error())
}

func TestFieldErrors_MultipleErrors(t *testing.T) {
	var fe FieldErrors
	fe.Add("port", "too low")
	fe.Add("name", "required")
	err := fe.Err()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "port: too low")
	assert.Contains(t, err.Error(), "name: required")
}

func TestMinInt(t *testing.T) {
	var fe FieldErrors
	fe.MinInt("count", 5, 1)
	assert.Nil(t, fe.Err())

	fe.MinInt("count", 0, 1)
	require.Error(t, fe.Err())
	assert.Contains(t, fe.Err().Error(), "must be >= 1")
}

func TestRangeInt(t *testing.T) {
	var fe FieldErrors
	fe.RangeInt("port", 8080, 1, 65535)
	assert.Nil(t, fe.Err())

	fe.RangeInt("port", 0, 1, 65535)
	require.Error(t, fe.Err())
}

func TestOneOf(t *testing.T) {
	var fe FieldErrors
	fe.OneOf("mode", "auto", []string{"auto", "manual"})
	assert.Nil(t, fe.Err())

	fe.OneOf("mode", "banana", []string{"auto", "manual"})
	require.Error(t, fe.Err())
	assert.Contains(t, fe.Err().Error(), "must be one of")
	assert.Contains(t, fe.Err().Error(), "banana")
}

func TestNotEmpty(t *testing.T) {
	var fe FieldErrors
	fe.NotEmpty("name", "hello")
	assert.Nil(t, fe.Err())

	fe.NotEmpty("name", "")
	require.Error(t, fe.Err())
}

func TestPortNumber(t *testing.T) {
	var fe FieldErrors
	fe.PortNumber("port", 8080)
	assert.Nil(t, fe.Err())

	fe.PortNumber("port", 0)
	require.Error(t, fe.Err())

	var fe2 FieldErrors
	fe2.PortNumber("port", 70000)
	require.Error(t, fe2.Err())
}

func TestMinIntPtr(t *testing.T) {
	var fe FieldErrors
	fe.MinIntPtr("limit", nil, 0)
	assert.Nil(t, fe.Err())

	val := -1
	fe.MinIntPtr("limit", &val, 0)
	require.Error(t, fe.Err())
}

func TestMinFloatPtr(t *testing.T) {
	var fe FieldErrors
	fe.MinFloatPtr("cpu", nil, 0)
	assert.Nil(t, fe.Err())

	val := -0.5
	fe.MinFloatPtr("cpu", &val, 0)
	require.Error(t, fe.Err())
}

func TestMinFloat(t *testing.T) {
	var fe FieldErrors
	fe.MinFloat("cpu", 1.0, 0)
	assert.Nil(t, fe.Err())

	fe.MinFloat("cpu", -1.0, 0)
	require.Error(t, fe.Err())
}
