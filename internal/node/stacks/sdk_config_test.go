package stacks

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/cli/cli/config"
)

// The SDK never reads the host's Docker config: not even Compose's dry
// run, which builds a CLI of its own with the default config. A
// DOCKER_CONFIG holding a login is ignored.
func TestSDKIgnoresTheHostsDockerConfig(t *testing.T) {
	poisoned := t.TempDir()
	if err := os.WriteFile(filepath.Join(poisoned, "config.json"), []byte(`{"auths":{"ghcr.io":{"auth":"aG9zdDpob3N0LXB3LWZha2U="}},"credsStore":"desktop"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", poisoned)
	if _, err := NewSDK("unix:///nonexistent.sock"); err != nil {
		t.Fatal(err)
	}
	if config.Dir() == poisoned {
		t.Fatal("the default config directory is still DOCKER_CONFIG's")
	}
	cf := config.LoadDefaultConfigFile(io.Discard)
	// docker/cli names the platform's own store when the file names none
	// (osxkeychain on a Mac): only the poisoned file's matters here.
	if len(cf.AuthConfigs) != 0 || cf.CredentialsStore == "desktop" {
		t.Fatalf("the default config has %d logins and store %q", len(cf.AuthConfigs), cf.CredentialsStore)
	}
}
