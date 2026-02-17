# Code Review Fixes

## Overview

Address all issues discovered during full-project code review. Fixes span bugs, error handling, code quality, test reliability, and documentation gaps.

Key changes:
- fix incorrect timestamp calculations in event discovery
- replace `log.Fatalf` with proper error returns in non-main functions
- fix syslog writer double-close when both files and syslog are enabled
- eliminate flaky `time.Sleep` patterns in tests
- update README with missing CLI flags and documentation gaps

## Context (from discovery)

Files/components involved:
- `app/discovery/events.go` — timestamp bugs, typo, Names guard, parameter count
- `app/main.go` — log.Fatalf paths, syslog double-close, incomplete validation, syslog error handling
- `app/logger/multiwriter.go` — variable shadowing in Write/Close loops
- `app/logger/logger.go` — godoc improvements
- `app/main_test.go` — flaky sleep patterns, syslog test quality, MixErr coverage gap
- `app/logger/logger_test.go` — flaky sleep patterns
- `app/syslog/syslog_unix.go` — godoc fix
- `README.md` — missing flags, typo, missing exclusivity note

## Development Approach

- **ALL issues are in scope** — this plan exists specifically to fix pre-existing problems found during code review. Nothing should be dismissed as "pre-existing", "not introduced by this branch", or "outside scope"
- **testing approach**: Regular (fix code, then add/update tests)
- complete each task fully before moving to the next
- make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task**
- **CRITICAL: update this plan file when scope changes during implementation**
- run tests after each change
- maintain backward compatibility

## Testing Strategy

- **unit tests**: required for every task with code changes
- no e2e tests in this project

## Progress Tracking

- mark completed items with `[x]` immediately when done
- add newly discovered tasks with ➕ prefix
- document issues/blockers with ⚠️ prefix

## Implementation Steps

### Task 1: Fix timestamp calculations in events.go

**Files:**
- Modify: `app/discovery/events.go`
- Modify: `app/discovery/events_test.go`

- [x] fix `activate()` line 123: use `TimeNano` with fallback — `if dockerEvent.TimeNano != 0 { time.Unix(0, dockerEvent.TimeNano) } else { time.Unix(dockerEvent.Time, 0) }` to handle older Docker API versions that may not set `TimeNano`
- [x] fix `emitRunningContainers()` line 150: change `time.Unix(c.Created/1000, 0)` to `time.Unix(c.Created, 0)` — `Created` is already Unix seconds
- [x] fix typo line 96: change `"can't add even listener"` to `"can't add event listener"`
- [x] add guard in `emitRunningContainers()` for empty `c.Names` slice before accessing `c.Names[0]`
- [x] add test case in `TestEmit` verifying `Event.TS` is a reasonable timestamp (not ~1970)
- [x] add test case in `TestActivateAllContainerStatuses` or similar verifying `Event.TS` from activate path
- [x] run `go test ./app/discovery/ -count=1` — must pass before next task

### Task 2: Replace log.Fatalf with error returns in makeLogWriters

**Files:**
- Modify: `app/main.go`
- Modify: `app/main_test.go`

- [x] change `makeLogWriters` signature to return `(logWriter, errWriter io.WriteCloser, err error)`
- [x] replace `log.Fatalf` on line 179 (neither destination enabled) with `return nil, nil, errors.New("either files or syslog has to be enabled")`
- [x] replace `log.Fatalf` on line 191 (directory creation failure) with error return
- [x] update `procEvent` in `runEventLoop` (the only caller of `makeLogWriters`) to handle the error — log warning and skip the container
- [x] add test case for `makeLogWriters` with neither files nor syslog enabled — verify error returned
- [x] add test case for `makeLogWriters` with invalid directory path — verify error returned
- [x] run `go test ./app/ -count=1` — must pass before next task

### Task 3: Fix syslog writer double-close

**Files:**
- Modify: `app/main.go`
- Modify: `app/main_test.go`

- [x] in `makeLogWriters`, when syslog is enabled: track whether the same syslog writer is shared between logWriters and errWriters. When both files and syslog are enabled without MixErr, the syslog writer appears in both MultiWriters and gets closed twice
- [x] approach: create a `writeNopCloser` that wraps an `io.Writer` but makes `Close()` a no-op, use it for the errWriters syslog entry so only logWriters actually closes syslog. Note: the `MixErr` guard in `runEventLoop` remains necessary for file writers
- [x] add test case with both `EnableFiles: true` and `EnableSyslog: true` verifying no double-close errors
- [x] run `go test ./app/ -count=1 -race` — must pass before next task

### Task 4a: Fix variable shadowing in multiwriter.go

**Files:**
- Modify: `app/logger/multiwriter.go`

- [x] rename loop variable in `MultiWriter.Write` from `w` to `wr`: `for _, wr := range w.writers`
- [x] rename loop variable in `MultiWriter.Close` from `w` to `wr`: `for _, wr := range w.writers`
- [x] run `go test ./app/logger/ -count=1` — must pass before next task

### Task 4b: Add missing filter validation in do()

**Files:**
- Modify: `app/main.go`
- Modify: `app/main_test.go`

- [x] add validation in `do()` for `IncludesPattern + ExcludesPattern` combination — return error
- [x] add validation in `do()` for `Includes + ExcludesPattern` combination — return error
- [x] add validation in `do()` for `IncludesPattern + Excludes` combination — return error
- [x] add test cases in `Test_doValidation` for all three new conflict combinations
- [x] run `go test ./app/ -count=1` — must pass before next task

### Task 5: Fix flaky test patterns in main_test.go

**Files:**
- Modify: `app/main_test.go`

- [x] `Test_runEventLoop:"start and stop container"` line 111: replace `time.Sleep(100ms)` after stop event with `require.Eventually` checking a verifiable condition (e.g., send a follow-up sentinel start event and wait for it to be processed, confirming the stop was handled first)
- [x] `Test_runEventLoop:"duplicate start ignored"` lines 140-145: replace `time.Sleep(50ms)` with `require.Eventually` to confirm first event processed (logsCalls == 1) before sending second event
- [x] `Test_runEventLoop:"stop non-mapped container ignored"` lines 171-174: replace `time.Sleep(50ms)` with a sentinel event technique — send a known start event after the stop, wait for it to be processed via `require.Eventually`, then assert LogsCalls == 1 (confirming stop was also processed and ignored)
- [x] remove `time.Sleep(200ms)` from `Test_Do` line 37 — serves no purpose after `do()` returns
- [x] run `go test ./app/ -count=1 -race` — must pass before next task

### Task 6: Fix flaky test patterns in logger_test.go

**Files:**
- Modify: `app/logger/logger_test.go`

- [x] `TestLogStreamer_NormalCompletion` line 72: replace `time.Sleep(50ms)` with `require.Eventually` checking `mock.LogsCalls()` length == 1 before calling `Close()`
- [x] `TestLogStreamer_ErrorTermination` line 114: replace `time.Sleep(50ms)` with `require.Eventually` checking `mock.LogsCalls()` length == 1 before calling `Close()`
- [x] run `go test ./app/logger/ -count=1 -race` — must pass before next task

### Task 7: Improve syslog test quality and add MixErr coverage

**Files:**
- Modify: `app/main_test.go`

- [x] fix `Test_makeLogWritersSyslogFailed`: make it actually verify syslog failure — e.g., assert that the returned writer has zero underlying file writers (only syslog was attempted and failed)
- [x] fix or remove `Test_makeLogWritersSyslogPassed`: either start a UDP listener (like syslog_unix_test.go does) and verify data, or consolidate with SyslogFailed since they exercise the same path
- [x] add `Test_runEventLoop_MixErr` test: verify that with `MixErr: true`, the err writer is not separately closed on container stop
- [x] run `go test ./app/ -count=1` — must pass before next task

### Task 8: Update documentation

**Files:**
- Modify: `README.md`
- Modify: `app/syslog/syslog_unix.go`
- Modify: `app/logger/logger.go`

- [x] add missing rows to README options table: `--loc`/`LOG_FILES_LOC` (default "logs"), `--syslog-prefix`/`SYSLOG_PREFIX` (default "docker/"), `--dbg`/`DEBUG` (default false)
- [x] add bullet for `--exclude`/`--exclude-pattern` mutual exclusivity note
- [x] fix typo on line 8: "inlcudes" → "includes"
- [x] fix godoc for `GetWriter` in syslog_unix.go: mention syslogPrefix parameter
- [x] improve godoc for `LogStreamer.Go()`: mention goroutine lifecycle and retry behavior
- [x] improve godoc for `LogStreamer.Close()`: mention it blocks until stream completes
- [x] improve godoc for `LogStreamer.Wait()`: clarify it waits on context cancellation

### Task 9: Verify acceptance criteria

- [x] verify all timestamp calculations produce correct dates
- [x] verify `makeLogWriters` returns errors instead of calling Fatalf
- [x] verify no double-close warnings with files+syslog enabled
- [x] verify no `time.Sleep` patterns remain in tests (except `TestLogStreamer_RetryOnDockerEOF` which is unavoidable due to 1s sleep in production code)
- [x] run full test suite: `go test ./... -count=1`
- [x] run race detector: `go test -race -timeout=60s ./...`
- [x] run linter: `golangci-lint run`
- [x] run formatter: `~/.claude/format.sh`
- [x] verify test coverage: `go test -cover ./...` (target 80%+)

### Task 10: [Final] Update documentation and finalize

- [x] update CLAUDE.md if new patterns discovered
- [x] move this plan to `docs/plans/completed/`

## Technical Details

### Timestamp fix

Docker API `Time` field is Unix seconds, `TimeNano` is full nanosecond-precision Unix timestamp.

Current (wrong):
```go
TS: time.Unix(dockerEvent.Time/1000, dockerEvent.TimeNano)
TS: time.Unix(c.Created/1000, 0)
```

Fixed (with fallback for older Docker API versions where TimeNano may be zero):
```go
// activate path — prefer nanosecond precision with fallback
ts := time.Unix(0, dockerEvent.TimeNano)
if dockerEvent.TimeNano == 0 {
    ts = time.Unix(dockerEvent.Time, 0)
}
TS: ts

// emitRunningContainers path — Created is Unix seconds
TS: time.Unix(c.Created, 0)
```

### makeLogWriters error handling

Change signature from:
```go
func makeLogWriters(opts *cliOpts, containerName, group string) (logWriter, errWriter io.WriteCloser)
```
To:
```go
func makeLogWriters(opts *cliOpts, containerName, group string) (logWriter, errWriter io.WriteCloser, err error)
```

### Syslog double-close fix

When both files and syslog are enabled, the same `syslogWriter` is added to both `logWriters` and `errWriters` slices. Both MultiWriters close it independently. Fix by wrapping the errWriters copy with a no-op closer:

```go
type writeNopCloser struct {
    io.Writer
}

func (writeNopCloser) Close() error { return nil }
```

Add `syslogWriter` directly to `logWriters`, add `writeNopCloser{syslogWriter}` to `errWriters`.

## Post-Completion

**Manual verification:**
- test with a live Docker daemon (`TEST_DOCKER=1 go test ./app/ -run Test_Do`)
- verify log output shows correct timestamps for container events
