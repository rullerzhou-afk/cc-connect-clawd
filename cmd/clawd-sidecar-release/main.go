package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	defaultOutDir     = "dist/clawd-sidecar"
	defaultCmdPkg     = "./cmd/cc-connect-clawd"
	defaultBuildLimit = 5 * time.Minute
	binaryName        = "cc-connect-clawd"
	checksumsName     = "checksums.txt"
)

type targetSpec struct {
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
	GOOS     string `json:"goos"`
	GOARCH   string `json:"goarch"`
	Dir      string `json:"dir"`
	Exe      string `json:"exe"`
	Output   string `json:"output"`
	Archive  string `json:"archive,omitempty"`
}

type buildMetadata struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"buildTime"`
}

type manifest struct {
	OutDir    string        `json:"outDir"`
	CmdPkg    string        `json:"cmdPkg"`
	Metadata  buildMetadata `json:"metadata"`
	Checksums string        `json:"checksums"`
	Targets   []targetSpec  `json:"targets"`
}

type commandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type buildOptions struct {
	GoBin    string
	CmdPkg   string
	Metadata buildMetadata
	Target   targetSpec
	Timeout  time.Duration
	Runner   commandRunner
	Stdout   io.Writer
	Stderr   io.Writer
}

var supportedTargets = []targetSpec{
	{Platform: "windows", Arch: "x64", GOOS: "windows", GOARCH: "amd64"},
	{Platform: "windows", Arch: "arm64", GOOS: "windows", GOARCH: "arm64"},
	{Platform: "darwin", Arch: "x64", GOOS: "darwin", GOARCH: "amd64"},
	{Platform: "darwin", Arch: "arm64", GOOS: "darwin", GOARCH: "arm64"},
	{Platform: "linux", Arch: "x64", GOOS: "linux", GOARCH: "amd64"},
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("clawd-sidecar-release", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outDir := fs.String("out", defaultOutDir, "output directory for Clawd sidecar binaries")
	targetArg := fs.String("target", "all", "target to build: all or comma-separated platform-arch names")
	cmdPkg := fs.String("cmd", defaultCmdPkg, "Go package for the Clawd sidecar main command")
	goBin := fs.String("go", defaultGoBinary(), "go binary to execute")
	buildVersion := fs.String("build-version", "", "version string to inject into cc-connect-clawd")
	buildCommit := fs.String("build-commit", "", "commit string to inject into cc-connect-clawd")
	buildTime := fs.String("build-time", "", "build time string to inject into cc-connect-clawd")
	dryRun := fs.Bool("dry-run", false, "print the build manifest without building")
	if err := fs.Parse(args); err != nil {
		return err
	}

	targets, err := selectTargets(*targetArg)
	if err != nil {
		return err
	}
	metadata := discoverBuildMetadata(context.Background())
	if strings.TrimSpace(*buildVersion) != "" {
		metadata.Version = strings.TrimSpace(*buildVersion)
	}
	if strings.TrimSpace(*buildCommit) != "" {
		metadata.Commit = strings.TrimSpace(*buildCommit)
	}
	if strings.TrimSpace(*buildTime) != "" {
		metadata.BuildTime = strings.TrimSpace(*buildTime)
	}
	m := buildManifest(*outDir, *cmdPkg, metadata, targets)
	if *dryRun {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(m)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var releaseFiles []string
	for _, target := range m.Targets {
		if err := buildTarget(ctx, buildOptions{
			GoBin:    *goBin,
			CmdPkg:   *cmdPkg,
			Metadata: metadata,
			Target:   target,
			Runner:   execRunner{},
			Stdout:   stdout,
			Stderr:   stderr,
		}); err != nil {
			return err
		}
		archivePath, err := archiveTarget(target)
		if err != nil {
			return err
		}
		releaseFiles = append(releaseFiles, target.Output, archivePath)
	}
	if err := writeChecksums(m.OutDir, m.Checksums, releaseFiles); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Wrote %s\n", fromSlash(m.Checksums))
	return nil
}

func defaultGoBinary() string {
	if runtime.GOOS == "windows" {
		const installedGo = `C:\Program Files\Go\bin\go.exe`
		if _, err := os.Stat(installedGo); err == nil {
			return installedGo
		}
	}
	return "go"
}

func selectTargets(raw string) ([]targetSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return append([]targetSpec(nil), supportedTargets...), nil
	}
	byName := map[string]targetSpec{}
	for _, target := range supportedTargets {
		byName[target.DirName()] = target
	}
	seen := map[string]bool{}
	var out []targetSpec
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		target, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("unsupported target %q; expected one of: %s", name, strings.Join(targetNames(), ", "))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, target)
	}
	return out, nil
}

func targetNames() []string {
	names := make([]string, 0, len(supportedTargets)+1)
	names = append(names, "all")
	for _, target := range supportedTargets {
		names = append(names, target.DirName())
	}
	return names
}

func buildManifest(outDir, cmdPkg string, metadata buildMetadata, targets []targetSpec) manifest {
	cleanOut := toSlashPath(filepath.Clean(outDir))
	items := make([]targetSpec, 0, len(targets))
	for _, target := range targets {
		target.Dir = target.DirName()
		target.Exe = executableName(target.Platform)
		target.Output = slashJoin(cleanOut, target.Dir, target.Exe)
		target.Archive = slashJoin(cleanOut, archiveName(target))
		items = append(items, target)
	}
	return manifest{
		OutDir:    cleanOut,
		CmdPkg:    cmdPkg,
		Metadata:  metadata,
		Checksums: slashJoin(cleanOut, checksumsName),
		Targets:   items,
	}
}

func (t targetSpec) DirName() string {
	return t.Platform + "-" + t.Arch
}

func executableName(platform string) string {
	if platform == "windows" {
		return binaryName + ".exe"
	}
	return binaryName
}

func archiveName(target targetSpec) string {
	base := binaryName + "-" + target.DirName()
	if target.Platform == "windows" {
		return base + ".zip"
	}
	return base + ".tar.gz"
}

func buildTarget(ctx context.Context, opts buildOptions) error {
	if strings.TrimSpace(opts.GoBin) == "" {
		return errors.New("go binary path is required")
	}
	if opts.Runner == nil {
		opts.Runner = execRunner{}
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultBuildLimit
	}
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}

	outPath := fromSlash(opts.Target.Output)
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create output directory for %s: %w", opts.Target.Dir, err)
	}
	tmpPath := temporaryOutputPath(outPath)
	defer os.Remove(tmpPath)

	fmt.Fprintf(opts.Stdout, "Building %s -> %s\n", opts.Target.Dir, outPath)
	buildCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	args := []string{
		"build",
		"-trimpath",
		"-ldflags", ldflags(opts.Metadata),
		"-o", tmpPath,
		opts.CmdPkg,
	}
	if err := opts.Runner.Run(buildCtx, opts.GoBin, args, buildEnv(os.Environ(), opts.Target), opts.Stdout, opts.Stderr); err != nil {
		if buildCtx.Err() != nil {
			return fmt.Errorf("build %s canceled: %w", opts.Target.Dir, buildCtx.Err())
		}
		return fmt.Errorf("build %s: %w", opts.Target.Dir, err)
	}
	if err := replaceFile(tmpPath, outPath); err != nil {
		return fmt.Errorf("install %s: %w", opts.Target.Dir, err)
	}
	return nil
}

func temporaryOutputPath(outPath string) string {
	return fmt.Sprintf("%s.tmp-%d", outPath, os.Getpid())
}

func replaceFile(src, dst string) error {
	if runtime.GOOS == "windows" {
		if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(src, dst)
}

func ldflags(metadata buildMetadata) string {
	return strings.Join([]string{
		"-s",
		"-w",
		"-X", "main.version=" + metadata.Version,
		"-X", "main.commit=" + metadata.Commit,
		"-X", "main.buildTime=" + metadata.BuildTime,
	}, " ")
}

func buildEnv(base []string, target targetSpec) []string {
	allow := map[string]bool{
		"APPDATA":      true,
		"COMSPEC":      true,
		"GOCACHE":      true,
		"GOMODCACHE":   true,
		"GOPATH":       true,
		"GOROOT":       true,
		"HOME":         true,
		"LOCALAPPDATA": true,
		"PATH":         true,
		"PATHEXT":      true,
		"SYSTEMDRIVE":  true,
		"SYSTEMROOT":   true,
		"TEMP":         true,
		"TMP":          true,
		"TMPDIR":       true,
		"USERPROFILE":  true,
		"WINDIR":       true,
	}
	env := map[string]string{}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if allow[strings.ToUpper(key)] {
			env[strings.ToUpper(key)] = key + "=" + value
		}
	}
	env["CGO_ENABLED"] = "CGO_ENABLED=0"
	env["GOOS"] = "GOOS=" + target.GOOS
	env["GOARCH"] = "GOARCH=" + target.GOARCH

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, env[key])
	}
	return out
}

func archiveTarget(target targetSpec) (string, error) {
	if target.Platform == "windows" {
		return zipTarget(target)
	}
	return tarGzTarget(target)
}

func zipTarget(target targetSpec) (string, error) {
	archivePath := fromSlash(target.Archive)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return "", err
	}
	file, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	zw := zip.NewWriter(file)
	defer zw.Close()
	if err := addZipFile(zw, target.Exe, fromSlash(target.Output)); err != nil {
		return "", err
	}
	return target.Archive, nil
}

func addZipFile(zw *zip.Writer, name, sourcePath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	writer, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, source)
	return err
}

func tarGzTarget(target targetSpec) (string, error) {
	archivePath := fromSlash(target.Archive)
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return "", err
	}
	file, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	gzipWriter := gzip.NewWriter(file)
	defer gzipWriter.Close()
	tw := tar.NewWriter(gzipWriter)
	defer tw.Close()
	if err := addTarFile(tw, target.Exe, fromSlash(target.Output)); err != nil {
		return "", err
	}
	return target.Archive, nil
}

func addTarFile(tw *tar.Writer, name, sourcePath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	header := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: info.Size(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(tw, source)
	return err
}

func writeChecksums(outDir, checksumsPath string, files []string) error {
	var lines []string
	for _, filePath := range files {
		sum, err := sha256File(fromSlash(filePath))
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(fromSlash(outDir), fromSlash(filePath))
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = path.Base(filePath)
		}
		rel = toSlashPath(rel)
		lines = append(lines, fmt.Sprintf("%s  %s", sum, rel))
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	if err := os.MkdirAll(filepath.Dir(fromSlash(checksumsPath)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(fromSlash(checksumsPath), data, 0o644)
}

func sha256File(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func discoverBuildMetadata(ctx context.Context) buildMetadata {
	metadata := buildMetadata{
		Version:   "dev",
		Commit:    "none",
		BuildTime: time.Now().UTC().Format(time.RFC3339),
	}
	if version, err := gitOutput(ctx, "describe", "--tags", "--always", "--dirty"); err == nil && version != "" {
		metadata.Version = version
	}
	if commit, err := gitOutput(ctx, "rev-parse", "--short", "HEAD"); err == nil && commit != "" {
		metadata.Commit = commit
	}
	return metadata
}

func gitOutput(ctx context.Context, args ...string) (string, error) {
	gitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", args...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func slashJoin(parts ...string) string {
	return path.Join(parts...)
}

func toSlashPath(value string) string {
	return strings.ReplaceAll(filepath.ToSlash(value), "\\", "/")
}

func fromSlash(value string) string {
	return filepath.FromSlash(value)
}
