# dp8shim

Tiny native shim that hosts a **DirectPlay8 Server** (implemented by `dpnet.dll`).

Why a shim:
- DirectPlay8 is a **COM** API (`IDirectPlay8Server`, `IDirectPlay8Address`).
- Go can call COM, but it is much less painful to keep COM + callbacks in a small native DLL and call it from Go.

## Runtime Prereqs

- DirectPlay8 runtime (`dpnet.dll`) is part of Windows.
- The COM class for DirectPlay8Server is registered.

## Build Prereqs

You need Visual Studio / MSVC Build Tools with **Desktop development with C++**.

You also need DirectPlay headers (`dplay8.h`, `dpaddr.h`, `dplay.h`).

Common locations that can found after installing the game:
- `C:\\Program Files (x86)\\Microsoft DirectX SDK (August 2007)\\Include\\`

This repo does not vendor those SDK headers by default. The build script will try to locate them automatically.

## Build

```powershell
cd dp8shim
.\build.ps1
```

Note: stop `open-zone` before building. Windows locks loaded DLLs.

## What It Does

- `DP8_StartServer(port)`:
  - `CoInitializeEx`
  - `CoCreateInstance(CLSID_DirectPlay8Server)`
  - Create a `DirectPlay8Address` with `CLSID_DP8SP_TCPIP` and bind the port
  - `Host()` with a fixed `DPN_APPLICATION_DESC.guidApplication`
- Accepts connections by returning `S_OK` on `DPN_MSGID_INDICATE_CONNECT`.

## Verify Exports (No dumpbin Needed)

`open-zone` requires these exports:
- `DP8_StartServer`
- `DP8_StopServer`
- `DP8_PopEvent`
- `DP8_SendTo`
- (optional) `DP8_GetQueueDepth`

You can verify exports via Python (no extra deps):

```powershell
cd .
@'
import struct
from pathlib import Path
p=Path(r"dp8shim/dp8shim.dll")
pe=p.read_bytes()
e_lfanew=struct.unpack_from("<I",pe,0x3c)[0]
num_sections=struct.unpack_from("<H",pe,e_lfanew+6)[0]
opt_size=struct.unpack_from("<H",pe,e_lfanew+20)[0]
opt_off=e_lfanew+24
magic=struct.unpack_from("<H",pe,opt_off)[0]
dd_off=opt_off+(96 if magic==0x10b else 112)
export_rva,_=struct.unpack_from("<II",pe,dd_off)
sec_off=opt_off+opt_size
secs=[]
for i in range(num_sections):
    off=sec_off+i*40
    vsize,vaddr,rsize,rptr=struct.unpack_from("<IIII",pe,off+8)
    secs.append((vaddr,max(vsize,rsize),rptr))
def rva_to_off(rva):
    for vaddr,sz,rptr in secs:
        if vaddr<=rva<vaddr+sz:
            return rptr+(rva-vaddr)
    raise KeyError(rva)
eoff=rva_to_off(export_rva)
(_,_,_,_,name_rva,_,_,num_names,_,addr_names_rva,_)=struct.unpack_from("<IIHHIIIIIII",pe,eoff)
names_off=rva_to_off(addr_names_rva)
names=[]
for i in range(num_names):
    nrva=struct.unpack_from("<I",pe,names_off+i*4)[0]
    noff=rva_to_off(nrva)
    names.append(pe[noff:pe.index(b"\\0",noff)].decode("ascii","ignore"))
print("exports:",sorted(names))
'@ | python -
```

## Current App GUID

From on-wire `7F..` 96-byte frame, a GUID-looking value was extracted:

`77E2D9C2-504E-459F-8416-0848130BBE1E`

The shim uses this as `DPN_APPLICATION_DESC.guidApplication` for now.
