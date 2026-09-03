package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWrapper(t *testing.T) {
	repoRoot := repositoryRoot(t)
	wrapper := filepath.Join(repoRoot, "scripts", "examples", "gpt-image-2.sh")
	tmpRoot := t.TempDir()
	outsideRepository := t.TempDir()
	buildDirectory := t.TempDir()
	buildArgs := filepath.Join(buildDirectory, "build-args")
	buildWorkingDirectory := filepath.Join(buildDirectory, "build-working-directory")
	clientArgs := filepath.Join(buildDirectory, "client-args")
	fakeGo := writeFakeGo(t, buildDirectory)
	requestedOutput := filepath.Join(outsideRepository, "requested image.png")
	arguments := []string{
		"--address", "modelhub.internal.dev:50053",
		"--prompt", "paper boat; keep every character",
		"--output", requestedOutput,
		"--timeout", "45s",
		"--force",
	}

	command := exec.Command("bash", append([]string{wrapper}, arguments...)...)
	command.Dir = outsideRepository
	command.Env = append(os.Environ(),
		"TMPDIR="+tmpRoot,
		"WG_MODELHUB_GO_BIN="+fakeGo,
		"WG_MODELHUB_TEST_BUILD_ARGS="+buildArgs,
		"WG_MODELHUB_TEST_BUILD_WORKING_DIRECTORY="+buildWorkingDirectory,
		"WG_MODELHUB_TEST_CLIENT_ARGS="+clientArgs,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed: %v output=%q", err, output)
	}
	if len(output) != 0 {
		t.Fatalf("wrapper output=%q", output)
	}

	if got, err := os.ReadFile(buildWorkingDirectory); err != nil || strings.TrimSpace(string(got)) != repoRoot {
		t.Fatalf("build working directory=%q err=%v, want %q", got, err, repoRoot)
	}
	gotBuildArgs := readNULArguments(t, buildArgs)
	if len(gotBuildArgs) != 4 || gotBuildArgs[0] != "build" || gotBuildArgs[1] != "-o" || gotBuildArgs[3] != "./examples/gpt-image-2" {
		t.Fatalf("build args=%q", gotBuildArgs)
	}
	buildOutput := gotBuildArgs[2]
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(tmpRoot) + `/wg-modelhub-gpt-image-2\.[^/]{8}/gpt-image-2$`)
	if !pattern.MatchString(buildOutput) {
		t.Fatalf("build output=%q does not match %q", buildOutput, pattern)
	}
	if got := readNULArguments(t, clientArgs); !sameArguments(got, arguments) {
		t.Fatalf("client args=%q, want %q", got, arguments)
	}
	if _, err := os.Stat(requestedOutput); err != nil {
		t.Fatalf("requested output was removed: %v", err)
	}
	if _, err := os.Stat(buildOutput); !os.IsNotExist(err) {
		t.Fatalf("temporary binary remains: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(buildOutput)); !os.IsNotExist(err) {
		t.Fatalf("temporary directory remains: %v", err)
	}
}

func TestWrapperBuildFailureIsSanitized(t *testing.T) {
	repoRoot := repositoryRoot(t)
	wrapper := filepath.Join(repoRoot, "scripts", "examples", "gpt-image-2.sh")
	buildDirectory := t.TempDir()
	command := exec.Command("bash", wrapper, "--address", "example:50053")
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(),
		"TMPDIR="+t.TempDir(),
		"WG_MODELHUB_GO_BIN="+writeFakeGo(t, buildDirectory),
		"WG_MODELHUB_TEST_BUILD_FAIL=1",
		"WG_MODELHUB_TEST_BUILD_ARGS="+filepath.Join(buildDirectory, "build-args"),
		"WG_MODELHUB_TEST_BUILD_WORKING_DIRECTORY="+filepath.Join(buildDirectory, "build-working-directory"),
		"WG_MODELHUB_TEST_CLIENT_ARGS="+filepath.Join(buildDirectory, "client-args"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("wrapper succeeded")
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 70 {
		t.Fatalf("wrapper error=%v, want exit 70", err)
	}
	if got, want := string(output), "gpt-image-2 client build failed\n"; got != want {
		t.Fatalf("output=%q, want %q", got, want)
	}
}

func TestWrapperStopsActiveChildAndCleansExactlyOnceOnSignals(t *testing.T) {
	repoRoot := repositoryRoot(t)
	wrapper := filepath.Join(repoRoot, "scripts", "examples", "gpt-image-2.sh")

	for _, test := range []struct {
		name       string
		signal     syscall.Signal
		exitStatus int
	}{
		{name: "INT", signal: syscall.SIGINT, exitStatus: 130},
		{name: "TERM", signal: syscall.SIGTERM, exitStatus: 143},
		{name: "HUP", signal: syscall.SIGHUP, exitStatus: 129},
	} {
		t.Run(test.name, func(t *testing.T) {
			tmpRoot := t.TempDir()
			binDirectory := t.TempDir()
			readyPath := filepath.Join(tmpRoot, "build-ready")
			cleanupCallsPath := filepath.Join(tmpRoot, "cleanup-calls")
			fakeGo := filepath.Join(binDirectory, "fake-go")
			if err := os.WriteFile(fakeGo, []byte(`#!/usr/bin/env bash
set -u
: > "$WG_MODELHUB_TEST_BUILD_READY"
exec /bin/sleep 2
`), 0o700); err != nil {
				t.Fatal(err)
			}
			fakeRM := filepath.Join(binDirectory, "rm")
			if err := os.WriteFile(fakeRM, []byte(`#!/usr/bin/env bash
set -u
printf '%s\n' cleanup >> "$WG_MODELHUB_TEST_CLEANUP_CALLS"
exec /bin/rm "$@"
`), 0o700); err != nil {
				t.Fatal(err)
			}

			command := exec.Command("bash", wrapper)
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			command.Env = append(os.Environ(),
				"PATH="+binDirectory+":"+os.Getenv("PATH"),
				"TMPDIR="+tmpRoot,
				"WG_MODELHUB_GO_BIN="+fakeGo,
				"WG_MODELHUB_TEST_BUILD_READY="+readyPath,
				"WG_MODELHUB_TEST_CLEANUP_CALLS="+cleanupCallsPath,
			)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }()
			waitForPath(t, readyPath)
			signalStarted := time.Now()
			if err := command.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			err := command.Wait()
			if elapsed := time.Since(signalStarted); elapsed >= time.Second {
				t.Fatalf("wrapper took %s to stop its active child, want <1s", elapsed)
			}
			exitError, ok := err.(*exec.ExitError)
			if !ok || exitError.ExitCode() != test.exitStatus {
				t.Fatalf("wrapper error=%v, want exit %d", err, test.exitStatus)
			}

			cleanupCalls, readErr := os.ReadFile(cleanupCallsPath)
			if readErr != nil {
				t.Fatalf("read cleanup calls: %v", readErr)
			}
			if got := strings.Count(string(cleanupCalls), "cleanup\n"); got != 1 {
				t.Fatalf("cleanup calls=%d, want 1", got)
			}
			entries, readErr := os.ReadDir(tmpRoot)
			if readErr != nil {
				t.Fatal(readErr)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), "wg-modelhub-gpt-image-2.") {
					t.Fatalf("temporary directory remains: %s", entry.Name())
				}
			}
		})
	}
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, "..", ".."))
}

func writeFakeGo(t *testing.T, directory string) string {
	t.Helper()
	path := filepath.Join(directory, "fake-go")
	contents := `#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$PWD" > "$WG_MODELHUB_TEST_BUILD_WORKING_DIRECTORY"
printf '%s\0' "$@" > "$WG_MODELHUB_TEST_BUILD_ARGS"
if [[ "${WG_MODELHUB_TEST_BUILD_FAIL:-}" == "1" ]]; then
  printf '%s\n' 'SENSITIVE_BUILD_STDERR_SENTINEL' >&2
  exit 1
fi
if [[ "$#" != "4" || "$1" != "build" || "$2" != "-o" || "$4" != "./examples/gpt-image-2" ]]; then
  exit 2
fi
output=$3
printf '%s\n' '#!/usr/bin/env bash' 'set -Eeuo pipefail' 'printf '\''%s\0'\'' "$@" > "$WG_MODELHUB_TEST_CLIENT_ARGS"' 'output=""' 'while (($#)); do' '  if [[ "$1" == "--output" ]]; then output=$2; shift 2; continue; fi' '  shift' 'done' '[[ -n "$output" ]]' ': > "$output"' > "$output"
chmod 700 "$output"
`
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func readNULArguments(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parts := bytes.Split(bytes.TrimSuffix(data, []byte{0}), []byte{0})
	arguments := make([]string, len(parts))
	for index, part := range parts {
		arguments[index] = string(part)
	}
	return arguments
}

func sameArguments(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
