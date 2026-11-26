# Install pawabase-connector on Windows.
#
#   irm https://raw.githubusercontent.com/Pawabase/pawabase-connector/main/install.ps1 | iex
#
# Settings (environment variables):
#   PAWABASE_CONNECTOR_VERSION   a release tag such as v0.1.0 (default: the latest release)
#   PAWABASE_CONNECTOR_DIR       where to put the program (default: Program Files as Administrator, else your local programs folder)
#   PAWABASE_CONNECTOR_BASE_URL  where releases are downloaded from (default: the GitHub releases of Pawabase/pawabase-connector)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$Repo = 'Pawabase/pawabase-connector'
$Binary = 'pawabase-connector'
$Version = if ($env:PAWABASE_CONNECTOR_VERSION) { $env:PAWABASE_CONNECTOR_VERSION } else { 'latest' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "pawabase-connector install: unsupported processor $($env:PROCESSOR_ARCHITECTURE)" }
}

if ($env:PAWABASE_CONNECTOR_BASE_URL) { $base = $env:PAWABASE_CONNECTOR_BASE_URL.TrimEnd('/') }
elseif ($Version -eq 'latest') { $base = "https://github.com/$Repo/releases/latest/download" }
else { $base = "https://github.com/$Repo/releases/download/$Version" }
$asset = "${Binary}_windows_${arch}.zip"

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("pawabase-connector-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "Downloading $asset ($Version)..."
  Invoke-WebRequest -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset) -UseBasicParsing
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp 'checksums.txt') -UseBasicParsing

  $line = Get-Content (Join-Path $tmp 'checksums.txt') | Where-Object { $_ -match "\s$([regex]::Escape($asset))$" } | Select-Object -First 1
  if (-not $line) { throw "pawabase-connector install: $asset is not in checksums.txt" }
  $expected = ($line -split '\s+')[0].ToLower()
  $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
  if ($expected -ne $actual) { throw "pawabase-connector install: checksum mismatch for ${asset}: the download is not the published release" }

  Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force

  $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
  if ($env:PAWABASE_CONNECTOR_DIR) { $dir = $env:PAWABASE_CONNECTOR_DIR }
  elseif ($admin) { $dir = Join-Path $env:ProgramFiles 'Pawabase Connector' }
  else { $dir = Join-Path $env:LOCALAPPDATA 'Programs\pawabase-connector' }
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Copy-Item (Join-Path $tmp "$Binary.exe") (Join-Path $dir "$Binary.exe") -Force

  $scope = if ($admin) { 'Machine' } else { 'User' }
  $path = [Environment]::GetEnvironmentVariable('Path', $scope)
  if (($path -split ';') -notcontains $dir) {
    [Environment]::SetEnvironmentVariable('Path', ($path.TrimEnd(';') + ";$dir"), $scope)
    $env:Path = "$env:Path;$dir"
    Write-Host "Added $dir to your PATH (new windows pick it up)."
  }

  & (Join-Path $dir "$Binary.exe") version
  Write-Host ''
  Write-Host 'Next, in a PowerShell window opened as Administrator, with the token Studio showed you:'
  Write-Host "  $Binary enroll --server <your Pawabase address> --token <pbce_...>"
  Write-Host "  $Binary service install"
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
