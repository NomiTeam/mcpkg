package main

import (
	"bytes"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testTransport func(*http.Request) (*http.Response, error)

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestManagedPluginLifecycle(t *testing.T) {
	root := t.TempDir()
	server := newServer(root)
	if err := os.Mkdir(server.pluginsPath, 0700); err != nil {
		t.Fatal(err)
	}
	jars := map[string][]byte{
		"base": pluginJAR(t, "plugin.yml", "name: Base\nversion: 1\n"),
		"app1": pluginJAR(t, "paper-plugin.yml", "name: Example\nversion: 1\n"),
		"app2": pluginJAR(t, "paper-plugin.yml", "name: Example\nversion: 2\n"),
	}
	latest := "app1"
	badDigest := false
	client := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = client })
	http.DefaultClient = &http.Client{Transport: testTransport(func(request *http.Request) (*http.Response, error) {
		var payload []byte
		switch request.URL.Path {
		case "/v2/project/base/version", "/v2/project/baseID/version", "/v2/project/example/version":
			id := latest
			number := strings.TrimPrefix(id, "app")
			projectID := "exampleID"
			var required []map[string]string
			if strings.Contains(request.URL.Path, "base") {
				id, number, projectID = "base", "1", "baseID"
			} else {
				required = append(required, map[string]string{"project_id": "baseID", "dependency_type": "required"})
			}
			digest := fmt.Sprintf("%x", sha512.Sum512(jars[id]))
			if badDigest && id == "app2" {
				digest = strings.Repeat("0", 128)
			}
			var err error
			payload, err = json.Marshal([]map[string]any{{
				"id": id, "project_id": projectID, "version_number": number, "version_type": "release",
				"dependencies": required, "files": []map[string]any{{
					"url": "https://artifacts.example/" + id, "filename": id + ".jar", "primary": true,
					"hashes": map[string]string{"sha512": digest},
				}},
			}})
			if err != nil {
				return nil, err
			}
		default:
			payload = jars[strings.TrimPrefix(request.URL.Path, "/")]
			if payload == nil {
				return nil, fmt.Errorf("unexpected request %s", request.URL)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Request: request, Body: io.NopCloser(bytes.NewReader(payload))}, nil
	})}

	runCLI := func(wantError string, args ...string) string {
		t.Helper()
		var output, stderr bytes.Buffer
		err := run(append([]string{"-server", root, "-minecraft", "1.21.11"}, args...), &output, &stderr)
		if wantError == "" && err != nil || wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError)) {
			t.Fatalf("%v: got %v, want %q (%s)", args, err, wantError, output.String())
		}
		return output.String()
	}

	runCLI("", "install", "base")
	runCLI("", "install", "example")
	runCLI("requires modrinth:baseid", "remove", "base")
	runCLI("Example requires Base", "disable", "mcpkg-modrinth-base.jar")
	runCLI("", "disable", "mcpkg-modrinth-example.jar")

	latest = "app2"
	badDigest = true
	runCLI("", "update")
	if output := runCLI("", "list"); !strings.Contains(strings.Join(strings.Fields(output), " "), "modrinth:example 1 2") {
		t.Fatalf("list did not display the new version: %s", output)
	}
	runCLI("artifact checksum mismatch", "upgrade")
	disabled := filepath.Join(server.pluginsPath, "mcpkg-modrinth-example.jar.disabled")
	data, err := os.ReadFile(disabled)
	if err != nil || !bytes.Equal(data, jars["app1"]) {
		t.Fatalf("failed upgrade changed the JAR: %v", err)
	}
	badDigest = false
	runCLI("", "-dry-run", "upgrade")
	metadata, err := inspectJAR(disabled)
	if err != nil || metadata.Version != "1" {
		t.Fatalf("preview changed the JAR: %v %v", metadata, err)
	}
	runCLI("", "upgrade", "example")
	metadata, err = inspectJAR(disabled)
	if err != nil || metadata.Version != "2" {
		t.Fatalf("upgrade did not replace the disabled JAR: %v %v", metadata, err)
	}
	runCLI("", "disable", "mcpkg-modrinth-base.jar")
	runCLI("Example requires Base", "enable", "mcpkg-modrinth-example.jar")
	runCLI("", "enable", "mcpkg-modrinth-base.jar")
	runCLI("", "enable", "mcpkg-modrinth-example.jar")
	runCLI("", "check")

	config := filepath.Join(server.pluginsPath, "Example")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	runCLI("", "remove", "example")
	runCLI("", "remove", "base")
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("remove deleted the plugin data directory: %v", err)
	}
	files, err := server.plugins()
	if err != nil || len(files) != 0 {
		t.Fatalf("JAR files remain after remove: %v %v", files, err)
	}
}

func TestCommitRestoresReplacedJAROnCollision(t *testing.T) {
	server := newServer(t.TempDir())
	staging := filepath.Join(server.pluginsPath, ".staging")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(server.pluginsPath, "example.jar")
	replacement := filepath.Join(staging, "replacement")
	collision := filepath.Join(server.pluginsPath, "collision.jar")
	for filename, contents := range map[string]string{
		installed: "original", replacement: "replacement", collision: "existing file",
	} {
		if err := os.WriteFile(filename, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan := packagePlan{server: server, staging: staging, changes: []jarChange{
		{before: "example.jar", after: pluginFile{filename: "example.jar"}, staged: replacement},
		{after: pluginFile{filename: "collision.jar"}, staged: filepath.Join(staging, "unused")},
	}}
	if err := plan.commit(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("commit returned %v, want a destination conflict", err)
	}
	for filename, expected := range map[string]string{installed: "original", collision: "existing file"} {
		data, err := os.ReadFile(filename)
		if err != nil || string(data) != expected {
			t.Errorf("%s = %q, %v; want %q", filename, data, err, expected)
		}
	}
	if _, err := os.Stat(server.statePath()); !os.IsNotExist(err) {
		t.Fatalf("state was changed despite failed commit: %v", err)
	}
}
