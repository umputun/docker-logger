---
worth: later
where: app/logger/logger.go:LogStreamer.stream
added: 2026-11-22
---
# logs of containers started with a TTY are never collected

A container with `tty: true` (`docker run -t`) sends its log as a raw stream, not stdout/stderr frames.
`LogStreamer` never sets `LogsOptions.RawTerminal`, so the vendored client always demultiplexes, and
`stdcopy.StdCopy` fails on the first byte with `unrecognized stream: N`. The streamer treats that as final
and logs one WARN; nothing from the container reaches the files or syslog.

Fix: inspect the container (`Config.Tty`) before streaming and set `RawTerminal` for TTY containers, which
needs `InspectContainer` on the `LogClient` interface and a mock regeneration. Surfaced reviewing PR #66,
where the error had turned into an endless reopen loop before it was made final.
