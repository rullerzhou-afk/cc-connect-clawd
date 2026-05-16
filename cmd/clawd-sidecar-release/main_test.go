package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct {
	args []string
	env  []string
	err  error
}

func (r *fakeRunner) Run(ctx context.Context, _ string, args []string, env []string, _, _ io.Writer) error {
	r.args = append([]string(nil), args...)
	r.env = append([]string(nil), env...)
	if r.err != nil {
		return r.err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-o" {
			return os.WriteFile(args[i+1], []byte("binary"), 0o755)
		}
	}
	return errors.New("missing -o argument")
}

func TestDefaultManifestUsesClawdDirectoryNames(t *testing.T) {
	m := buildManifest(defaultOutDir, defaultCmdPkg, buildMetadata{}, supportedTargets)
	wantDirs := map[string]bool{
		"windows-x64":   true,
		"windows-arm64": true,
		"darwin-x64":    true,
		"darwin-arm64":  true,
		"linux-x64":     true,
	}
	if len(m.Targets) != len(wantDirs) {
		t.Fatalf("len(targets) = %d, want %d", len(m.Targets), len(wantDirs))
	}
	byDir := targetsByDir(m.Targets)
	for want := range wantDirs {
		got, ok := byDir[want]
		if !ok {
			t.Fatalf("missing target %q in %#v", want, byDir)
		}
		if strings.Contains(got.Dir, "amd64") {
			t.Fatalf("target %q must use Clawd arch name x64", got.Dir)
		}
		if !strings.Contains(got.Output, filepath.ToSlash(filepath.Join(defaultOutDir, want))) {
			t.Fatalf("target %s output = %q, want under %q", want, got.Output, filepath.ToSlash(filepath.Join(defaultOutDir, want)))
		}
		if strings.Contains(got.Output, "\\") || strings.Contains(got.Archive, "\\") {
			t.Fatalf("target %s uses platform separators: output=%q archive=%q", want, got.Output, got.Archive)
		}
	}
	if got := byDir["windows-x64"]; got.Exe != "cc-connect-clawd.exe" {
		t.Fatalf("windows exe = %q", got.Exe)
	}
	if got := byDir["darwin-x64"]; got.Exe != "cc-connect-clawd" {
		t.Fatalf("darwin exe = %q", got.Exe)
	}
	if m.Checksums != "dist/clawd-sidecar/checksums.txt" {
		t.Fatalf("checksums = %q", m.Checksums)
	}
}

func TestSelectTargetsMapsX64ToGoAMD64AndDedupes(t *testing.T) {
	targets, err := selectTargets("windows-x64,windows-x64,linux-x64")
	if err != nil {
		t.Fatalf("selectTargets() error: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("len(targets) = %d, want 2", len(targets))
	}
	got := buildManifest(defaultOutDir, defaultCmdPkg, buildMetadata{}, targets).Targets[0]
	if got.GOOS != "windows" || got.GOARCH != "amd64" {
		t.Fatalf("GO target = %s/%s, want windows/amd64", got.GOOS, got.GOARCH)
	}
	if got.Dir != "windows-x64" {
		t.Fatalf("dir = %q, want windows-x64", got.Dir)
	}
}

func TestSelectTargetsRejectsGoArchDirectoryName(t *testing.T) {
	if _, err := selectTargets("windows-amd64"); err == nil {
		t.Fatal("selectTargets(windows-amd64) error = nil, want unsupported target error")
	}
}

func TestDryRunPrintsSlashNormalizedManifest(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run([]string{"--target", "windows-x64,linux-x64", "--dry-run", "--build-version", "v1", "--build-commit", "abc", "--build-time", "now"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() error: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Contains(stdout.String(), "\\") {
		t.Fatalf("dry-run output contains backslash path separators:\n%s", stdout.String())
	}
	var m manifest
	if err := json.Unmarshal(stdout.Bytes(), &m); err != nil {
		t.Fatalf("dry-run output is not JSON: %v\n%s", err, stdout.String())
	}
	byDir := targetsByDir(m.Targets)
	if _, ok := byDir["windows-x64"]; !ok {
		t.Fatalf("missing windows-x64 in dry-run manifest")
	}
	if _, ok := byDir["linux-x64"]; !ok {
		t.Fatalf("missing linux-x64 in dry-run manifest")
	}
	if m.Metadata.Version != "v1" || m.Metadata.Commit != "abc" || m.Metadata.BuildTime != "now" {
		t.Fatalf("metadata = %#v", m.Metadata)
	}
}

func TestBuildEnvIsMinimalAndOverridesTarget(t *testing.T) {
	env := buildEnv([]string{
		"PATH=C:\\bin",
		"GOCACHE=C:\\cache",
		"GOFLAGS=-mod=vendor",
		"GOPROXY=https://user:pass@example.test",
		"CLAWD_TG_BOT_TOKEN=secret",
		"GOOS=linux",
	}, targetSpec{GOOS: "windows", GOARCH: "amd64"})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=C:\\bin", "GOCACHE=C:\\cache", "CGO_ENABLED=0", "GOOS=windows", "GOARCH=amd64"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q in:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"GOFLAGS=", "GOPROXY=", "CLAWD_TG_BOT_TOKEN="} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("env leaked %q in:\n%s", forbidden, joined)
		}
	}
}

func TestBuildTargetInjectsVersionAndInstallsOnlyAfterSuccess(t *testing.T) {
	dir := t.TempDir()
	target := targetSpec{
		Platform: "windows",
		Arch:     "x64",
		GOOS:     "windows",
		GOARCH:   "amd64",
		Dir:      "windows-x64",
		Output:   filepath.ToSlash(filepath.Join(dir, "windows-x64", "cc-connect-clawd.exe")),
	}
	runner := &fakeRunner{}
	err := buildTarget(context.Background(), buildOptions{
		GoBin:    "go",
		CmdPkg:   "./cmd/cc-connect-clawd",
		Metadata: buildMetadata{Version: "v1", Commit: "abc", BuildTime: "now"},
		Target:   target,
		Runner:   runner,
	})
	if err != nil {
		t.Fatalf("buildTarget() error: %v", err)
	}
	if _, err := os.Stat(fromSlash(target.Output)); err != nil {
		t.Fatalf("final output missing: %v", err)
	}
	args := strings.Join(runner.args, "\n")
	for _, want := range []string{"main.version=v1", "main.commit=abc", "main.buildTime=now"} {
		if !strings.Contains(args, want) {
			t.Fatalf("build args missing %q in:\n%s", want, args)
		}
	}
}

func TestBuildTargetRemovesTemporaryOutputOnFailure(t *testing.T) {
	dir := t.TempDir()
	target := targetSpec{
		GOOS:   "windows",
		GOARCH: "amd64",
		Dir:    "windows-x64",
		Output: filepath.ToSlash(filepath.Join(dir, "cc-connect-clawd.exe")),
	}
	err := buildTarget(context.Background(), buildOptions{
		GoBin:  "go",
		CmdPkg: defaultCmdPkg,
		Target: target,
		Runner: &fakeRunner{err: errors.New("boom")},
	})
	if err == nil {
		t.Fatal("buildTarget() error = nil, want failure")
	}
	if _, err := os.Stat(fromSlash(target.Output)); !os.IsNotExist(err) {
		t.Fatalf("final output exists after failed build: %v", err)
	}
	if _, err := os.Stat(temporaryOutputPath(fromSlash(target.Output))); !os.IsNotExist(err) {
		t.Fatalf("temporary output exists after failed build: %v", err)
	}
}

func TestBuildTargetRejectsEmptyGoBinary(t *testing.T) {
	err := buildTarget(context.Background(), buildOptions{
		GoBin:  "",
		CmdPkg: defaultCmdPkg,
		Target: targetSpec{Output: filepath.ToSlash(filepath.Join(t.TempDir(), "out"))},
		Runner: &fakeRunner{},
	})
	if err == nil {
		t.Fatal("buildTarget() error = nil, want empty go binary error")
	}
}

func TestBuildTargetReportsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := buildTarget(ctx, buildOptions{
		GoBin:   "go",
		CmdPkg:  defaultCmdPkg,
		Target:  targetSpec{Dir: "windows-x64", Output: filepath.ToSlash(filepath.Join(t.TempDir(), "out.exe"))},
		Runner:  &fakeRunner{},
		Timeout: time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("buildTarget() error = %v, want canceled", err)
	}
}

func TestArchiveAndChecksums(t *testing.T) {
	dir := t.TempDir()
	target := targetSpec{
		Platform: "windows",
		Dir:      "windows-x64",
		Exe:      "cc-connect-clawd.exe",
		Output:   filepath.ToSlash(filepath.Join(dir, "windows-x64", "cc-connect-clawd.exe")),
		Archive:  filepath.ToSlash(filepath.Join(dir, "cc-connect-clawd-windows-x64.zip")),
	}
	if err := os.MkdirAll(filepath.Dir(fromSlash(target.Output)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fromSlash(target.Output), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath, err := archiveTarget(target)
	if err != nil {
		t.Fatalf("archiveTarget() error: %v", err)
	}
	zr, err := zip.OpenReader(fromSlash(archivePath))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "cc-connect-clawd.exe" {
		t.Fatalf("zip files = %#v", zr.File)
	}
	checksums := filepath.ToSlash(filepath.Join(dir, checksumsName))
	if err := writeChecksums(filepath.ToSlash(dir), checksums, []string{target.Output, archivePath}); err != nil {
		t.Fatalf("writeChecksums() error: %v", err)
	}
	data, err := os.ReadFile(fromSlash(checksums))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "windows-x64/cc-connect-clawd.exe") || !strings.Contains(text, "cc-connect-clawd-windows-x64.zip") {
		t.Fatalf("checksums = %q", text)
	}
}

func TestTarGzArchive(t *testing.T) {
	dir := t.TempDir()
	target := targetSpec{
		Platform: "linux",
		Dir:      "linux-x64",
		Exe:      "cc-connect-clawd",
		Output:   filepath.ToSlash(filepath.Join(dir, "linux-x64", "cc-connect-clawd")),
		Archive:  filepath.ToSlash(filepath.Join(dir, "cc-connect-clawd-linux-x64.tar.gz")),
	}
	if err := os.MkdirAll(filepath.Dir(fromSlash(target.Output)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fromSlash(target.Output), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath, err := archiveTarget(target)
	if err != nil {
		t.Fatalf("archiveTarget() error: %v", err)
	}
	file, err := os.Open(fromSlash(archivePath))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gr, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("tar first entry: %v", err)
	}
	if header.Name != "cc-connect-clawd" || header.Mode != 0o755 {
		t.Fatalf("tar header = %#v", header)
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("tar second entry error = %v, want EOF", err)
	}
}

func targetsByDir(targets []targetSpec) map[string]targetSpec {
	out := map[string]targetSpec{}
	for _, target := range targets {
		out[target.Dir] = target
	}
	return out
}
