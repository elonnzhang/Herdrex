# Herdrex

Bridge of Herdr and Rex. [中文](README.zh-CN.md)

Herdrex serves a Herdr server as a Rex host. Rex.app and the `rex` CLI connect to it like any other host and see Herdr's workspaces as sessions, its tabs as windows and its panes as blocks. Typing, pasting, mouse and wheel input go to the Herdr pane, and the pane's screen streams back through Herdr's direct terminal protocol.

## Requirements

- [Rex](https://www.superlogical.com/rex/docs/installation): Rex.app for macOS is in an invite-only beta; testers download it after logging in with Discord. Linux has only the server, for testers.
- [Herdr](https://herdr.dev/)

## Install

```sh
# add the tap (github.com/elonnzhang/homebrew-tap), then install
brew tap elonnzhang/tap
brew install elonnzhang/tap/herdrex
# or
go install github.com/elonnzhang/herdrex/cmd/herdrex@latest
```

## Quick start

```sh
herdrex --add-host   # serve the default Herdr on 127.0.0.1:7388 and add it to your Rex hosts as "herdr"
rex -S herdr ls
```

`--add-host` runs `rex hosts add`, which writes to your Rex home and syncs to all your devices. Rex.app does not read those hosts: add the host in Rex.app under Settings > Remote Host, with the name and address herdrex prints when it starts. `--add-host=NAME` picks another name; when a host of that name already exists, its endpoint is replaced. Without `--herdr-socket`, the bridge finds Herdr's socket as `herdr` does: `HERDR_SOCKET_PATH`, then `HERDR_SESSION`, then the default session (`~/.config/herdr/herdr.sock` when `XDG_CONFIG_HOME` is unset). `herdrex --help` lists every option.

herdrex runs in the foreground until Ctrl+C. `--daemon` runs it in the background instead, in a session of its own so that it outlives the terminal: it returns once the bridge serves, or prints why it failed, and logs to `<user cache dir>/herdrex/herdrex-PORT.log` (`--log FILE` picks another). `kill PID`, with the pid it prints, stops it as Ctrl+C would.

Without `--start`, the bridge serves the Herdr already running at the socket. With `--start`, it starts a new Herdr server there, with its config and state beside the socket (`x.sock`: `x.d`), and stops it on exit; it refuses a socket where a server is running or one that would share your own Herdr's config.

To try it without touching your Herdr or keeping anything:

```sh
herdrex test   # a new Herdr at /tmp/herdr/herdr-for-rex.sock, served on 127.0.0.1:7388 as Rex host herdr-in-Rex
```

It runs in the foreground. To try it in Rex.app, add the host it prints under Settings > Remote Host. Ctrl+C removes the Rex host it added, stops that Herdr server and removes its socket, config and state; remove the host in Rex.app yourself.

The bridge has no authentication, so it only listens on a loopback or a Tailscale address (100.64.0.0/10, fd7a:115c:a1e0::/48). On a Tailscale address, every device your tailnet lets reach this machine can read and type into every pane:

```sh
herdrex --listen $(tailscale ip -4):7388 --add-host
```

When an agent in a pane is blocked or done, the bridge sends Rex.app a `desktop_notification`, which Rex.app shows as a system notification. `--notify=false` turns this off.

Herdr has no floating layers, detached blocks, Lua scripts or client actions, so the Rex methods for them fail. `docs/PROTOCOL.md` lists every method and event with its mapping, and section 6 lists the known gaps.

## Packages

One Go module, `github.com/elonnzhang/herdrex`. The packages under `pkg/` can be used on their own.

| Package | Contents |
|---|---|
| `pkg/rex` | Rex protocol types: hello, welcome, requests, responses, events, error and close codes, the session view |
| `pkg/rex/client` | Rex client: dial `unix://`, `http://` or `https://`, make calls, read events in order, open a block's terminal stream |
| `pkg/rex/server` | Rex server framework: the handshake, event seq, request order, attachments and the events they select, clients, data tickets |
| `pkg/rex/termstream` | Encoding and decoding of the terminal data stream |
| `pkg/herdr` | Herdr's socket path rules |
| `pkg/herdr/jsonapi` | Client for Herdr's JSON API (`herdr.sock`) |
| `pkg/herdr/term` | Client for Herdr's direct terminal protocol (`herdr-client.sock`) |
| `pkg/herdr/endpoint` | Client for Herdr's endpoint protocol (`herdr-client.sock`) |
| `pkg/herdr/cli` | Wrapper for the `herdr` command: version, status, sessions, schema, and an isolated `herdr server` |
| `internal/bridge` | The mapping from Rex methods to Herdr calls and from Herdr events to Rex events |

## Development

```sh
go test ./...
REX_TEST_SERVER=http://127.0.0.1:7388 go test ./pkg/rex/client/   # also runs against a live Rex server or bridge
HERDREX_TRACE=1 ./bin/herdrex ...                                  # logs every request, event and frame
HERDR_TEST_SOCKET=/tmp/hx-live/herdr.sock HERDR_TEST_BIN=$(command -v herdr) go test -p 1 -run Live ./pkg/herdr/...   # live tests, isolated Herdr only
HERDR_SOCKET_PATH=path/to/herdr-socket herdr                         # enter herdr
```

The test with `REX_TEST_SERVER` creates a session on that server and destroys it at the end. The `pkg/herdr` live tests create and close workspaces and tabs, so point `HERDR_TEST_SOCKET` at a Herdr server started only for them (`docs/TESTING.md`); the `cli` test starts its own. `docs/TESTING.md` sets up an isolated Herdr, Rex server and Rex client under `/tmp/rexbridge`, so tests don't touch the ones you use every day.

## Docs

The docs are in Chinese.

- `BRIDGE.md`: one diagram of how herdrex sits between Rex and Herdr, and how their objects, input, output and events map
- `docs/PROTOCOL.md`: how the two sides map, the mapping and status of every method and event, the test checklist
- `docs/herdr-api.md`: the Herdr socket API (basis of `pkg/herdr/jsonapi`)
- `docs/herdr-client-protocols.md`: the direct terminal protocol and the endpoint protocol on Herdr's client socket, and how they differ
- `docs/rex-api.md`: the Rex control and data protocols (basis of `pkg/rex`)
- `docs/ref/`: per-method field tables, generated by `tools/apidoc/gen.py`
- `docs/TESTING.md`: the isolated test environment
- `docs/remote-herdr.md`: research on connecting to a remote Herdr (not planned)
