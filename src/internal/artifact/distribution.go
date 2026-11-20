package artifact

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
)

var versionName = regexp.MustCompile("^[A-Za-z0-9_.-]+$")

const spigotBuildTools = "https://hub.spigotmc.org/jenkins/job/BuildTools/lastSuccessfulBuild/artifact/target/BuildTools.jar"

func DownloadServer(ctx context.Context, project, version, destination string, output, stderr io.Writer) error {
	if project == "buildtools" {
		return Publish(destination, func(path string) error {
			_, err := Download(ctx, path, spigotBuildTools, "")
			return err
		})
	}
	if !versionName.MatchString(version) || version == "." || version == ".." {
		return fmt.Errorf("specify the Minecraft version with -minecraft")
	}
	if project == "spigot" {
		dir, err := os.MkdirTemp("", "mcpkg-spigot-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		if _, err := Download(ctx, filepath.Join(dir, "BuildTools.jar"), spigotBuildTools, ""); err != nil {
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
	if err := ReadJSON(ctx, endpoint, &builds); err != nil {
		return err
	}
	var downloadURL, digest string
	var buildID int
	for _, build := range builds {
		file := build.Downloads["server:default"]
		if build.Channel == "STABLE" && build.ID > buildID && file.URL != "" && file.Checksums["sha256"] != "" {
			downloadURL = file.URL
			digest = "sha256:" + file.Checksums["sha256"]
			buildID = build.ID
		}
	}
	if buildID == 0 {
		return fmt.Errorf("no stable %s build for Minecraft %s", project, version)
	}
	return Publish(destination, func(path string) error {
		_, err := Download(ctx, path, downloadURL, digest)
		return err
	})
}
