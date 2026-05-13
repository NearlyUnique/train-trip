package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_Defaults(t *testing.T) {
	os.Unsetenv("PORT")
	os.Unsetenv("DATA_URL")
	os.Unsetenv("RTT_TOKEN")
	cfg := loadConfig()
	assert.Equal(t, ":8089", cfg.addr)
	assert.Equal(t, "https://data.rtt.io", cfg.dataURL)
}

func TestLoadConfig_FromEnv(t *testing.T) {
	os.Setenv("PORT", "9090")
	os.Setenv("DATA_URL", "https://custom.url")
	os.Setenv("RTT_TOKEN", "mytoken")
	defer os.Unsetenv("PORT")
	defer os.Unsetenv("DATA_URL")
	defer os.Unsetenv("RTT_TOKEN")
	cfg := loadConfig()
	assert.Equal(t, ":9090", cfg.addr)
	assert.Equal(t, "https://custom.url", cfg.dataURL)
	assert.Equal(t, "mytoken", cfg.rttToken)
}

func TestLoadDotEnv(t *testing.T) {
	f, err := os.CreateTemp("", "env")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	f.WriteString("DOTENV_A=hello\n# comment\n\nINVALID\nDOTENV_B = world \n")
	f.Close()

	os.Unsetenv("DOTENV_A")
	os.Unsetenv("DOTENV_B")
	loadDotEnv(f.Name())

	assert.Equal(t, "hello", os.Getenv("DOTENV_A"))
	assert.Equal(t, "world", os.Getenv("DOTENV_B"))
}

func TestLoadDotEnv_DoesNotOverwrite(t *testing.T) {
	f, err := os.CreateTemp("", "env")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	f.WriteString("DOTENV_C=from-file\n")
	f.Close()

	os.Setenv("DOTENV_C", "already-set")
	defer os.Unsetenv("DOTENV_C")
	loadDotEnv(f.Name())

	assert.Equal(t, "already-set", os.Getenv("DOTENV_C"))
}

func TestLoadDotEnv_MissingFile(t *testing.T) {
	// Should silently do nothing
	loadDotEnv("/no/such/file.env")
}
