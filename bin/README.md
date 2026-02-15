# bin

This folder holds runtime binaries that the Go app loads.

## Required

- `dp8shim.dll`
  - Built from `dp8shim/dp8shim.cpp`.
  - Build it via:
    - `cd dp8shim; .\\build.ps1`

## Optional (debugging)

- `dp8shim.pdb`
  - Copied into this folder automatically by the build scripts when present.

## System Dependencies (Windows)

- DirectPlay8 runtime (`dpnet.dll`) is part of Windows.
