param(
  [string]$OutDir = "deploy"
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Ensure-Dir([string]$p) {
  New-Item -ItemType Directory -Force -Path $p | Out-Null
}

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $here

Push-Location $root
try {
  # Fresh deploy directory.
  Remove-Item -Recurse -Force $OutDir -ErrorAction SilentlyContinue
  Ensure-Dir $OutDir
  Ensure-Dir (Join-Path $OutDir "bin")
  Ensure-Dir (Join-Path $OutDir "config")

  # Best-effort shim rebuild. If in use, keep existing DLL.
  try {
    & pwsh -NoProfile -ExecutionPolicy Bypass -File (Join-Path $root "dp8shim\\build.ps1")
  } catch {
    Write-Warning $_.Exception.Message
  }

  # Build server binary for Windows x64.
  $env:GOOS = "windows"
  $env:GOARCH = "amd64"
  $env:CGO_ENABLED = "0"
  & go build -trimpath -ldflags "-s -w" -o (Join-Path $OutDir "open-zone.exe") .\\cmd\\open-zone

  # Stage runtime artifacts.
  Copy-Item -Force .\\bin\\dp8shim.dll (Join-Path $OutDir "bin\\dp8shim.dll")
  if (Test-Path .\\bin\\dp8shim.pdb) {
    Copy-Item -Force .\\bin\\dp8shim.pdb (Join-Path $OutDir "bin\\dp8shim.pdb")
  }
  Copy-Item -Force .\\config\\config.yaml (Join-Path $OutDir "config\\config.yaml")
  Copy-Item -Force .\\README.md (Join-Path $OutDir "README.md")
  Copy-Item -Force .\\run-server.bat (Join-Path $OutDir "run-server.bat")
  if (Test-Path .\\docs) {
    Copy-Item -Recurse -Force .\\docs (Join-Path $OutDir "docs")
  }
} finally {
  Pop-Location
}

