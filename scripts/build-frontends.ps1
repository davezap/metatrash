<#
.SYNOPSIS
  Builds Slate and Slate Viewer's website build, and copies them into Metatrash.

.DESCRIPTION
  Metatrash ships two front ends that live in their own repositories:

    Slate         slate.js, slate.css and mermaid/  ->  internal/service/web/
                  (and the same files into Slate Viewer's public/slate/)
    Slate Viewer  npm run build:web (dist-web/)     ->  internal/service/web/app/

  This script builds both from the sibling folders next to this checkout,
  copies the results in, and writes internal/service/web/frontends.json,
  which records the version and commit of each, so every Metatrash commit
  says exactly which front-end sources it carries.

  It changes files only; it never commits. Review with git status, then
  commit Slate, Slate Viewer and Metatrash as usual.

.PARAMETER Slate
  The Slate checkout. Default: ..\..\Slate from this script.

.PARAMETER Viewer
  The Slate Viewer checkout. Default: ..\..\Slate_Viewer from this script.

.PARAMETER SkipSlate
  Don't rebuild Slate: copy its committed slate.js, slate.css and mermaid/.

.PARAMETER Install
  Run npm ci in both repositories first (after pulling new dependencies).

.PARAMETER Test
  Run go vet and go test in Metatrash afterwards.

.EXAMPLE
  powershell -ExecutionPolicy Bypass -File scripts\build-frontends.ps1
#>
[CmdletBinding()]
param(
  [string]$Slate = '',
  [string]$Viewer = '',
  [switch]$SkipSlate,
  [switch]$Install,
  [switch]$Test
)

$ErrorActionPreference = 'Stop'
# Windows PowerShell 5.1 leaves $PSScriptRoot empty inside param(), so the defaults are set here.
$Here = if ($PSScriptRoot) { $PSScriptRoot } else { Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $Slate) { $Slate = Join-Path $Here '..\..\Slate' }
if (-not $Viewer) { $Viewer = Join-Path $Here '..\..\Slate_Viewer' }
$Metatrash = (Resolve-Path (Join-Path $Here '..')).Path
$Slate = (Resolve-Path $Slate).Path
$Viewer = (Resolve-Path $Viewer).Path
$Web = Join-Path $Metatrash 'internal\service\web'
$App = Join-Path $Web 'app'

function Step($text) { Write-Host ''; Write-Host "== $text" -ForegroundColor Cyan }

# Runs a command in a folder and stops the script if it fails.
function Run($dir, $exe, [string[]]$arguments) {
  Write-Host "> $exe $($arguments -join ' ')   ($dir)" -ForegroundColor DarkGray
  Push-Location $dir
  try {
    & $exe @arguments
    if ($LASTEXITCODE -ne 0) { throw "$exe $($arguments -join ' ') failed in $dir (exit $LASTEXITCODE)." }
  } finally {
    Pop-Location
  }
}

# Replaces a folder's contents with another folder's (both must be ours: never a root).
function Replace-Folder($from, $to) {
  if (-not (Test-Path (Join-Path $from '*'))) { throw "$from is empty or missing." }
  if (Test-Path $to) { Remove-Item -LiteralPath $to -Recurse -Force }
  Copy-Item -LiteralPath $from -Destination $to -Recurse -Force
}

function Copy-Files($from, $to, [string[]]$names) {
  foreach ($name in $names) {
    $source = Join-Path $from $name
    if (-not (Test-Path $source)) { throw "$source is missing." }
    Copy-Item -LiteralPath $source -Destination (Join-Path $to $name) -Force
  }
}

# Version (package.json), commit and whether the checkout has uncommitted changes.
function Describe($dir) {
  $package = Get-Content -Raw (Join-Path $dir 'package.json') | ConvertFrom-Json
  $commit = (& git -C $dir rev-parse --short HEAD).Trim()
  $changes = @(& git -C $dir status --porcelain)
  [ordered]@{ version = $package.version; commit = $commit; uncommitted = ($changes.Count -gt 0) }
}

foreach ($dir in @($Slate, $Viewer)) {
  if (-not (Test-Path (Join-Path $dir 'package.json'))) { throw "$dir is not a checkout (no package.json)." }
}
foreach ($tool in @('npm', 'git')) {
  if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "$tool is not on PATH." }
}

$slateInfo = Describe $Slate
$viewerInfo = Describe $Viewer
Write-Host "Slate        $($slateInfo.version) ($($slateInfo.commit))  $Slate"
Write-Host "Slate Viewer $($viewerInfo.version) ($($viewerInfo.commit))  $Viewer"
Write-Host "Metatrash    $Metatrash"
foreach ($pair in @(@('Slate', $slateInfo), @('Slate Viewer', $viewerInfo))) {
  if ($pair[1].uncommitted) { Write-Warning "$($pair[0]) has uncommitted changes: the build records that, so commit them before the Metatrash commit if you can." }
}

if ($Install) {
  Step 'Installing dependencies (npm ci)'
  if (-not $SkipSlate) { Run $Slate 'npm' @('ci') }
  Run $Viewer 'npm' @('ci')
}

if ($SkipSlate) {
  Step 'Slate: using its committed build'
} else {
  Step 'Slate: npm run build'
  if (-not (Test-Path (Join-Path $Slate 'node_modules'))) { Run $Slate 'npm' @('ci') }
  Run $Slate 'npm' @('run', 'build')
}

Step 'Slate -> Slate Viewer public\slate'
$viewerSlate = Join-Path $Viewer 'public\slate'
Copy-Files $Slate $viewerSlate @('slate.js', 'slate.css', 'slate-editor.d.ts', 'CHANGELOG.md')
Replace-Folder (Join-Path $Slate 'mermaid') (Join-Path $viewerSlate 'mermaid')
Replace-Folder (Join-Path $Slate 'themes') (Join-Path $viewerSlate 'themes')

Step 'Slate -> Metatrash internal\service\web'
Copy-Files $Slate $Web @('slate.js', 'slate.css')
Replace-Folder (Join-Path $Slate 'mermaid') (Join-Path $Web 'mermaid')

Step 'Slate Viewer: npm run build:web'
if (-not (Test-Path (Join-Path $Viewer 'node_modules'))) { Run $Viewer 'npm' @('ci') }
Run $Viewer 'npm' @('run', 'build:web')

Step 'Slate Viewer dist-web -> Metatrash internal\service\web\app'
$dist = Join-Path $Viewer 'dist-web'
if (-not (Test-Path (Join-Path $dist '.vite\manifest.json'))) { throw "$dist has no .vite\manifest.json: the website build did not finish." }
# Keep app\README.md; everything else is the build.
Replace-Folder (Join-Path $dist 'assets') (Join-Path $App 'assets')
Replace-Folder (Join-Path $dist '.vite') (Join-Path $App '.vite')

Step 'Recording versions in internal\service\web\frontends.json'
$stamp = [ordered]@{
  note   = 'Written by scripts/build-frontends.ps1: the front-end sources this Metatrash build carries.'
  built  = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')
  slate  = $slateInfo
  viewer = $viewerInfo
}
$json = ($stamp | ConvertTo-Json -Depth 4) -replace "`r`n", "`n"
[IO.File]::WriteAllText((Join-Path $Web 'frontends.json'), $json + "`n", (New-Object Text.UTF8Encoding($false)))

if ($Test) {
  Step 'Metatrash: go vet and go test'
  Run $Metatrash 'go' @('vet', './...')
  Run $Metatrash 'go' @('test', './...')
}

Step 'Done'
Write-Host "Slate $($slateInfo.version) and Slate Viewer $($viewerInfo.version) are in Metatrash. Review the changes:"
Write-Host "  git -C `"$Slate`" status --short"
Write-Host "  git -C `"$Viewer`" status --short"
Write-Host "  git -C `"$Metatrash`" status --short"
