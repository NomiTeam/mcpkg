package manager

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NomiTeam/mcpkg/internal/source"
)

type testTransport func(*http.Request) (*http.Response, error)

func (transport testTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestManagedPluginLifecycle(t *testing.T) {
	root := t.TempDir()
	server := NewServer(root)
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

	runCommand := func(wantError string, args ...string) string {
		t.Helper()
		var output, stderr bytes.Buffer
		var err error
		preview := args[0] == "-dry-run"
		if preview {
			args = args[1:]
		}
		switch args[0] {
		case "enable", "disable":
			_, err = server.SetEnabled(args[1], args[0] == "enable")
		case "list":
			var plugins []PluginStatus
			plugins, err = server.List()
			for _, plugin := range plugins {
				fmt.Fprintln(&output, plugin.Source, plugin.Version, plugin.Available)
			}
		case "check":
			var problems []string
			problems, err = server.Check()
			if len(problems) != 0 {
				err = fmt.Errorf("check: %v", problems)
			}
		default:
			argument := ""
			if len(args) == 2 {
				argument = args[1]
			}
			err = server.Manage(context.Background(), args[0], argument, source.Catalog{Loader: "paper", Minecraft: "1.21.11"}, preview, &output, &stderr)
		}
		if wantError == "" && err != nil || wantError != "" && (err == nil || !strings.Contains(err.Error(), wantError)) {
			t.Fatalf("%v: got %v, want %q (%s)", args, err, wantError, output.String())
		}
		return output.String()
	}

	runCommand("", "install", "base")
	runCommand("", "install", "example")
	runCommand("requires modrinth:baseid", "remove", "base")
	runCommand("Example requires Base", "disable", "mcpkg-modrinth-base.jar")
	runCommand("", "disable", "mcpkg-modrinth-example.jar")

	latest = "app2"
	badDigest = true
	runCommand("", "update")
	if output := runCommand("", "list"); !strings.Contains(strings.Join(strings.Fields(output), " "), "modrinth:example 1 2") {
		t.Fatalf("list did not display the new version: %s", output)
	}
	runCommand("artifact checksum mismatch", "upgrade")
	disabled := filepath.Join(server.pluginsPath, "mcpkg-modrinth-example.jar.disabled")
	data, err := os.ReadFile(disabled)
	if err != nil || !bytes.Equal(data, jars["app1"]) {
		t.Fatalf("failed upgrade changed the JAR: %v", err)
	}
	badDigest = false
	runCommand("", "-dry-run", "upgrade")
	metadata, err := inspectJAR(disabled)
	if err != nil || metadata.Version != "1" {
		t.Fatalf("preview changed the JAR: %v %v", metadata, err)
	}
	runCommand("", "upgrade", "example")
	metadata, err = inspectJAR(disabled)
	if err != nil || metadata.Version != "2" {
		t.Fatalf("upgrade did not replace the disabled JAR: %v %v", metadata, err)
	}
	runCommand("", "disable", "mcpkg-modrinth-base.jar")
	runCommand("Example requires Base", "enable", "mcpkg-modrinth-example.jar")
	runCommand("", "enable", "mcpkg-modrinth-base.jar")
	runCommand("", "enable", "mcpkg-modrinth-example.jar")
	runCommand("", "check")

	config := filepath.Join(server.pluginsPath, "Example")
	if err := os.Mkdir(config, 0700); err != nil {
		t.Fatal(err)
	}
	runCommand("", "remove", "example")
	runCommand("", "remove", "base")
	if _, err := os.Stat(config); err != nil {
		t.Fatalf("remove deleted the plugin data directory: %v", err)
	}
	files, err := server.plugins()
	if err != nil || len(files) != 0 {
		t.Fatalf("JAR files remain after remove: %v %v", files, err)
	}
}

func TestCommitRestoresReplacedJAROnCollision(t *testing.T) {
	server := NewServer(t.TempDir())
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
