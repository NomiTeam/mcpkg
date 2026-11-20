package manager

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func pluginJAR(t *testing.T, descriptor, yaml string) []byte {
	t.Helper()
	var jar bytes.Buffer
	writer := zip.NewWriter(&jar)
	file, err := writer.Create(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(yaml)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return jar.Bytes()
}

func TestSetEnabled(t *testing.T) {
	// Keep a non-ASCII filename to exercise renames on different filesystems.
	tests := []struct {
		name   string
		source string
		target string
		enable bool
	}{
		{"disable", "Caf\u00e9.jar", "Caf\u00e9.jar.disabled", false},
		{"enable", "Caf\u00e9.jar.disabled", "Caf\u00e9.jar", true},
		{"enable legacy filename", "Caf\u00e9.jar.off", "Caf\u00e9.jar", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer(t.TempDir())
			if err := os.Mkdir(server.pluginsPath, 0700); err != nil {
				t.Fatal(err)
			}
			sourcePath := filepath.Join(server.pluginsPath, test.source)
			original := pluginJAR(t, "plugin.yml", "name: Example\nversion: 1\n")
			if err := os.WriteFile(sourcePath, original, 0600); err != nil {
				t.Fatal(err)
			}

			filename, err := server.SetEnabled("Caf\u00e9.jar", test.enable)
			if err != nil || filename != test.target {
				t.Fatalf("SetEnabled returned %q, %v; want %q", filename, err, test.target)
			}
			plugins, err := server.plugins()
			if err != nil || len(plugins) != 1 || plugins[0].enabled != test.enable {
				t.Fatalf("plugins returned %v, %v", plugins, err)
			}
			contents, err := os.ReadFile(filepath.Join(server.pluginsPath, filename))
			if err != nil || !bytes.Equal(contents, original) {
				t.Fatalf("renamed file contents = %q, %v", contents, err)
			}
		})
	}
}

func TestSetEnabledPreservesConflictingFiles(t *testing.T) {
	server := NewServer(t.TempDir())
	if err := os.Mkdir(server.pluginsPath, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"example.jar": "enabled file", "example.jar.disabled": "disabled file"}
	for filename, contents := range files {
		if err := os.WriteFile(filepath.Join(server.pluginsPath, filename), []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := server.SetEnabled("example.jar.disabled", true); err == nil {
		t.Fatal("enable succeeded even though the target already existed")
	}
	if _, err := server.SetEnabled("example.jar", false); err == nil {
		t.Fatal("disable succeeded even though the target already existed")
	}
	for filename, want := range files {
		contents, err := os.ReadFile(filepath.Join(server.pluginsPath, filename))
		if err != nil || string(contents) != want {
			t.Errorf("%s = %q, %v; want %q", filename, contents, err, want)
		}
	}
}
