package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
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
/bin/rm "$@"
exit 91
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

func TestWrapperEscalatesForNonCooperativeBuildAndClientGroups(t *testing.T) {
	repoRoot := repositoryRoot(t)
	wrapper := filepath.Join(repoRoot, "scripts", "examples", "gpt-image-2.sh")

	for _, stage := range []string{"build", "client"} {
		t.Run(stage, func(t *testing.T) {
			tmpRoot := t.TempDir()
			binDirectory := t.TempDir()
			readyPath := filepath.Join(tmpRoot, "ready")
			fakeGoStartedPath := filepath.Join(tmpRoot, "fake-go-started")
			childPIDPath := filepath.Join(tmpRoot, "child-pid")
			descendantPIDPath := filepath.Join(tmpRoot, "descendant-pid")
			cleanupCallsPath := filepath.Join(tmpRoot, "cleanup-calls")
			stubbornProgram := filepath.Join(binDirectory, "stubborn-program")
			if err := os.WriteFile(stubbornProgram, []byte(`#!/usr/bin/env bash
set -u
trap '' TERM
printf '%s' "$$" > "$WG_MODELHUB_TEST_CHILD_PID"
: > "$WG_MODELHUB_TEST_CHILD_READY"
bash -c 'trap "" TERM; printf "%s" "$$" > "$WG_MODELHUB_TEST_DESCENDANT_PID"; while :; do /bin/sleep 1; done' &
wait
`), 0o700); err != nil {
				t.Fatal(err)
			}
			fakeGo := filepath.Join(binDirectory, "fake-go")
			if err := os.WriteFile(fakeGo, []byte(`#!/usr/bin/env bash
set -u
: > "$WG_MODELHUB_TEST_FAKE_GO_STARTED"
if [[ "$WG_MODELHUB_TEST_STUBBORN_STAGE" == "build" ]]; then
  exec bash "$WG_MODELHUB_TEST_STUBBORN_PROGRAM"
fi
output=$3
/bin/cp "$WG_MODELHUB_TEST_STUBBORN_PROGRAM" "$output"
/bin/chmod 700 "$output"
`), 0o700); err != nil {
				t.Fatal(err)
			}
			fakeRM := filepath.Join(binDirectory, "rm")
			if err := os.WriteFile(fakeRM, []byte(`#!/usr/bin/env bash
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
				"WG_MODELHUB_TEST_STUBBORN_STAGE="+stage,
				"WG_MODELHUB_TEST_STUBBORN_PROGRAM="+stubbornProgram,
				"WG_MODELHUB_TEST_CHILD_READY="+readyPath,
				"WG_MODELHUB_TEST_FAKE_GO_STARTED="+fakeGoStartedPath,
				"WG_MODELHUB_TEST_CHILD_PID="+childPIDPath,
				"WG_MODELHUB_TEST_DESCENDANT_PID="+descendantPIDPath,
				"WG_MODELHUB_TEST_CLEANUP_CALLS="+cleanupCallsPath,
			)
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }()
			waitForPath(t, fakeGoStartedPath)
			waitForPath(t, readyPath)
			childPID := readPID(t, childPIDPath)
			waitForNonEmptyPath(t, descendantPIDPath)
			descendantPID := readPID(t, descendantPIDPath)
			defer func() {
				_ = syscall.Kill(childPID, syscall.SIGKILL)
				_ = syscall.Kill(descendantPID, syscall.SIGKILL)
			}()

			started := time.Now()
			if err := command.Process.Signal(syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- command.Wait() }()
			var err error
			select {
			case err = <-waited:
			case <-time.After(3 * time.Second):
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
				<-waited
				t.Fatal("wrapper did not bound a non-cooperative child")
			}
			if elapsed := time.Since(started); elapsed >= 2*time.Second {
				t.Fatalf("wrapper signal exit took %s, want <2s", elapsed)
			}
			exitError, ok := err.(*exec.ExitError)
			if !ok || exitError.ExitCode() != 143 {
				t.Fatalf("wrapper error=%v, want exit 143", err)
			}
			waitForProcessExit(t, childPID)
			waitForProcessExit(t, descendantPID)
			cleanupCalls, readErr := os.ReadFile(cleanupCallsPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if got := strings.Count(string(cleanupCalls), "cleanup\n"); got != 1 {
				t.Fatalf("cleanup calls=%d, want 1", got)
			}
			for _, entry := range mustReadDir(t, tmpRoot) {
				if strings.HasPrefix(entry.Name(), "wg-modelhub-gpt-image-2.") {
					t.Fatalf("temporary directory remains: %s", entry.Name())
				}
			}
		})
	}
}

func TestWrapperQueuesSignalsDuringChildLaunchRegistration(t *testing.T) {
	repoRoot := repositoryRoot(t)
	wrapper := filepath.Join(repoRoot, "scripts", "examples", "gpt-image-2.sh")

	for _, stage := range []string{"build", "client"} {
		for index, test := range []struct {
			name       string
			signalName string
			exitStatus int
		}{
			{name: "INT", signalName: "INT", exitStatus: 130},
			{name: "TERM", signalName: "TERM", exitStatus: 143},
			{name: "HUP", signalName: "HUP", exitStatus: 129},
		} {
			t.Run(stage+"-"+test.name, func(t *testing.T) {
				tmpRoot := t.TempDir()
				binDirectory := t.TempDir()
				childPIDPath := filepath.Join(tmpRoot, "child-pid")
				descendantPIDPath := filepath.Join(tmpRoot, "descendant-pid")
				signalSentPath := filepath.Join(tmpRoot, "signal-sent")
				readyPath := filepath.Join(tmpRoot, "ready-after-signal")
				cleanupCallsPath := filepath.Join(tmpRoot, "cleanup-calls")
				bashEnvironment := filepath.Join(binDirectory, "wrapper-bash-env")
				stubbornProgram := filepath.Join(binDirectory, "early-signal-program")
				boundary := "before-pid"
				if index%2 == 1 {
					boundary = "before-pgid"
				}
				targetLaunch := "1"
				if stage == "client" {
					targetLaunch = "2"
				}

				if err := os.WriteFile(bashEnvironment, []byte(`if [[ -z "${WG_MODELHUB_TEST_WRAPPER_PID:-}" ]]; then
  export WG_MODELHUB_TEST_WRAPPER_PID=$$
  wg_modelhub_launch_count=0
  wg_modelhub_debug_launch() {
    local target='active_pid=$!'
    if [[ "$WG_MODELHUB_TEST_LAUNCH_BOUNDARY" == "before-pgid" ]]; then
      target='active_pgid=$active_pid'
    fi
    if [[ "$BASH_COMMAND" == "$target" ]]; then
      wg_modelhub_launch_count=$((wg_modelhub_launch_count + 1))
      if [[ "$wg_modelhub_launch_count" == "$WG_MODELHUB_TEST_TARGET_LAUNCH" ]]; then
        while [[ ! -e "$WG_MODELHUB_TEST_SIGNAL_SENT" ]]; do /bin/sleep 0.005; done
      fi
    fi
  }
  trap wg_modelhub_debug_launch DEBUG
fi
`), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(stubbornProgram, []byte(`#!/usr/bin/env bash
set -u
trap '' INT TERM HUP
printf '%s' "$$" > "$WG_MODELHUB_TEST_CHILD_PID"
bash -c 'trap "" INT TERM HUP; printf "%s" "$$" > "$WG_MODELHUB_TEST_DESCENDANT_PID"; while :; do /bin/sleep 1; done' &
while [[ ! -s "$WG_MODELHUB_TEST_DESCENDANT_PID" ]]; do /bin/sleep 0.005; done
kill -s "$WG_MODELHUB_TEST_EARLY_SIGNAL" "$PPID"
: > "$WG_MODELHUB_TEST_SIGNAL_SENT"
/bin/sleep 1
: > "$WG_MODELHUB_TEST_READY_AFTER_SIGNAL"
while :; do /bin/sleep 1; done
`), 0o700); err != nil {
					t.Fatal(err)
				}
				fakeGo := filepath.Join(binDirectory, "fake-go")
				if err := os.WriteFile(fakeGo, []byte(`#!/usr/bin/env bash
set -u
if [[ "$WG_MODELHUB_TEST_STUBBORN_STAGE" == "build" ]]; then
  exec bash "$WG_MODELHUB_TEST_STUBBORN_PROGRAM"
fi
output=$3
/bin/cp "$WG_MODELHUB_TEST_STUBBORN_PROGRAM" "$output"
/bin/chmod 700 "$output"
`), 0o700); err != nil {
					t.Fatal(err)
				}
				fakeRM := filepath.Join(binDirectory, "rm")
				if err := os.WriteFile(fakeRM, []byte(`#!/usr/bin/env bash
printf '%s\n' cleanup >> "$WG_MODELHUB_TEST_CLEANUP_CALLS"
exec /bin/rm "$@"
`), 0o700); err != nil {
					t.Fatal(err)
				}

				command := exec.Command("bash", wrapper)
				command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				command.Env = append(os.Environ(),
					"BASH_ENV="+bashEnvironment,
					"PATH="+binDirectory+":"+os.Getenv("PATH"),
					"TMPDIR="+tmpRoot,
					"WG_MODELHUB_GO_BIN="+fakeGo,
					"WG_MODELHUB_TEST_STUBBORN_STAGE="+stage,
					"WG_MODELHUB_TEST_STUBBORN_PROGRAM="+stubbornProgram,
					"WG_MODELHUB_TEST_CHILD_PID="+childPIDPath,
					"WG_MODELHUB_TEST_DESCENDANT_PID="+descendantPIDPath,
					"WG_MODELHUB_TEST_SIGNAL_SENT="+signalSentPath,
					"WG_MODELHUB_TEST_READY_AFTER_SIGNAL="+readyPath,
					"WG_MODELHUB_TEST_CLEANUP_CALLS="+cleanupCallsPath,
					"WG_MODELHUB_TEST_LAUNCH_BOUNDARY="+boundary,
					"WG_MODELHUB_TEST_TARGET_LAUNCH="+targetLaunch,
					"WG_MODELHUB_TEST_EARLY_SIGNAL="+test.signalName,
				)
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				defer func() { _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL) }()
				waitForNonEmptyPath(t, childPIDPath)
				waitForNonEmptyPath(t, descendantPIDPath)
				childPID := readPID(t, childPIDPath)
				descendantPID := readPID(t, descendantPIDPath)
				defer func() {
					_ = syscall.Kill(-childPID, syscall.SIGKILL)
					_ = syscall.Kill(childPID, syscall.SIGKILL)
					_ = syscall.Kill(descendantPID, syscall.SIGKILL)
				}()
				waitForPath(t, signalSentPath)

				started := time.Now()
				waited := make(chan error, 1)
				go func() { waited <- command.Wait() }()
				var err error
				select {
				case err = <-waited:
				case <-time.After(3 * time.Second):
					_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
					<-waited
					t.Fatal("wrapper did not bound a signal delivered during child registration")
				}
				if elapsed := time.Since(started); elapsed >= 2*time.Second {
					t.Fatalf("wrapper launch-signal exit took %s, want <2s", elapsed)
				}
				exitError, ok := err.(*exec.ExitError)
				if !ok || exitError.ExitCode() != test.exitStatus {
					t.Fatalf("wrapper error=%v, want exit %d", err, test.exitStatus)
				}
				waitForProcessExit(t, childPID)
				waitForProcessExit(t, descendantPID)
				if _, err := os.Stat(readyPath); !os.IsNotExist(err) {
					t.Fatalf("child reached readiness after signaling wrapper: %v", err)
				}
				cleanupCalls, err := os.ReadFile(cleanupCallsPath)
				if err != nil {
					t.Fatal(err)
				}
				if got := strings.Count(string(cleanupCalls), "cleanup\n"); got != 1 {
					t.Fatalf("cleanup calls=%d, want 1", got)
				}
				for _, entry := range mustReadDir(t, tmpRoot) {
					if strings.HasPrefix(entry.Name(), "wg-modelhub-gpt-image-2.") {
						t.Fatalf("temporary directory remains: %s", entry.Name())
					}
				}
			})
		}
	}
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func waitForProcessExit(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d remains alive", pid)
}

func mustReadDir(t *testing.T, path string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
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

func waitForNonEmptyPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for non-empty %s", path)
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
