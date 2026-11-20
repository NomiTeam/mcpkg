package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func publishJAR(destination string, write func(string) error) error {
	if !strings.HasSuffix(strings.ToLower(destination), ".jar") {
		return fmt.Errorf("output must have a .jar extension")
	}
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

	return publishJAR(destination, func(staged string) error {
		_, err := writeArtifact(staged, jar, "")
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

func repositoryBuild(ctx context.Context, repository, ref, jar, destination string, command []string, output, stderr io.Writer) error {
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
	resolved, err := filepath.EvalSymlinks(filepath.Join(checkout, jar))
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

func defaultBuildCommand(dir string) ([]string, error) {
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

const spigotBuildTools = "https://hub.spigotmc.org/jenkins/job/BuildTools/lastSuccessfulBuild/artifact/target/BuildTools.jar"

func downloadServer(ctx context.Context, project, version, destination string, output, stderr io.Writer) error {
	if project == "buildtools" {
		return publishJAR(destination, func(path string) error {
			_, err := downloadArtifact(ctx, path, artifact{URL: spigotBuildTools})
			return err
		})
	}
	if !sourcePart.MatchString(version) || version == "." || version == ".." {
		return fmt.Errorf("specify the Minecraft version with -minecraft")
	}
	if project == "spigot" {
		dir, err := os.MkdirTemp("", "mcpkg-spigot-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if _, err := downloadArtifact(ctx, filepath.Join(dir, "BuildTools.jar"), artifact{URL: spigotBuildTools}); err != nil {
			return err
		}
		outputDir := filepath.Join(dir, "output")
		if err := os.Mkdir(outputDir, 0700); err != nil {
			return err
		}
		if err := runBuildCommand(ctx, dir, output, stderr, "java", "-jar", "BuildTools.jar", "--nogui", "--rev", version, "--compile", "SPIGOT", "--output-dir", outputDir); err != nil {
			return err
		}
		jars, err := filepath.Glob(filepath.Join(outputDir, "spigot-*.jar"))
		if err != nil {
			return err
		}
		if len(jars) != 1 {
			return fmt.Errorf("BuildTools produced %d Spigot JARs; expected one", len(jars))
		}
		return copyJAR(jars[0], destination)
	}
	if project != "paper" && project != "folia" {
		return fmt.Errorf("unknown server distribution %q", project)
	}

	var builds []struct {
		ID        int
		Channel   string
		Downloads map[string]struct {
			URL       string
			Checksums map[string]string
		}
	}
	endpoint := "https://fill.papermc.io/v3/projects/" + project + "/versions/" + url.PathEscape(version) + "/builds"
	if err := getJSON(ctx, endpoint, &builds); err != nil {
		return err
	}
	var selected artifact
	var buildID int
	for _, build := range builds {
		file := build.Downloads["server:default"]
		if build.Channel == "STABLE" && build.ID > buildID && file.URL != "" && file.Checksums["sha256"] != "" {
			selected = artifact{URL: file.URL, Digest: "sha256:" + file.Checksums["sha256"]}
			buildID = build.ID
		}
	}
	if buildID == 0 {
		return fmt.Errorf("no stable %s build for Minecraft %s", project, version)
	}
	return publishJAR(destination, func(path string) error {
		_, err := downloadArtifact(ctx, path, selected)
		return err
	})
}
