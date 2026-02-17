# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

docker-logger collects stdout/stderr logs from Docker containers and forwards them to rotated local files and/or remote syslog. It watches for container start/stop events and automatically manages log streams.

## Build and Test Commands

```bash
# build
cd app && go build -o /tmp/docker-logger -ldflags "-X main.revision=dev" && cd ..

# run all tests
go test ./... -count=1

# run tests with race detector
go test -race -timeout=60s ./...

# run a single test
go test -run TestEvents ./app/discovery/

# test coverage
go test -cover ./...

# lint (golangci-lint v2 config)
golangci-lint run

# generate mocks (run from repo root)
go generate ./...

# vendor dependencies
go mod vendor
```

## Architecture

Single-binary Go application in `app/` package (uses `package main`, not a library).

**Control flow**: `main()` → `do()` → `runEventLoop()` which listens on `EventNotif.Channel()` for container events and creates/destroys `LogStreamer` instances. Exits on context cancellation or when events channel is closed (e.g., docker event listener failure). Uses `closeStreamer` helper for resource cleanup.

### Packages

- **`app/`** — entry point, CLI options (`go-flags`), event loop, log writer factory (`makeLogWriters` returns `logWriter, errWriter, error`). Wires discovery events to log streamers. Creates `MultiWriter` combining file (lumberjack) and syslog destinations. Uses `writeNopCloser` wrapper to prevent double-close when syslog writer is shared between log and err MultiWriters.
- **`app/discovery/`** — `EventNotif` watches Docker daemon for container start/stop events via `go-dockerclient`. `NewEventNotif(client, EventNotifOpts)` accepts filtering options via struct. Emits `Event` structs on a channel; closes the channel on listener failure. Handles include/exclude filtering by name lists and regex patterns. Extracts group name from image path.
- **`app/logger/`** — `LogStreamer` attaches to a container's log stream (blocking `Logs()` call in a goroutine). `MultiWriter` fans out writes to multiple `io.WriteCloser` destinations, optionally wrapping in JSON envelope.
- **`app/syslog/`** — platform-specific syslog writer. Build-tagged: real implementation on unix, stub on windows.

### Key Patterns

- **Docker client interface**: `discovery.DockerClient` and `logger.LogClient` are consumer-side interfaces wrapping `go-dockerclient`. Mocks generated with `moq` into `mocks/` subdirectories.
- **LogStreamer lifecycle**: `Go(ctx)` starts streaming in a goroutine, `Close()` cancels context and waits, `Wait()` blocks on `ctx.Done()`, `Err()` retrieves the error (if any) after `Wait()` returns. Has retry logic for Docker EOF errors.
- **MultiWriter**: ignores individual write errors unless all writers fail. `Close()` collects errors via `go-multierror`.
- **Container filtering**: supports name lists (`--exclude`/`--include`) and regex patterns (`--exclude-pattern`/`--include-pattern`), mutually exclusive within each group and across groups (e.g., `--include` + `--exclude-pattern` is also invalid).

## Dependencies

- `github.com/fsouza/go-dockerclient` — Docker API client
- `github.com/go-pkgz/lgr` — logging
- `github.com/jessevdk/go-flags` — CLI flags with env var support
- `github.com/pkg/errors` — error wrapping
- `github.com/hashicorp/go-multierror` — error accumulation
- `gopkg.in/natefinch/lumberjack.v2` — log rotation
- Vendored in `vendor/`

## Testing Notes

- Tests use `moq`-generated mocks in `mocks/` subdirectories
- Most `app/` tests use mocks; no live Docker needed. `Test_Do` requires a live Docker daemon but is skipped unless `TEST_DOCKER` env var is set
- Channel-based synchronization preferred over `time.Sleep` for race-free tests; use `require.Eventually` with condition checks for async operations
- Uses `t.TempDir()` for temporary files and `t.Context()` for test contexts
- Sentinel event technique: send a known event after the one being tested, wait for it via `require.Eventually` to confirm both events were processed
