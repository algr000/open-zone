# open-zone Design (DP8 Transport + App Protocol)

`open-zone` is a single Windows-first runtime that provides ZoneMatch Internet compatible server behavior for the
Dungeon Siege client UI to progress and interact with multiplayer pages.

At a high level:

1. A native shim (`dp8shim`) hosts a real DirectPlay8 server via Windows `dpnet.dll`.
2. The Go runtime polls DP8 events from the shim, parses the app-layer XML-ish messages, and sends app-layer replies.
3. The same Go runtime also hosts small TCP endpoints used by adjacent UI flows (News, AutoUpdate).

## Goals

- Provide a stable transport (DirectPlay8) using Windows `dpnet.dll`.
- Implement enough of the app-layer protocol (NUL-terminated, XML-ish frames) to drive UI state transitions:
  - connect
  - Games list header/page refresh
  - host-published state ingestion (HostData / SetLoc)
  - game details (RowPg)
- Keep responses conservative by default; only enable `HR=0` for flows implemented by the server.

## Architecture

```
Dungeon Siege client
  |
  |  DirectPlay8 (dpnet.dll) connects to local DP8 server port (default 2300)
  v
bin/dp8shim.dll  (native, transport only)
  |
  |  DP8_PopEvent / DP8_SendTo
  v
cmd/open-zone (Go)
  |
  |  internal/dp8: event loop + send queue
  |  internal/proto: app protocol engine (XML-ish)
  |
  +--> internal/news: HTTP server (default 2301, GET / -> news.txt)
  +--> internal/autoupdate: TCP sink (default 80, accept+close)
```

## Repo Layout (Key Pieces)

- `cmd/open-zone/`: main entrypoint (wires config + servers + DP8 engine)
- `internal/config/`: config loading (Viper) + defaults
- `internal/dp8/`: DP8 polling loop, DP8 send worker, “last inbound payload” capture
- `internal/dp8shim/`: Windows DLL loader + wrappers around shim exports
- `internal/proto/`: app protocol parser + handlers + host state
- `internal/news/`: minimal news HTTP endpoint
- `internal/autoupdate/`: best-effort “fail fast” listener for AutoUpdate probes
- `dp8shim/`: native shim source + build scripts (outputs `bin/dp8shim.dll`)
- `bin/`: runtime binaries (shim lives here)
- `tools/`: helper utilities

## Runtime Startup Sequence

Implementation reference: `cmd/open-zone/main.go`.

1. Load config (`internal/config.Load`).
2. Start auxiliary TCP endpoints:
   - AutoUpdate sink (default `:80`) if enabled
   - News server (default `:2301`)
3. Load and start DP8 shim server:
   - load `bin/dp8shim.dll`
   - `DP8_StartServer(port)` (default `2300`)
4. Run the DP8 engine loop (`internal/dp8.Engine.Run`).

## DirectPlay8 Layer (internal/dp8 + internal/dp8shim)

Implementation reference: `internal/dp8/engine.go`.

### Event loop

- `dp8shim.PopEvent(...)` yields DP8 events (notably `DPN_MSGID_RECEIVE`).
- For each receive:
  - if payload begins with `<`, treat it as an app-protocol frame and parse it

### Sending strategy

- Outbound messages are queued to a send worker (channel-backed) to keep DP8 callbacks simple.
- DP8 send flags:
  - Connect bundle (`ConnectRes`, `ConInfoRes`, `ConnectEv`) uses `SYNC|GUARANTEED` to satisfy dpnet constraints.
  - Most other replies use `GUARANTEED`.
- Payload encoding:
  - app messages are written as NUL-terminated bytes (`...>\0`)
  - optional “tail” bytes can be appended after the NUL terminator

## App Protocol Layer (internal/proto)

Implementation reference: `internal/proto/protocol.go`, `internal/proto/xmlish.go`.

### Message format

- Messages are small, XML-ish tags with attributes, sent over DP8 as NUL-terminated frames.
- Typical pattern:
  - request: `<X ... />\0`
  - response: `<XRes HR="0x........" ... />\0`
- Many requests include a correlation attribute (commonly `Cx="0x..."`).

### Core state machine hooks

The current engine explicitly handles:

- `Connect` -> emits the “connect unlock bundle”:
  - `ConnectRes`
  - `ConInfoRes` (includes server `Port`)
  - `ConnectEv`

- Games list:
  - `HdrRow` -> `HdrRowRes` (headers for a given view id `Vid`)
  - `Page` -> `PageRes` (page results, including rows)
  - `RowPg` -> `RowPgRes` (details for a selected row id `Rid`)

- Hosting state ingestion (from clients that publish):
  - `SetLoc` -> `SetLocRes` (stores host’s current location string)
  - `HostData` -> `HostDataRes` (updates the server-side host model)

### Host state

Implementation reference: `internal/state/host_store.go`.

- Hosting clients publish incremental state via `HostData` and `SetLoc`.
- The server keeps an in-memory `hostState` keyed by the publishing client’s `DPNID`.
- The server assigns a stable small integer `Rid` per host session:
  - `Rid` must remain within signed 32-bit range; the client treats it as a signed `int`.

### Row encoding (important)

The Games UI is sensitive to schema details. For the flows that are enabled, follow these rules:

- For page rows, the client-side row decoder consumes values from **attributes on the `<Row ...>` element**, in
  attribute order matching the current header token order.
- Extra attributes (like a leading `Num="16"`) shift indices and can silently break column mapping.

This is why the current implementation constructs `<Row .../>` using the header token list order and avoids adding
extra wrapper elements.

## Auxiliary TCP Endpoints

### News server (internal/news)

- Default port: `2301`
- Endpoint: `GET /` and `HEAD /`
- Response body: `news.txt` (repo-root file by default)

### AutoUpdate sink (internal/autoupdate)

- Default port: `80`
- Behavior: accept TCP connections and immediately close them (fail fast).
- Purpose: prevent long UI timeouts if the client attempts an AutoUpdate connection.

## Telemetry / Debugging

- Optional NDJSON:
  - enable by setting `telemetry.dp8_ndjson_path` (or env `OZ_TELEMETRY_DP8_NDJSON_PATH`)
  - records inbound/outbound DP8 app messages and key events

## Configuration Model

Implementation reference: `internal/config/config.go`.

- Config file: `config/config.yaml` (optional; env-only is fine)
- Env prefix: `OZ_` (nested keys use `_`, for example `OZ_DP8_PORT=2300`)
- Key defaults:
  - `dp8.port`: `2300`
  - `news.port`: `2301`
  - `autoupdate.port`: `80`
  - `shim.path`: `bin\\dp8shim.dll`
  - `proto`: reserved for app-protocol options (currently none)

## Development Loop

1. Build the shim:

```powershell
cd dp8shim
.\build.ps1
```

2. Run the server:

```powershell
go run .\cmd\open-zone
```

3. (Optional) hot reload:

```powershell
air
```

## Practical Safety Rules

- Default to failure for unimplemented message families (`HR=E_FAIL`).
- Keep successful payloads minimal: extra attrs/tags can change the decoder branch and regress UI behavior.
