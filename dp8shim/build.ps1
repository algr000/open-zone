$ErrorActionPreference = "Stop"

$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$binDir = Join-Path (Resolve-Path (Join-Path $here "..")).Path "bin"
$tmpDir = Join-Path $here "tmp"

if (-not (Test-Path $tmpDir)) {
  New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
}
if (-not (Test-Path $binDir)) {
  New-Item -ItemType Directory -Force -Path $binDir | Out-Null
}

function Resolve-DirectPlayIncludeDir {
  param([string]$Here)

  $vendored = Join-Path $Here "include"
  if (Test-Path (Join-Path $vendored "dplay8.h")) { return $vendored }
  if ($env:DXSDK_DIR) {
    $dx = Join-Path $env:DXSDK_DIR "Include"
    if (Test-Path (Join-Path $dx "dplay8.h")) { return $dx }
  }
  $default = "C:\\Program Files (x86)\\Microsoft DirectX SDK (August 2007)\\Include"
  if (Test-Path (Join-Path $default "dplay8.h")) { return $default }
  return ""
}

$includeDir = Resolve-DirectPlayIncludeDir -Here $here
if (-not $includeDir) {
  throw "DirectPlay headers not found. Install the DirectX SDK (August 2007) or set DXSDK_DIR, then re-run."
}

$outDll = Join-Path $binDir "dp8shim.dll"
$outPdb = Join-Path $binDir "dp8shim.pdb"
if (Test-Path $outDll) {
  try {
    $fs = [System.IO.File]::Open($outDll, [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
    $fs.Close()
  } catch {
    throw "bin/dp8shim.dll is in use. Stop open-zone (or any process using the shim) and re-run."
  }
}

# If cl.exe isn't already available (Developer Command Prompt), locate vcvars64.bat via vswhere.
$vcvars64 = $null
$needVcVars = -not $env:VSCMD_VER
if ($needVcVars) {
  $vswhere = Join-Path ${env:ProgramFiles(x86)} "Microsoft Visual Studio\\Installer\\vswhere.exe"
  if (Test-Path $vswhere) {
    $installPath = (& $vswhere -latest -products * -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 -property installationPath).Trim()
    if ($installPath) {
      $candidate = Join-Path $installPath "VC\\Auxiliary\\Build\\vcvars64.bat"
      if (Test-Path $candidate) { $vcvars64 = $candidate }
    }
  }

  if (-not $vcvars64) {
    $fallback = Join-Path ${env:ProgramFiles(x86)} "Microsoft Visual Studio\\2022\\BuildTools\\VC\\Auxiliary\\Build\\vcvars64.bat"
    if (Test-Path $fallback) { $vcvars64 = $fallback }
  }
  if (-not $vcvars64) { throw "vcvars64.bat not found. Install VS 2022 Build Tools (Desktop development with C++)." }
}

Write-Host "Building dp8shim.dll -> $binDir\\dp8shim.dll"
Write-Host "Using DirectPlay headers from: $includeDir"

try {
  if ($vcvars64) {
    # Use cmd.exe so vcvars64.bat can set INCLUDE/LIB/PATH for cl.exe and the Windows SDK.
    $cmd = @(
      "call `"$vcvars64`" >nul",
      "cd /d `"$here`"",
      "cl /nologo /EHsc /LD dp8shim.cpp /I `"$includeDir`" /Fo`"$tmpDir\\dp8shim.obj`" /Fd`"$tmpDir\\dp8shim.pdb`" /link /OUT:`"$binDir\\dp8shim.dll`" /PDB:`"$binDir\\dp8shim.pdb`" ole32.lib uuid.lib"
    ) -join " && "

    & cmd.exe /c $cmd
    if ($LASTEXITCODE -ne 0) { throw "Build failed (exit=$LASTEXITCODE)" }
  } else {
    Push-Location $here
    try {
      & cl.exe /nologo /EHsc /LD dp8shim.cpp /I $includeDir /Fo"$tmpDir\\dp8shim.obj" /Fd"$tmpDir\\dp8shim.pdb" /link /OUT:"$binDir\\dp8shim.dll" /PDB:"$binDir\\dp8shim.pdb" ole32.lib uuid.lib
      if ($LASTEXITCODE -ne 0) { throw "Build failed (exit=$LASTEXITCODE)" }
    } finally {
      Pop-Location
    }
  }

  Write-Host "OK"
} finally {
  # Cleanup: keep bin\dp8shim.dll (and optionally bin\dp8shim.pdb).
  # Remove intermediates even if the build fails so a retry starts clean.
  try {
    if (Test-Path $tmpDir) {
      Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $tmpDir
    }

    $localArtifacts = @(
      (Join-Path $here "dp8shim.exp"),
      (Join-Path $here "dp8shim.lib"),
      (Join-Path $here "dp8shim.ilk"),
      (Join-Path $here "dp8shim.obj"),
      (Join-Path $here "dp8shim.pdb"),
      (Join-Path $here "vc140.pdb"),
      (Join-Path $here "vc141.pdb"),
      (Join-Path $here "vc142.pdb"),
      (Join-Path $here "vc143.pdb")
    )
    foreach ($p in $localArtifacts) {
      if (Test-Path $p) {
        Remove-Item -Force -ErrorAction SilentlyContinue $p
      }
    }

    $binStale = @(
      (Join-Path $binDir "dp8shim.exp"),
      (Join-Path $binDir "dp8shim.lib"),
      (Join-Path $binDir "dp8shim.ilk"),
      (Join-Path $binDir "dp8shim.log"),
      (Join-Path $binDir "dp8recv_last.bin")
    )
    foreach ($p in $binStale) {
      if (Test-Path $p) {
        Remove-Item -Force -ErrorAction SilentlyContinue $p
      }
    }
  } catch {
    # Best-effort cleanup; do not fail the build.
  }
}
