package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const cliSecret = "sk-test-credential-9f3a"
const cliBody = "custom_flag: keep-this-body"

func TestCLICreatesOwnerOnlyFile(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.yaml")
	output := filepath.Join(dir, "out.yaml")
	writeInput(t, input)
	stderr := runCLI(t, input, output)
	if !strings.Contains(stderr, "renamed 1 instances\n") {
		t.Fatalf("stderr=%q", stderr)
	}
	assertNoConfigLeak(t, stderr)
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), cliSecret) || !strings.Contains(string(body), "Gemini_Developer_API") {
		t.Fatal("new file lost credential or renamed instance")
	}
}

func TestCLIRefusesExistingOutputAndSamePath(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.yaml")
	output := filepath.Join(dir, "out.yaml")
	writeInput(t, input)
	original := []byte("already-here-" + cliSecret)
	if err := os.WriteFile(output, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(output, 0o644); err != nil {
		t.Fatal(err)
	}
	stderr := runCLI(t, input, output)
	assertNoConfigLeak(t, stderr)
	if strings.Contains(stderr, "renamed ") {
		t.Fatal("existing output was treated as success")
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("existing output was overwritten")
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("existing mode=%o", info.Mode().Perm())
	}

	before, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	stderr = runCLI(t, input, input)
	assertNoConfigLeak(t, stderr)
	after, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("input path was overwritten")
	}
	rel := filepath.Join(dir, "rel.yaml")
	if err := os.WriteFile(rel, before, 0o600); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	stderr = runCLI(t, "rel.yaml", "./rel.yaml")
	assertNoConfigLeak(t, stderr)
	again, err := os.ReadFile(rel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, before) {
		t.Fatal("equivalent path was overwritten")
	}
}

func TestCLIErrorsOmitConfig(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.yaml")
	var stderr bytes.Buffer
	if code := run(nil, &stderr); code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	assertNoConfigLeak(t, stderr.String())

	stderr.Reset()
	if code := run([]string{filepath.Join(dir, "missing.yaml"), filepath.Join(dir, "out.yaml")}, &stderr); code == 0 {
		t.Fatal("missing input succeeded")
	}
	assertNoConfigLeak(t, stderr.String())
	if _, err := os.Stat(filepath.Join(dir, "out.yaml")); !os.IsNotExist(err) {
		t.Fatal("missing input created an output")
	}

	if err := os.WriteFile(input, []byte("token: "+cliSecret+"\nproviders: [\n"+cliBody+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := run([]string{input, filepath.Join(dir, "bad-out.yaml")}, &stderr); code == 0 {
		t.Fatal("invalid yaml succeeded")
	}
	assertNoConfigLeak(t, stderr.String())
	if _, err := os.Stat(filepath.Join(dir, "bad-out.yaml")); !os.IsNotExist(err) {
		t.Fatal("invalid yaml created an output")
	}

	// 用当前 VWorld ID 制造实例冲突；旧 ltx ID 已不再归入薇光点亮。
	collision := `
providers:
  a:
    models: [VWorld_outfit_video_1.0]
    ltx:
      base_url: https://a.example
      token: ` + cliSecret + `
      duration: 1
      fps: 1
      poll_interval: 1
  b:
    models: [VWorld_outfit_video_1.0]
    ltx:
      base_url: https://b.example
      token: ` + cliSecret + `-b
      duration: 1
      fps: 1
      poll_interval: 1
`
	collisionPath := filepath.Join(dir, "collision.yaml")
	if err := os.WriteFile(collisionPath, []byte(collision), 0o600); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	out := filepath.Join(dir, "collision-out.yaml")
	if code := run([]string{collisionPath, out}, &stderr); code == 0 {
		t.Fatal("name collision succeeded")
	}
	assertNoConfigLeak(t, stderr.String())
	if strings.Contains(stderr.String(), cliSecret+"-b") {
		t.Fatal("command output included config body or credential")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("collision created an output")
	}
}

func writeInput(t *testing.T, path string) {
	t.Helper()
	const body = `
note: keep-me
providers:
  gemini_api:
    models: [gemini-2.5-flash]
    custom_flag: keep-this-body
    gemini:
      api_key: sk-test-credential-9f3a
      proxy_url: http://127.0.0.1:1081
`
	if !strings.Contains(body, cliSecret) || !strings.Contains(body, cliBody) {
		t.Fatal("fixture drifted from leak markers")
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCLI(t *testing.T, input, output string) string {
	t.Helper()
	var stderr bytes.Buffer
	if code := run([]string{input, output}, &stderr); code == 0 && !strings.Contains(stderr.String(), "renamed ") {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	return stderr.String()
}

func assertNoConfigLeak(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, cliSecret) || strings.Contains(text, cliBody) || strings.Contains(text, "api_key:") {
		t.Fatal("command output included config body or credential")
	}
}
