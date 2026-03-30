package steam

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVDF_Simple(t *testing.T) {
	input := `"appinfo"
{
	"appid"		"380870"
	"common"
	{
		"name"		"Project Zomboid Dedicated Server"
		"type"		"Tool"
	}
}`
	root, err := ParseVDF(input)
	require.NoError(t, err)

	appinfo := root.Child("appinfo")
	require.NotNil(t, appinfo)
	assert.Equal(t, "380870", appinfo.ChildValue("appid"))

	common := appinfo.Child("common")
	require.NotNil(t, common)
	assert.Equal(t, "Project Zomboid Dedicated Server", common.ChildValue("name"))
	assert.Equal(t, "Tool", common.ChildValue("type"))
}

func TestParseVDF_Nested(t *testing.T) {
	input := `"depots"
{
	"branches"
	{
		"public"
		{
			"buildid"		"12345"
		}
	}
}`
	root, err := ParseVDF(input)
	require.NoError(t, err)

	depots := root.Child("depots")
	require.NotNil(t, depots)

	branches := depots.Child("branches")
	require.NotNil(t, branches)

	public := branches.Child("public")
	require.NotNil(t, public)
	assert.Equal(t, "12345", public.ChildValue("buildid"))
}

func TestParseVDF_EscapedStrings(t *testing.T) {
	input := `"test" { "key" "value with \"quotes\"" }`
	root, err := ParseVDF(input)
	require.NoError(t, err)

	test := root.Child("test")
	require.NotNil(t, test)
	assert.Equal(t, `value with "quotes"`, test.ChildValue("key"))
}

func TestParseVDF_CaseInsensitiveLookup(t *testing.T) {
	input := `"Root" { "KeyName" "value" }`
	root, err := ParseVDF(input)
	require.NoError(t, err)

	node := root.Child("root")
	require.NotNil(t, node)
	assert.Equal(t, "value", node.ChildValue("keyname"))
}

func TestParseVDF_Empty(t *testing.T) {
	root, err := ParseVDF("")
	require.NoError(t, err)
	assert.Empty(t, root.Children)
}

func TestParseBinaryVDF_SimpleTypes(t *testing.T) {
	// Build a minimal binary VDF:
	// subtree "test" { string "name" = "hello", int32 "count" = 42, end }
	data := []byte{
		0x00,                                     // subtree type
		't', 'e', 's', 't', 0x00,                // key "test\0"
		0x01,                                     // string type
		'n', 'a', 'm', 'e', 0x00,                // key "name\0"
		'h', 'e', 'l', 'l', 'o', 0x00,           // value "hello\0"
		0x02,                                     // int32 type
		'c', 'o', 'u', 'n', 't', 0x00,           // key "count\0"
		0x2a, 0x00, 0x00, 0x00,                   // value 42 (LE)
		0x08,                                     // end subtree
		0x08,                                     // end root
	}

	root, err := ParseBinaryVDF(data)
	require.NoError(t, err)

	test := root.Child("test")
	require.NotNil(t, test)
	assert.Equal(t, "hello", test.ChildValue("name"))
	assert.Equal(t, "42", test.ChildValue("count"))
}
