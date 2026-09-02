# LingCoWork Windows development startup script
# Usage: .\dev.ps1
# Options:
#   --no-backend    Start only frontend + electron
#   --no-frontend   Start only backend (electron will also be skipped)
#   --no-electron   Start without Electron, browser only
#   --fresh         Force reinstall dependencies (go mod download + pnpm install)

$ErrorActionPreference = "Stop"

Set-Location $PSScriptRoot

$WITH_BACKEND = $true
$WITH_FRONTEND = $true
$WITH_ELECTRON = $true
$FRESH = $false
$BACKEND_PORT = 9001
$FRONTEND_PORT = 5173
$LOG_DIR = Join-Path $PSScriptRoot "logs\dev"

$USAGE = "Usage: .\dev.ps1 [--no-backend] [--no-frontend] [--no-electron] [--fresh]"

# Parse arguments
foreach ($arg in $args) {
    switch ($arg) {
        "--no-frontend" { $WITH_FRONTEND = $false; $WITH_ELECTRON = $false }
        "--no-backend"  { $WITH_BACKEND = $false }
        "--no-electron" { $WITH_ELECTRON = $false }
        "--fresh"       { $FRESH = $true }
        "-h"            { Write-Host $USAGE; exit 0 }
        "--help"        { Write-Host $USAGE; exit 0 }
        default         { Write-Host "Unknown argument: $arg (use --help for usage)"; exit 1 }
    }
}

Write-Host "Starting LingCoWork development environment"
Write-Host ""

New-Item -ItemType Directory -Force -Path $LOG_DIR | Out-Null

# Tracked child processes: list of [pscustomobject]@{ Name; Process }
$script:children = New-Object System.Collections.ArrayList

function Get-PortPids {
    param([int]$Port)
    $conns = @(Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
    return @($conns | ForEach-Object { $_.OwningProcess } | Sort-Object -Unique)
}

function Test-PortInUse {
    param([int]$Port)
    return (@(Get-PortPids -Port $Port).Count -gt 0)
}

function Test-TcpReady {
    param([int]$Port, [int]$TimeoutMs = 500)
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect("127.0.0.1", $Port, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne($TimeoutMs, $false)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch {
        return $false
    } finally {
        $client.Close()
    }
}

function Test-HttpReady {
    param([string]$Url, [int]$TimeoutSec = 2)
    try {
        $r = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec $TimeoutSec -ErrorAction Stop
        return ($r.StatusCode -ge 200 -and $r.StatusCode -lt 400)
    } catch {
        return $false
    }
}

# Launch a shell command as a detached child, redirecting output to log files.
# Needed because pnpm/npm on Windows are .cmd/.ps1 shims, not Win32 executables,
# so Start-Process -FilePath "pnpm" fails with "%1 is not a valid Win32 application".
function Start-Child {
    param(
        [string]$Name,
        [string]$CommandLine,
        [string]$WorkingDirectory = $PSScriptRoot,
        [string]$LogFile
    )
    $psi = New-Object System.Diagnostics.ProcessStartInfo
    $psi.FileName = "$env:ComSpec"
    # /d skips AutoRun, /s + quoting keeps the whole command line intact
    $psi.Arguments = "/d /s /c `"$CommandLine > `"$LogFile`" 2>&1`""
    $psi.WorkingDirectory = $WorkingDirectory
    $psi.UseShellExecute = $false
    $psi.CreateNoWindow = $true
    $proc = [System.Diagnostics.Process]::Start($psi)
    [void]$script:children.Add([pscustomobject]@{ Name = $Name; Process = $proc })
    return $proc
}

# Decide whether `pnpm install` needs to run in a workspace directory.
#
# Checking only for node_modules/ existence is not enough: after a `git pull`
# that adds dependencies, node_modules/ still exists but is missing the new
# packages, and Vite then dies with a confusing
# "Failed to resolve import ..." instead of a dependency error. So compare
# manifest mtimes against node_modules/ and verify every declared dependency
# is actually present on disk.
function Test-NeedsInstall {
    param([string]$Directory)

    $nodeModules = Join-Path $Directory "node_modules"
    if (-not (Test-Path $nodeModules)) { return $true }

    $manifest = Join-Path $Directory "package.json"
    if (-not (Test-Path $manifest)) { return $false }

    $stamp = (Get-Item $nodeModules).LastWriteTime
    foreach ($f in @("package.json", "pnpm-lock.yaml")) {
        $p = Join-Path $Directory $f
        if ((Test-Path $p) -and (Get-Item $p).LastWriteTime -gt $stamp) { return $true }
    }

    try {
        $pkg = Get-Content $manifest -Raw | ConvertFrom-Json
    } catch {
        return $false
    }
    foreach ($section in @($pkg.dependencies, $pkg.devDependencies)) {
        if (-not $section) { continue }
        foreach ($name in $section.PSObject.Properties.Name) {
            if (-not (Test-Path (Join-Path $nodeModules $name))) { return $true }
        }
    }
    return $false
}

# Run `pnpm install` in a directory, failing the whole script if it errors.
function Invoke-PnpmInstall {
    param([string]$Directory, [string]$Label)
    Write-Host "  Installing ${Label} dependencies (pnpm install)..."
    Push-Location $Directory
    try {
        & cmd /d /s /c "pnpm install"
        if ($LASTEXITCODE -ne 0) {
            Write-Host "  pnpm install failed in ${Directory}"
            Invoke-Cleanup -ExitCode 1
        }
    } finally { Pop-Location }
}

function Show-LogTail {
    param([string]$LogFile, [int]$Lines = 20)
    if (Test-Path $LogFile) {
        Get-Content $LogFile -Tail $Lines -ErrorAction SilentlyContinue |
            ForEach-Object { Write-Host "    $_" }
    }
}

function Stop-Child {
    param($Entry)
    $proc = $Entry.Process
    if (-not $proc) { return }
    try { if ($proc.HasExited) { return } } catch { return }
    # /T kills the whole tree: cmd.exe -> pnpm/node/go -> compiled binary
    & taskkill.exe /PID $proc.Id /T /F 2>&1 | Out-Null
}

$script:cleanedUp = $false
function Invoke-Cleanup {
    param([int]$ExitCode = 0)
    if ($script:cleanedUp) { return }
    $script:cleanedUp = $true
    Write-Host ""
    Write-Host "Stopping all services..."
    foreach ($entry in @($script:children)) { Stop-Child -Entry $entry }
    Start-Sleep -Milliseconds 300
    exit $ExitCode
}

try {
    # ---------------- Backend (Go / Gin) ----------------
    if ($WITH_BACKEND) {
        Write-Host "Starting backend (Go/Gin, port ${BACKEND_PORT})..."

        if (Test-PortInUse -Port $BACKEND_PORT) {
            $pids = Get-PortPids -Port $BACKEND_PORT
            Write-Host "  Port ${BACKEND_PORT} is already in use"
            Write-Host "  Occupied PIDs: $($pids -join ' ')"
            Write-Host "  You can kill them manually: Stop-Process -Id $($pids -join ',') -Force"
            Invoke-Cleanup -ExitCode 1
        }

        if (-not (Test-Path ".env")) {
            if (Test-Path ".env.example") {
                Write-Host "  .env does not exist, copying from .env.example (remember to fill your API key)"
                Copy-Item ".env.example" ".env"
            } else {
                Write-Host "  Both .env and .env.example do not exist, backend cannot start"
                Invoke-Cleanup -ExitCode 1
            }
        }

        if ($FRESH -or -not (Test-Path (Join-Path $env:USERPROFILE "go\pkg\mod\cache\download"))) {
            Write-Host "  Downloading Go dependencies (go mod download)..."
            & go mod download
            if ($LASTEXITCODE -ne 0) {
                Write-Host "  go mod download failed"
                Invoke-Cleanup -ExitCode 1
            }
        }

        $backendLog = Join-Path $LOG_DIR "backend.log"
        $backendProcess = Start-Child -Name "backend" -CommandLine "go run ./cmd/api" -LogFile $backendLog

        Write-Host "  Waiting for 127.0.0.1:${BACKEND_PORT} ..."
        $ready = $false
        for ($i = 1; $i -le 120; $i++) {
            if (Test-TcpReady -Port $BACKEND_PORT) {
                Write-Host "  Backend is ready"
                $ready = $true
                break
            }
            if ($backendProcess.HasExited) {
                Write-Host "  Backend process exited, check ${backendLog} for details"
                Show-LogTail -LogFile $backendLog
                Invoke-Cleanup -ExitCode 1
            }
            Start-Sleep -Milliseconds 500
        }
        if (-not $ready) {
            Write-Host "  Not ready after 60s, check ${backendLog} for details"
            Show-LogTail -LogFile $backendLog
        }
    }

    # ---------------- Frontend (Vite) ----------------
    $frontendUrl = "http://localhost:${FRONTEND_PORT}"
    if ($WITH_FRONTEND) {
        Write-Host "Starting frontend (Vite, port ${FRONTEND_PORT})..."

        $FRONTEND_REUSED = $false
        if (Test-HttpReady -Url $frontendUrl) {
            Write-Host "  Frontend service already exists at ${frontendUrl}, reusing it"
            $FRONTEND_REUSED = $true
        }

        if (-not $FRONTEND_REUSED) {
            if (Test-PortInUse -Port $FRONTEND_PORT) {
                $pids = Get-PortPids -Port $FRONTEND_PORT
                Write-Host "  Port ${FRONTEND_PORT} is already in use, but not an accessible frontend service"
                Write-Host "  Occupied PIDs: $($pids -join ' ')"
                Invoke-Cleanup -ExitCode 1
            }

            $webDir = Join-Path $PSScriptRoot "web"
            if ($FRESH -or (Test-NeedsInstall -Directory $webDir)) {
                Invoke-PnpmInstall -Directory $webDir -Label "frontend"
            }

            $frontendLog = Join-Path $LOG_DIR "frontend.log"
            $frontendProcess = Start-Child -Name "frontend" -CommandLine "pnpm dev" `
                -WorkingDirectory (Join-Path $PSScriptRoot "web") -LogFile $frontendLog

            Write-Host "  Waiting for ${frontendUrl} ..."
            $ready = $false
            for ($i = 1; $i -le 60; $i++) {
                if (Test-HttpReady -Url $frontendUrl) {
                    Write-Host "  Frontend is ready"
                    $ready = $true
                    break
                }
                if ($frontendProcess.HasExited) {
                    Write-Host "  Frontend process exited, check ${frontendLog} for details"
                    Show-LogTail -LogFile $frontendLog
                    Invoke-Cleanup -ExitCode 1
                }
                Start-Sleep -Milliseconds 500
            }
            if (-not $ready) {
                Write-Host "  Not ready after 30s, check ${frontendLog} for details"
                Show-LogTail -LogFile $frontendLog
            }
        }
    }

    Write-Host ""
    Write-Host "LingCoWork is started"
    Write-Host ""
    if ($WITH_BACKEND)  { Write-Host "  backend  : http://localhost:${BACKEND_PORT}" }
    if ($WITH_FRONTEND) { Write-Host "  frontend : ${frontendUrl}" }

    # ---------------- Electron (dev shell) ----------------
    if ($WITH_ELECTRON) {
        Write-Host "Starting Electron desktop shell..."

        $electronDir = Join-Path $PSScriptRoot "electron"
        if ($FRESH -or (Test-NeedsInstall -Directory $electronDir)) {
            Invoke-PnpmInstall -Directory $electronDir -Label "Electron"
        }

        Write-Host "  Waiting for frontend ${frontendUrl} ..."
        $ready = $false
        for ($i = 1; $i -le 40; $i++) {
            if (Test-HttpReady -Url $frontendUrl) { $ready = $true; break }
            Start-Sleep -Milliseconds 500
        }
        if (-not $ready) {
            Write-Host "  Frontend not responding after 20s, still trying to start Electron (window may be blank)"
        }

        $electronLog = Join-Path $LOG_DIR "electron.log"
        $electronProcess = Start-Child -Name "electron" -CommandLine "pnpm start" `
            -WorkingDirectory (Join-Path $PSScriptRoot "electron") -LogFile $electronLog

        Start-Sleep -Seconds 2
        if ($electronProcess.HasExited) {
            Write-Host "  Electron process exited, check ${electronLog} for details"
            Show-LogTail -LogFile $electronLog
            Invoke-Cleanup -ExitCode 1
        }
        Write-Host "  electron : Started (set INTERVIEW_ELECTRON_DEVTOOLS=1 to auto-open DevTools)"
    }

    Write-Host ""
    if ($WITH_ELECTRON) {
        Write-Host "  Log directory : ${LOG_DIR}\ (backend.log / frontend.log / electron.log)"
    } else {
        Write-Host "  Log directory : ${LOG_DIR}\ (backend.log / frontend.log)"
    }
    Write-Host "  Press Ctrl+C to stop all services"
    Write-Host ""

    # ---------------- Supervise: exit as soon as any child dies ----------------
    if ($script:children.Count -eq 0) {
        Write-Host "  Nothing to supervise, exiting"
    } else {
        while ($true) {
            foreach ($entry in @($script:children)) {
                if ($entry.Process.HasExited) {
                    Write-Host ""
                    Write-Host "Process '$($entry.Name)' exited with code $($entry.Process.ExitCode), shutting down the rest"
                    Invoke-Cleanup -ExitCode $entry.Process.ExitCode
                }
            }
            Start-Sleep -Milliseconds 500
        }
    }
} finally {
    # Runs on normal exit and on Ctrl+C
    Invoke-Cleanup
}
