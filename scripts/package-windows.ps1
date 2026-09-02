$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$RootDir = Split-Path -Parent $PSScriptRoot
$WebDir = Join-Path $RootDir "web"
$ElectronDir = Join-Path $RootDir "electron"
$ResourceDir = Join-Path $ElectronDir "package-resources"
$BackendDir = Join-Path $ResourceDir "backend"
$WebResourceDir = Join-Path $ResourceDir "web"
$ReleaseDir = Join-Path $RootDir "release"

function Assert-LastExitCode {
    param([string]$Step)
    if ($LASTEXITCODE -ne 0) {
        throw "$Step failed with exit code $LASTEXITCODE."
    }
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "Windows packaging must run on Windows."
}
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go is required to build the backend."
}
if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
    throw "pnpm is required to build the desktop application."
}
if (-not (Test-Path (Join-Path $ElectronDir "assets\icon.ico"))) {
    throw "electron\assets\icon.ico is required for Windows packaging."
}

Push-Location $RootDir
try {
    $previousCI = $env:CI
    try {
        # pnpm otherwise refuses to refresh node_modules in non-interactive
        # packaging sessions when its store location or version changed.
        $env:CI = "true"

        Write-Host "Installing frontend dependencies..."
        & pnpm --dir $WebDir install --frozen-lockfile
        Assert-LastExitCode "Frontend dependency installation"

        Write-Host "Installing Electron dependencies..."
        & pnpm --dir $ElectronDir install --frozen-lockfile
        Assert-LastExitCode "Electron dependency installation"
    } finally {
        $env:CI = $previousCI
    }

    Remove-Item -Recurse -Force $ResourceDir -ErrorAction SilentlyContinue
    New-Item -ItemType Directory -Force -Path $BackendDir, $WebResourceDir, $ReleaseDir |
        Out-Null

    Write-Host "Building web application..."
    & pnpm --dir $WebDir build
    Assert-LastExitCode "Web build"

    Write-Host "Building Windows backend sidecar..."
    $previousCgoEnabled = $env:CGO_ENABLED
    $previousGoos = $env:GOOS
    $previousGoarch = $env:GOARCH
    try {
        $env:CGO_ENABLED = "0"
        $env:GOOS = "windows"
        $env:GOARCH = "amd64"
        & go build -trimpath -ldflags="-s -w" `
            -o (Join-Path $BackendDir "lingcowork-api.exe") `
            ".\cmd\api"
        Assert-LastExitCode "Backend build"
    } finally {
        $env:CGO_ENABLED = $previousCgoEnabled
        $env:GOOS = $previousGoos
        $env:GOARCH = $previousGoarch
    }

    Copy-Item -Recurse -Force (Join-Path $WebDir "dist\*") $WebResourceDir

    Write-Host "Compiling Electron application..."
    & pnpm --dir $ElectronDir build
    Assert-LastExitCode "Electron TypeScript build"

    Write-Host "Creating Windows x64 packages..."
    & pnpm --dir $ElectronDir dist:win
    Assert-LastExitCode "Electron Builder"

    Write-Host ""
    Write-Host "LingCoWork Windows artifacts:"
    Get-ChildItem $ReleaseDir |
        Where-Object { $_.Name -like "LingCoWork*" -or $_.Name -eq "win-unpacked" } |
        ForEach-Object { Write-Host "  $($_.FullName)" }
} finally {
    Pop-Location
}
