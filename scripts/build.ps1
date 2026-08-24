[CmdletBinding()]
param(
  [string]$Version = "1.2.0",
  [string]$GoOS = $env:GOOS,
  [string]$GoArch = $env:GOARCH,
  [string]$OutputRoot = "release"
)

$ErrorActionPreference = "Stop"
if ([string]::IsNullOrWhiteSpace($GoOS)) { $GoOS = "windows" }
if ([string]::IsNullOrWhiteSpace($GoArch)) { $GoArch = "amd64" }

$extension = switch ($GoOS) {
  "windows" { ".dll" }
  "darwin" { ".dylib" }
  default { ".so" }
}
$pluginID = "usage-keeper"
$assetName = "${pluginID}_${Version}_${GoOS}_${GoArch}"
$releaseDir = Join-Path (Resolve-Path (Join-Path $PSScriptRoot "..")) $OutputRoot
$workDir = Join-Path $releaseDir $assetName
$zipPath = Join-Path $releaseDir ($assetName + ".zip")
$binaryName = $pluginID + $extension

New-Item -ItemType Directory -Force -Path $workDir | Out-Null
Push-Location (Join-Path $PSScriptRoot "..\go")
try {
  $env:CGO_ENABLED = "1"
  $env:GOOS = $GoOS
  $env:GOARCH = $GoArch
  go build -trimpath -buildmode=c-shared -ldflags "-s -w -X main.pluginVersion=$Version" -o (Join-Path $workDir $binaryName) .
  if ($LASTEXITCODE -ne 0) { throw "go build failed" }
} finally {
  Pop-Location
}

if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath -Force }
Compress-Archive -Path (Join-Path $workDir "*") -DestinationPath $zipPath -CompressionLevel Optimal
$hash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
Set-Content -LiteralPath (Join-Path $releaseDir "checksums.txt") -Value ("{0}  {1}" -f $hash, (Split-Path $zipPath -Leaf)) -Encoding ascii
Write-Output (Get-Item -LiteralPath $zipPath).FullName
