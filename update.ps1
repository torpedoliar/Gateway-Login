<#
.SYNOPSIS
    One-click update script for Gateway Login (Windows).
.DESCRIPTION
    Mirrors update.sh for hosts without bash. Requires:
      - Docker Desktop with WSL2
      - git for Windows
      - curl (bundled with Windows 10+)
    Run from the project root in PowerShell.
.PARAMETER NoBackup
    Skip the database backup step.
.PARAMETER Ref
    Git ref to pull (default: main).
.PARAMETER ComposeFile
    Path to the compose file (default: deploy/docker-compose.prod.yml).
.EXAMPLE
    .\update.ps1
    .\update.ps1 -NoBackup
    .\update.ps1 -Ref release/1.2
#>

[CmdletBinding()]
param(
    [switch]$NoBackup,
    [string]$Ref = "main",
    [string]$ComposeFile = "deploy/docker-compose.prod.yml"
)

$ErrorActionPreference = "Stop"

function Step($n, $msg) { Write-Host "`n[$n] $msg" -ForegroundColor Cyan }
function Ok($msg)       { Write-Host "  -> $msg" -ForegroundColor Green }
function Warn($msg)     { Write-Host "  WARNING: $msg" -ForegroundColor Yellow }
function Err($msg)      { Write-Host "  ERROR: $msg" -ForegroundColor Red; exit 1 }

# ----- Sanity -----
if (-not (Test-Path $ComposeFile) -and -not (Test-Path "deploy/docker-compose.yml")) {
    Err "no compose file found at $ComposeFile"
}
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    Err "docker not found in PATH"
}
if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
    Err "git not found in PATH"
}

$compose = @("docker", "compose", "-f", $ComposeFile)
$backupFile = ""

Write-Host ""
Write-Host "============================================"
Write-Host "  Gateway Login - Update (Windows)"
Write-Host "  compose: $ComposeFile"
Write-Host "  ref:     $Ref"
Write-Host "============================================"
Write-Host ""

# ----- Step 1: backup -----
if (-not $NoBackup) {
    Step "1/9" "Backing up database..."
    $backupDir = "backups"
    if (-not (Test-Path $backupDir)) { New-Item -ItemType Directory -Path $backupDir | Out-Null }
    $ts = Get-Date -Format "yyyyMMdd_HHmmss"
    $backupFile = "$backupDir\db_backup_$ts.sql.gz"

    $running = & docker compose -f $ComposeFile ps --status running postgres 2>$null
    if ($LASTEXITCODE -eq 0 -and $running -match "postgres") {
        & docker compose -f $ComposeFile exec -T postgres pg_dump -U sso -d sso --no-owner --no-acl `
            | gzip -9 | Set-Content -Path $backupFile -Encoding Byte
        if ((Get-Item $backupFile).Length -gt 0) {
            Ok "backup -> $backupFile ($([math]::Round((Get-Item $backupFile).Length/1KB,1)) KB)"
        } else {
            Warn "backup file is empty"
        }
    } else {
        Warn "postgres container not running; skipping backup"
    }
} else {
    Step "1/9" "Skipping backup (-NoBackup)"
}

# ----- Step 2: pull code -----
Step "2/9" "Pulling latest code (ref=$Ref)..."
& git pull origin $Ref
if ($LASTEXITCODE -ne 0) { Err "git pull failed; try: git stash && git pull origin $Ref && git stash pop" }
Ok "code updated"

# ----- Step 3: detect changes -----
Step "3/9" "Detecting relevant changes..."
$diff = & git diff HEAD~1 --name-only 2>$null
$schemaChanged = $diff | Where-Object { $_ -match "internal/db/migrations/" }
$configChanged = $diff | Where-Object { $_ -match "internal/(store|setup)/" }
$deployChanged = $diff | Where-Object { $_ -match "^deploy/" }

$needsMigration = $false
if ($schemaChanged) {
    $needsMigration = $true
    Write-Host "  schema migrations changed:" -ForegroundColor Yellow
    $schemaChanged | ForEach-Object { Write-Host "    $_" }
} else {
    Ok "no schema changes"
}
if ($configChanged) { Warn "config schema changed: re-running setup may be required" }
if ($deployChanged) { Warn "deploy files changed: nginx restart recommended" }

# ----- Step 4: rebuild -----
Step "4/9" "Rebuilding images (this may take 2-5 minutes)..."
& docker compose -f $ComposeFile build --no-cache
if ($LASTEXITCODE -ne 0) { Err "build failed" }
Ok "build ok"

# ----- Step 5: stop -----
Step "5/9" "Recreating containers..."
& docker compose -f $ComposeFile down --remove-orphans

# ----- Step 6: start -----
Step "6/9" "Starting containers..."
& docker compose -f $ComposeFile up -d
Start-Sleep -Seconds 5

# ----- Step 7: migrations -----
if ($needsMigration) {
    Step "7/9" "Running database migrations..."
    $env:MIGRATIONS_PATH = "./internal/db/migrations"
    & docker compose -f $ComposeFile run --rm setup
    if ($LASTEXITCODE -ne 0) { Warn "migrations exited non-zero" }
    Ok "migrations applied"
} else {
    Step "7/9" "Skipping migrations (none changed)"
}

# ----- Step 8: recover stale sync_runs -----
Step "8/9" "Recovering stale running sync_runs (bouncing sync)..."
& docker compose -f $ComposeFile restart sync
Start-Sleep -Seconds 2

# ----- Step 9: health check + cleanup -----
Step "9/9" "Health check + cleanup..."
$healthy = $false
for ($i = 1; $i -le 10; $i++) {
    try {
        $r = Invoke-WebRequest -Uri "http://localhost:8080/healthz" -UseBasicParsing -TimeoutSec 2
        if ($r.StatusCode -eq 200) { $healthy = $true; break }
    } catch {
        try {
            & docker compose -f $ComposeFile exec -T api wget -q -O - http://localhost:8080/healthz > $null 2>&1
            if ($LASTEXITCODE -eq 0) { $healthy = $true; break }
        } catch {}
    }
    Write-Host "  waiting for api /healthz (attempt $i/10)..."
    Start-Sleep -Seconds 2
}
if ($healthy) { Ok "api /healthz: ok" } else { Warn "api /healthz did not respond within 20s" }

# Keep last 5 backups
if (-not $NoBackup -and (Test-Path "backups")) {
    Get-ChildItem backups\db_backup_*.sql.gz -ErrorAction SilentlyContinue `
        | Sort-Object LastWriteTime -Descending `
        | Select-Object -Skip 5 `
        | Remove-Item -Force
}

# ----- Done -----
Write-Host ""
Write-Host "============================================"
Write-Host "  UPDATE COMPLETE"
Write-Host "============================================"
Write-Host ""
Write-Host "  api:      http://localhost:8080/healthz"
Write-Host "  metrics:  http://localhost:8080/metrics"
if ($backupFile -and (Test-Path $backupFile)) {
    Write-Host "  backup:   $backupFile"
}
Write-Host ""
Write-Host "  Tail logs:"
Write-Host "    docker compose -f $ComposeFile logs -f --tail=100 api"
Write-Host "    docker compose -f $ComposeFile logs -f --tail=100 sync"
Write-Host ""
if ($backupFile -and (Test-Path $backupFile)) {
    Write-Host "  Restore if needed:"
    Write-Host '    $env:PGPASSWORD = (Get-Content deploy\secrets\postgres_password.txt -Raw)'
    Write-Host "    Get-Content $backupFile"
Write-Host "    gunzip -c $backupFile | docker compose -f $ComposeFile exec -T postgres psql -U sso -d sso"
    Write-Host ""
}
