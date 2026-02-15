# DP8 App-Protocol Message Map

This doc is the current minimal map of the app-protocol messages handled over DirectPlay8.

Scope:
- Messages are **NUL-terminated**, XML-ish UTF-8 frames carried over DP8 `DPN_MSGID_RECEIVE`.
- DP8 is treated as transport (hosted by `dpnet.dll` via `dp8shim`).
- This doc describes the message schemas and the flows where each message appears.

Non-scope:
- UI click-to-message mapping.
- Low-level DP8 CFRAME/DFRAME (handled by Windows).

## Conventions

- Requests are inbound: `<Tag ... />\0`
- Responses are outbound: `<TagRes ... />\0` (or `<TagEv ... />\0` for events)
- `Cx="0x..."` is a correlation/context id (echo it back when present).
- `Vid="..."` is a view/table id used by Games list pages/rows.

## Flow 1: Connect (ZoneMatch Internet entry)

### `Connect` -> `ConnectRes` + `ConInfoRes` + `ConnectEv`

Inbound:
```xml
<Connect Cx="0x123" ProtoVer="3.3" />\0
```

Outbound bundle (must be sent; DP8 send flags matter at transport layer):
```xml
<ConnectRes HR="0x00000000" Cx="0x123" ProtoVer="3.3" ... />\0
<ConInfoRes HR="0x00000000" Cx="0x123" IpAddr="127.0.0.1" Port="2300" />\0
<ConnectEv  HR="0x00000000" Cx="0x123" Time="..." />\0
```

Notes:
- This is the required bundle that gets the UI past “Connecting to ZoneMatch Server...”.
- `Port` must match the DP8 server port configured for the runtime.
- `IpAddr`/`Port` are taken from config (`dp8.advertise_ip`, `dp8.advertise_port`) when set; otherwise the server defaults to `127.0.0.1:<dp8.port>`.

## Flow 2: Games Tab Browse (headers + page)

This is the core browse list (Games tab).

### `HdrRow` -> `HdrRowRes` (headers for a view)

Inbound (example):
```xml
<HdrRow Cx="0x65" Vid="101" />\0
```

Outbound (known-good schema):
```xml
<HdrRowRes HR="0x00000000" Cx="0x65" Vid="101">
  <Hdrs H0="Rid" H1="GName" H2="GameV" H3="Locale" H4="IpAddr" H5="Ip2" H6="SFlags" H7="Flags"
        H8="Map" H9="World" H10="NumP" H11="MaxP" H12="Difficulty" H13="Time" H14="TimeL" H15="InGame" />
</HdrRowRes>\0
```

Critical rules:
- Headers are encoded as **attributes on `<Hdrs .../>`**.
- Do **not** include `Num="16"` as the first attribute; it shifts indices and breaks column mapping.
- Client requests many `Vid` values in a burst; the server responds consistently to keep cached state sane.

### `Page` -> `PageRes` (page of rows for a view)

Inbound (browse):
```xml
<Page Cx="0x0" Vid="101" PageNo="0" Num="0" Str="" />\0
```

Outbound (empty page):
```xml
<PageRes HR="0x00000000" Cx="0x0" Vid="101" ViewId="101"
         PageNo="0" PageNumber="0" VType="0" ViewType="0" VIdx="0" ViewIndex="0" VTotal="0" ViewTotal="0"
         Count="0" Num="0" Str="" />\0
```

Outbound (one row):
```xml
<PageRes HR="0x00000000" Cx="0x0" Vid="101" ViewId="101" PageNo="0" PageNumber="0"
         VType="0" ViewType="0" VIdx="0" ViewIndex="0" VTotal="0" ViewTotal="0"
         Count="1" Num="0" Str="">
  <Row Rid="1" GName="Example Game" GameV="1.11.0.1462" Locale="1033"
       IpAddr="192.0.2.10" Ip2="198.51.100.11"
       SFlags="16930" Flags="1c42a2"
       Map="Example Map" World="Regular"
       NumP="1" MaxP="8" Difficulty="1"
       Time="100" TimeL="0" InGame="" />
</PageRes>\0
```

Critical rules:
- Rows are encoded as **attributes on `<Row .../>`**, using header token names as attribute names.
- `Rid` must be a small signed int (do not use DPNID directly; it can overflow client `int` parsing).
- `IpAddr` must be non-empty for “Join” to proceed into the NetPipe join path.

## Flow 3: Details/Staging (row page)

This is *not* the actual transport join. It’s a “details refresh” step triggered by row actions / Join UI.

### `RowPg` -> `RowPgRes`

Inbound:
```xml
<RowPg Cx="0x16" Vid="301" Rid="1" Num="0" Str="" />\0
```

Outbound (miss):
```xml
<RowPgRes HR="0x00000000" Cx="0x16" Vid="301" Rid="1" Num="0" Str="" Count="0" />\0
```

Outbound (hit; must match the same `<Row .../>` attribute encoding as `PageRes`):
```xml
<RowPgRes HR="0x00000000" Cx="0x16" Vid="301" Rid="1" Num="0" Str="" Count="1">
  <Row Rid="1" ... />
</RowPgRes>\0
```

## Flow 4: Hosting + In-Progress Updates (HostData + SetLoc)

Host publishes state to the service. The server persists it and reflects it back in browse rows.

### `SetLoc` -> `SetLocRes`

Inbound:
```xml
<SetLoc Cx="0x0" Flags="32" Location="STAGING AREA=test game" />\0
```

Outbound:
```xml
<SetLocRes HR="0x00000000" Cx="0x0" Flags="32" Location="STAGING AREA=test game" />\0
```

### `HostData` -> `HostDataRes`

Inbound shape (nested payload; the server treats it as opaque XML-ish and scans `<Item .../>` tags):
```xml
<HostData Cx="0x0">
  <HostData>
    <New>
      <Item ItemId="0" ... game/session fields ... />
      <Item ItemId="2" ... player fields ... />
    </New>
    <Mod>...</Mod>
    <Del>...</Del>
  </HostData>
</HostData>\0
```

Outbound:
```xml
<HostDataRes HR="0x00000000" Cx="0x0" />\0
```

Semantics:
- `ItemId="0"` is the “server/session” item (game metadata shown in the browse row).
- other `ItemId` values represent players.
- A “delete-style” payload can appear as `<Del><Item Num="0"/><Item Num="2"/></Del>` (no `ItemId` attr).

## Flow 5: Join (important clarification)

The **Join button** ultimately triggers a DirectPlay join against the host IP:
- It requires `IpAddr` to be populated from the browse row.
- Transport join traffic can be direct to the host.

## Related: AutoUpdate (not DP8)

After connect, the game may perform AutoUpdate HTTP POSTs to port 80:
- `/us/updateserver.dll?checkclient`
- `/us/updateserver.dll?enumpackages&ver=3`

This project includes a best-effort TCP “sink” on `:80` that accepts and immediately closes connections (fail-fast).
