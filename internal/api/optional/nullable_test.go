package optional

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNullable_AbsentNullValue(t *testing.T) {
	type body struct {
		Secret Nullable[string] `json:"secret"`
	}

	// absent: Present=false。
	var absent body
	require.NoError(t, json.Unmarshal([]byte(`{}`), &absent))
	assert.False(t, absent.Secret.Present, "absent key は Present=false")
	assert.False(t, absent.Secret.IsNull())

	// explicit null: Present=true, Value=nil。
	var null body
	require.NoError(t, json.Unmarshal([]byte(`{"secret":null}`), &null))
	assert.True(t, null.Secret.Present)
	assert.Nil(t, null.Secret.Value)
	assert.True(t, null.Secret.IsNull(), "explicit null は IsNull=true")

	// value: Present=true, Value=&v。
	var val body
	require.NoError(t, json.Unmarshal([]byte(`{"secret":"x"}`), &val))
	assert.True(t, val.Secret.Present)
	require.NotNil(t, val.Secret.Value)
	assert.Equal(t, "x", *val.Secret.Value)
	assert.False(t, val.Secret.IsNull())
}

func TestNullable_InvalidValueErrors(t *testing.T) {
	type body struct {
		N Nullable[int] `json:"n"`
	}
	var b body
	assert.Error(t, json.Unmarshal([]byte(`{"n":"notint"}`), &b))
}
