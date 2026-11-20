package artifact

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Publish validates a staged JAR before exposing it at the requested path.
func Publish(destination string, write func(string) error) error {
	if !strings.HasSuffix(strings.ToLower(destination), ".jar") {
		return fmt.Errorf("output must have a .jar extension")
	}
	// Stage beside the destination so the final hard link stays on one filesystem.
	stage, err := os.MkdirTemp(filepath.Dir(destination), ".mcpkg-output-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)

	file := filepath.Join(stage, "artifact.jar")
	if err := write(file); err != nil {
		return err
	}
	jar, err := zip.OpenReader(file)
	if err != nil {
		return fmt.Errorf("read output as a JAR: %w", err)
	}
	if err := jar.Close(); err != nil {
		return err
	}
	// A hard link publishes the complete JAR without overwriting an existing file.
	if err := os.Link(file, destination); err != nil {
		return fmt.Errorf("publish %s: %w", destination, err)
	}
	return nil
}

func copyJAR(sourcePath, destination string) error {
	jar, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer jar.Close()

	return Publish(destination, func(staged string) error {
		_, err := Write(staged, jar, "")
		return err
	})
}

func runBuildCommand(ctx context.Context, dir string, output, stderr io.Writer, args ...string) error {
	name := args[0]
	if !filepath.IsAbs(name) && strings.ContainsAny(name, "/\\") {
		name = filepath.Join(dir, name)
	}
	command := exec.CommandContext(ctx, name, args[1:]...)
	command.Dir = dir
	command.Stdout, command.Stderr = output, stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run %s: %w", args[0], err)
	}
	return nil
}

// BuildRepository clones into a temporary checkout and publishes one matching JAR.
func BuildRepository(ctx context.Context, repository, ref, jar, destination string, command []string, output, stderr io.Writer) error {
	if !filepath.IsLocal(jar) || !strings.HasSuffix(jar, ".jar") {
		return fmt.Errorf("build artifact must be a relative JAR path inside the repository")
	}
	if _, err := os.Stat(repository); err == nil {
		repository, err = filepath.Abs(repository)
		if err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp("", "mcpkg-build-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	checkout := filepath.Join(dir, "source")
	if err := runBuildCommand(ctx, dir, output, stderr, "git", "clone", "--", repository, checkout); err != nil {
		return err
	}
	if ref != "" {
		resolveRef := func(reference string) ([]byte, error) {
			verify := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "--end-of-options", reference+"^{commit}")
			verify.Dir = checkout
			return verify.Output()
		}
		id, err := resolveRef(ref)
		if err != nil {
			id, err = resolveRef("refs/remotes/origin/" + ref)
		}
		if err != nil {
			return fmt.Errorf("resolve Git reference %q: %w", ref, err)
		}
		if err := runBuildCommand(ctx, checkout, output, stderr, "git", "checkout", "--detach", strings.TrimSpace(string(id))); err != nil {
			return err
		}
	}
	if len(command) == 0 {
		command, err = defaultBuildCommand(checkout)
		if err != nil {
			return err
		}
	}
	if err := runBuildCommand(ctx, checkout, output, stderr, command...); err != nil {
		return err
	}
	artifactPath, err := buildArtifactPath(checkout, jar)
	if err != nil {
		return err
	}
	// A generated symlink must still resolve inside the checkout.
	resolved, err := filepath.EvalSymlinks(artifactPath)
	if err != nil {
		return fmt.Errorf("locate built JAR %s: %w", jar, err)
	}
	root, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || !filepath.IsLocal(relative) {
		return fmt.Errorf("artifact %s points outside the repository", jar)
	}
	return copyJAR(resolved, destination)
}

func buildArtifactPath(checkout, pattern string) (string, error) {
	path := filepath.Join(checkout, pattern)
	// Prefer an existing literal filename even if it contains glob characters.
	if file, err := os.Stat(path); err == nil && file.Mode().IsRegular() {
		return path, nil
	}
	directory, filename := filepath.Split(path)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", fmt.Errorf("locate built JAR %s: %w", pattern, err)
	}
	match := ""
	for _, entry := range entries {
		matched, err := filepath.Match(filename, entry.Name())
		if err != nil {
			return "", fmt.Errorf("invalid artifact pattern %q: %w", pattern, err)
		}
		if !matched || entry.IsDir() {
			continue
		}
		if match != "" {
			return "", fmt.Errorf("more than one built JAR matches %q", pattern)
		}
		match = entry.Name()
	}
	if match == "" {
		return "", fmt.Errorf("no built JAR matches %q", pattern)
	}
	return filepath.Join(directory, match), nil
}

func defaultBuildCommand(dir string) ([]string, error) {
	// Prefer wrappers because they select the tool version recorded by the repository.
	for _, tool := range []struct {
		wrapper, batch string
		args           []string
	}{
		{"gradlew", "gradlew.bat", []string{"build"}},
		{"mvnw", "mvnw.cmd", []string{"package"}},
	} {
		wrapper := tool.wrapper
		if runtime.GOOS == "windows" {
			wrapper = tool.batch
		}
		if _, err := os.Stat(filepath.Join(dir, wrapper)); err == nil {
			if runtime.GOOS == "windows" {
				return []string{"cmd.exe", "/d", "/c", wrapper, tool.args[0]}, nil
			}
			return append([]string{"sh", "./" + wrapper}, tool.args...), nil
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "pom.xml")); err == nil {
		if runtime.GOOS == "windows" {
			return []string{"cmd.exe", "/d", "/c", "mvn", "package"}, nil
		}
		return []string{"mvn", "package"}, nil
	}
	for _, buildFile := range []string{"build.gradle", "build.gradle.kts"} {
		if _, err := os.Stat(filepath.Join(dir, buildFile)); err == nil {
			if runtime.GOOS == "windows" {
				return []string{"cmd.exe", "/d", "/c", "gradle", "build"}, nil
			}
			return []string{"gradle", "build"}, nil
		}
	}
	return nil, fmt.Errorf("no Gradle or Maven build detected; supply -- <build command> [args]")
}
