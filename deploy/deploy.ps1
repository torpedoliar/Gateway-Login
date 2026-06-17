<#
.SYNOPSIS
    All-in-one production deploy for SSO Gateway on Windows + Docker Desktop.

.DESCRIPTION
    Takes a fresh checkout to a running, healthy production stack in 9 stages.
    Idempotent: re-running on a working host is a no-op (or a fast verify pass).
    Use -Force to override idempotency and re-run every stage.

.PARAMETER Force
    Re-run every stage regardless of skip conditions.

.PARAMETER Stage
    Run a single stage by name. Default: All.

.EXAMPLE
    .\deploy\deploy.ps1

.EXAMPLE
    .\deploy\deploy.ps1 -Force

.EXAMPLE
    .\deploy\deploy.ps1 -Stage Preflight
#>
[CmdletBinding()]
param(
    [switch]$Force,
    [ValidateSet('All','Preflight','Secrets','Keys','Env','VpsPrefill','Build','InfraUp','Setup','StackUp','Verify')]
    [string]$Stage = 'All'
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'  # speed up docker invocations

# Resolve repo root (script lives in <root>/deploy/deploy.ps1)
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot  = Resolve-Path (Join-Path $ScriptDir '..')
Set-Location $RepoRoot

# Paths
$DeployDir    = Join-Path $RepoRoot 'deploy'
$SecretsDir   = Join-Path $DeployDir 'secrets'
$KeysDir      = Join-Path $DeployDir 'keys'
$LogsDir      = Join-Path $DeployDir 'logs'
$EnvFile      = Join-Path $DeployDir '.env'
$SetupEnvFile = Join-Path $DeployDir '.setup-env'
$ComposeFile  = Join-Path $DeployDir 'docker-compose.prod.yml'

# --- Color helpers (auto-disabled on legacy terminals) ---
$Script:UseColor = $Host.UI.SupportsVirtualTerminal -and -not $env:NO_COLOR
function Write-Step  { param($m) Write-Host ("==> " + $m) -ForegroundColor Cyan }
function Write-Ok    { param($m) Write-Host ("  [ok] "   + $m) -ForegroundColor Green }
function Write-Skip  { param($m) Write-Host ("  [skip] " + $m) -ForegroundColor DarkGray }
function Write-Warn  { param($m) Write-Host ("  [warn] " + $m) -ForegroundColor Yellow }
function Write-Err   { param($m) Write-Host ("  [err] "  + $m) -ForegroundColor Red }

# --- Logging (tee to deploy/logs/deploy-<ts>.log) ---
function Initialize-Logging {
    if (-not (Test-Path $LogsDir)) { New-Item -ItemType Directory -Path $LogsDir -Force | Out-Null }
    $ts = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
    $Script:LogFile = Join-Path $LogsDir "deploy-$ts.log"
    '' | Set-Content -Path $Script:LogFile -Encoding UTF8
    Copy-Item -Path $Script:LogFile -Destination (Join-Path $LogsDir 'deploy.latest.log') -Force
}

function Write-Log {
    param($Message)
    $line = "[{0}] {1}" -f (Get-Date -Format 'o'), $Message
    Add-Content -Path $Script:LogFile -Value $line
}

# --- Preflight ---
function Test-Preflight {
    Write-Step 'Preflight'

    $fail = $false

    # Docker daemon reachable
    try {
        $null = docker version 2>&1
        if ($LASTEXITCODE -ne 0) { throw 'docker version failed' }
    } catch {
        Write-Err 'Docker CLI not responding. Is Docker Desktop running?'
        Write-Host '  Start Docker Desktop, wait for the whale icon to settle, retry.' -ForegroundColor Yellow
        $fail = $true
    }

    # docker compose plugin
    try {
        $null = docker compose version 2>&1
        if ($LASTEXITCODE -ne 0) { throw 'docker compose version failed' }
    } catch {
        Write-Err 'docker compose plugin missing. Update Docker Desktop to v4.x+.'
        $fail = $true
    }

    # openssl (Git for Windows, or system OpenSSL)
    try {
        $null = & openssl version 2>&1
        if ($LASTEXITCODE -ne 0) { throw 'openssl version failed' }
    } catch {
        Write-Err 'openssl not on PATH. Install Git for Windows or OpenSSL 1.1+.'
        $fail = $true
    }

    # compose file present
    if (-not (Test-Path $ComposeFile)) {
        Write-Err "compose file not found: $ComposeFile"
        $fail = $true
    }

    # Free disk >= 5GB on Docker data root
    try {
        $dockerRoot = (docker info --format '{{.DockerRootDir}}' 2>&1).Trim()
        if ($dockerRoot -and (Test-Path $dockerRoot)) {
            $drive = (Get-Item $dockerRoot).PSDrive.Name
            $free  = (Get-PSDrive -Name $drive).Free / 1GB
            if ($free -lt 5) {
                Write-Warn ("Free disk on {0}: {1:N1} GB (< 5 GB). Image pulls may fail." -f $drive, $free)
            } else {
                Write-Ok ("Free disk on {0}: {1:N1} GB" -f $drive, $free)
            }
        }
    } catch {
        Write-Warn 'Could not determine Docker data root free space (non-fatal).'
    }

    if ($fail) {
        throw 'Preflight failed. Fix the errors above and re-run.'
    }
    Write-Ok 'Preflight passed.'
    Write-Log 'Preflight OK'
}

# --- Stage stubs (filled in by later tasks) ---
function Invoke-Secrets {
    Write-Step 'Secrets'
    if (-not (Test-Path $SecretsDir)) {
        New-Item -ItemType Directory -Path $SecretsDir -Force | Out-Null
    }

    # Restrict the dir to current user only.
    # /inheritance:r strips inherited ACEs; /grant:r replaces grants.
    # Use domain-qualified identity: bare $env:USERNAME resolves to a
    # stale local-domain SID on some hosts (locks out the dir).
    $user = "{0}\{1}" -f $env:USERDOMAIN, $env:USERNAME
    & icacls $SecretsDir /inheritance:r /grant:r "${user}:(R,W)" | Out-Null

    # Each entry: name -> byte length
    $specs = [ordered]@{
        'postgres_password.txt'    = 24
        'redis_password.txt'       = 24
        'gateway_master_key.txt'   = 32
    }

    foreach ($name in $specs.Keys) {
        $path = Join-Path $SecretsDir $name
        $len  = $specs[$name]

        if ((Test-Path $path) -and ((Get-Item $path).Length -gt 0) -and -not $Force) {
            Write-Skip "exists: deploy/secrets/$name"
            continue
        }

        $bytes = New-Object byte[] $len
        $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
        try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
        $b64 = [Convert]::ToBase64String($bytes)

        # -NoNewline: secret files must not carry a trailing CR/LF
        # (redis-entrypoint.sh and similar consumers will trim, but
        # writing clean avoids the problem at the source).
        # Use AsciiBytes to avoid the UTF-8 BOM that Set-Content -Encoding UTF8 emits;
        # a leading BOM would change the file's hash and confuse consumers.
        [IO.File]::WriteAllBytes($path, [Text.Encoding]::ASCII.GetBytes($b64))
        & icacls $path /inheritance:r /grant:r "${user}:(R,W)" | Out-Null
        Write-Ok "created: deploy/secrets/$name ($len random bytes)"
    }

    Write-Ok 'Secrets complete.'
    Write-Log 'Secrets complete'
}
function Invoke-Keys       { Write-Step 'Keys (TODO)';        Write-Ok 'noop' }
function Invoke-Env        { Write-Step 'Env (TODO)';         Write-Ok 'noop' }
function Invoke-VpsPrefill { Write-Step 'VpsPrefill (TODO)';  Write-Ok 'noop' }
function Invoke-Build      { Write-Step 'Build (TODO)';       Write-Ok 'noop' }
function Invoke-InfraUp    { Write-Step 'InfraUp (TODO)';     Write-Ok 'noop' }
function Invoke-Setup      { Write-Step 'Setup (TODO)';       Write-Ok 'noop' }
function Invoke-StackUp    { Write-Step 'StackUp (TODO)';     Write-Ok 'noop' }
function Invoke-Verify     { Write-Step 'Verify (TODO)';      Write-Ok 'noop' }

# --- Dispatcher ---
try {
    Initialize-Logging
    Write-Log ("deploy.ps1 started (Force={0}, Stage={1})" -f [bool]$Force, $Stage)

    if ($Stage -in 'All','Preflight')   { Test-Preflight }

    if ($Stage -eq 'All') {
        if ($Force -or $true) { Invoke-Secrets }     # TODO: replace with skip check
        if ($Force -or $true) { Invoke-Keys }
        if ($Force -or $true) { Invoke-Env }
        if ($Force -or $true) { Invoke-VpsPrefill }
        if ($Force -or $true) { Invoke-Build }
        if ($Force -or $true) { Invoke-InfraUp }
        if ($Force -or $true) { Invoke-Setup }
        if ($Force -or $true) { Invoke-StackUp }
        if ($Force -or $true) { Invoke-Verify }
    } else {
        switch ($Stage) {
            'Secrets'    { Invoke-Secrets }
            'Keys'       { Invoke-Keys }
            'Env'        { Invoke-Env }
            'VpsPrefill' { Invoke-VpsPrefill }
            'Build'      { Invoke-Build }
            'InfraUp'    { Invoke-InfraUp }
            'Setup'      { Invoke-Setup }
            'StackUp'    { Invoke-StackUp }
            'Verify'     { Invoke-Verify }
        }
    }

    Write-Log 'deploy.ps1 completed'
    Write-Host ''
    Write-Ok "Deploy complete. Log: $Script:LogFile"
} catch {
    Write-Log ("FATAL: " + $_.Exception.Message)
    Write-Host ''
    Write-Err $_.Exception.Message
    exit 1
}
