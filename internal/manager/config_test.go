package manager

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NomiTeam/mcpkg/internal/source"
)

func TestConfiguredGitSourceUpgrade(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required to test configured Git sources")
	}
	server := NewServer(t.TempDir())
	repository := filepath.Join(server.root, "repository")
	sourceDirectory := filepath.Join(server.root, "sources.d")
	for _, dir := range []string{server.pluginsPath, repository, sourceDirectory} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeFile := func(filename string, contents []byte) {
		t.Helper()
		if err := os.WriteFile(filename, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	commit := func(version string) {
		t.Helper()
		writeFile(filepath.Join(repository, "plugin-"+version+".jar"), pluginJAR(t, "plugin.yml", "name: Example\nversion: "+version+"\n"))
		git("add", "-A")
		git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "update fixture")
	}

	git("init", "-qb", "main")
	commit("1")
	writeFile(filepath.Join(server.root, "mcpkg.yml"), []byte("loader: folia\nminecraft: \"26.3\"\n"))
	writeFile(filepath.Join(sourceDirectory, "example.yml"), []byte(
		"type: git\nrepository: repository\nref: main\nbuild:\n"+
			"  command: [git, log, -1, '--format=format:%h configured-build-ran']\n"+
			"  artifact: 'plugin-*.jar'\n"))

	config, err := server.LoadConfig()
	if err != nil || config.Loader != "folia" || config.Minecraft != "26.3" {
		t.Fatalf("load config: %+v, %v", config, err)
	}
	target := source.Catalog{Minecraft: config.Minecraft, Loader: config.Loader}
	run := func(command, argument string, preview bool) string {
		t.Helper()
		var output, stderr bytes.Buffer
		if err := server.Manage(context.Background(), command, argument, target, preview, &output, &stderr); err != nil {
			t.Fatalf("%s %s: %v\n%s\n%s", command, argument, err, &output, &stderr)
		}
		return output.String()
	}

	const buildMarker = "configured-build-ran"
	if output := run("install", "example", false); !strings.Contains(output, buildMarker) {
		t.Fatalf("install did not run the configured command: %s", output)
	}
	before, err := server.loadState()
	if err != nil {
		t.Fatal(err)
	}
	installed := before["source:example"]
	if !gitCommitID.MatchString(installed.ArtifactID) || installed.SHA256 == "" {
		t.Fatalf("install did not record the commit and checksum: %+v", installed)
	}
	if _, err := server.SetEnabled("source:example", false); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(repository, "plugin-1.jar")); err != nil {
		t.Fatal(err)
	}
	commit("2")
	if output := run("update", "", false); strings.Contains(output, buildMarker) || !strings.Contains(output, " -> ") {
		t.Fatalf("update should report the new commit without building: %s", output)
	}
	if output := run("upgrade", "example", true); strings.Contains(output, buildMarker) || !strings.Contains(output, "Build and install") {
		t.Fatalf("preview should show the planned build: %s", output)
	}
	filename := filepath.Join(server.pluginsPath, installed.JAR+disabledSuffix)
	metadata, err := inspectJAR(filename)
	if err != nil || metadata.Version != "1" {
		t.Fatalf("update or preview changed the JAR: %+v, %v", metadata, err)
	}
	if output := run("upgrade", "example", false); !strings.Contains(output, buildMarker) {
		t.Fatalf("upgrade did not run the configured command: %s", output)
	}
	metadata, err = inspectJAR(filename)
	if err != nil || metadata.Version != "2" {
		t.Fatalf("upgrade did not replace the disabled JAR: %+v, %v", metadata, err)
	}
	after, err := server.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if entry := after["source:example"]; entry.ArtifactID == installed.ArtifactID || entry.SHA256 == installed.SHA256 {
		t.Fatalf("upgrade did not update the commit and checksum: %+v", entry)
	}
}
