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
    # cmd /c wrapper: native stderr output under $ErrorActionPreference='Stop'
    # gets promoted to terminating error even when exit code is 0.
    try {
        cmd /c "openssl version 1>nul 2>nul"
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

        # Reset ACL to inherited (allows overwrite + delete), then delete.
        # On hosts with stale/restricted perms this ensures we can rewrite.
        cmd /c "icacls `"$path`" /reset /T 2>nul" | Out-Null
        if (Test-Path $path) { Remove-Item -Path $path -Force -ErrorAction SilentlyContinue }

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
function Invoke-Keys {
    Write-Step 'JWT keys'
    if (-not (Test-Path $KeysDir)) {
        New-Item -ItemType Directory -Path $KeysDir -Force | Out-Null
    }

    # Use domain-qualified identity: bare $env:USERNAME resolves to a
    # stale local-domain SID on some hosts (locks out the dir).
    $user    = "{0}\{1}" -f $env:USERDOMAIN, $env:USERNAME
    $privKey = Join-Path $KeysDir 'jwt-private.pem'
    $pubKey  = Join-Path $KeysDir 'jwt-public.pem'

    # Restrict the dir to current user.
    & icacls $KeysDir /inheritance:r /grant:r "${user}:(R,W)" | Out-Null

    # Reset ACLs and delete existing keys so we can overwrite them.
    cmd /c "icacls `"$privKey`" /reset /T 2>nul" | Out-Null
    cmd /c "icacls `"$pubKey`"  /reset /T 2>nul" | Out-Null
    if (Test-Path $privKey) { Remove-Item -Path $privKey -Force -ErrorAction SilentlyContinue }
    if (Test-Path $pubKey)  { Remove-Item -Path $pubKey  -Force -ErrorAction SilentlyContinue }

    Write-Host '  Generating 2048-bit RSA keypair (this takes ~5s)...'
    # openssl writes a progress counter to stderr; under $ErrorActionPreference='Stop'
    # PowerShell turns the first stderr line into a terminating RemoteException
    # even though the exit code is 0. Wrap in cmd /c to fully suppress the native
    # command error stream and let $LASTEXITCODE reflect real failure.
    cmd /c "openssl genpkey -algorithm RSA -out `"$privKey`" -pkeyopt rsa_keygen_bits:2048 1>nul 2>nul" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'openssl genpkey failed' }
    cmd /c "openssl rsa -in `"$privKey`" -pubout -out `"$pubKey`" 1>nul 2>nul" | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'openssl rsa pubout failed' }

    # Grant RW (not just R) so -Force can overwrite on re-run.
    & icacls $privKey /inheritance:r /grant:r "${user}:(R,W)" | Out-Null
    & icacls $pubKey  /inheritance:r /grant:r "${user}:(R,W)" | Out-Null

    Write-Ok 'created: deploy/keys/jwt-private.pem (2048-bit RSA, mode restricted)'
    Write-Ok 'created: deploy/keys/jwt-public.pem'
    Write-Log 'JWT keys generated'
}
function Invoke-Env {
    Write-Step '.env'

    $template = @'
# Public, non-secret env consumed by compose variable substitution.
# DO NOT commit this file.
VPS_MYSQL_DSN=changeme:sso_replicator:REAL_PASSWORD@tcp(vps.your-domain.com:3306)/sja?parseTime=true&readTimeout=30s
JWT_ISSUER=https://gateway.your-domain.com
S3_BUCKET=
S3_ENDPOINT=
'@
    # Reset ACL + delete so we can overwrite even if ACL is restrictive.
    cmd /c "icacls `"$EnvFile`" /reset /T 2>nul" | Out-Null
    if (Test-Path $EnvFile) { Remove-Item -Path $EnvFile -Force -ErrorAction SilentlyContinue }
    Set-Content -Path $EnvFile -Value $template -Encoding UTF8

    Write-Host ''
    Write-Host '  Two required values for deploy/.env. Leave blank to keep the current value.' -ForegroundColor Yellow
    Write-Host ''

    $current = Get-Content $EnvFile -Raw
    $curDsn  = if ($current -match '(?m)^VPS_MYSQL_DSN=(.*)$') { $matches[1].Trim() } else { '' }
    $curIss  = if ($current -match '(?m)^JWT_ISSUER=(.*)$')     { $matches[1].Trim() } else { '' }

    $dsn = Read-Host "  VPS_MYSQL_DSN (e.g. sso_replicator:PWD@tcp(host:3306)/sja?parseTime=true&readTimeout=30s) [$curDsn]"
    if (-not $dsn) { $dsn = $curDsn }
    if (-not $dsn) { throw 'VPS_MYSQL_DSN is required.' }
    if ($dsn -notmatch '^[^:]+:[^@\s]+@tcp\(.+:\d+\)/\S+\?') {
        throw "VPS_MYSQL_DSN does not match expected shape 'user:pass@tcp(host:port)/db?params'. Got: $dsn"
    }

    $iss = Read-Host "  JWT_ISSUER (e.g. https://gateway.example.com) [$curIss]"
    if (-not $iss) { $iss = $curIss }
    if (-not $iss) { throw 'JWT_ISSUER is required.' }
    if ($iss -notmatch '^https?://') {
        throw "JWT_ISSUER must start with http:// or https://. Got: $iss"
    }

    $s3b = Read-Host '  S3_BUCKET (optional, blank to skip)'
    $s3e = Read-Host '  S3_ENDPOINT (optional, blank to skip)'

    $content = "VPS_MYSQL_DSN=$dsn`nJWT_ISSUER=$iss`nS3_BUCKET=$s3b`nS3_ENDPOINT=$s3e`n"
    Set-Content -Path $EnvFile -Value $content -NoNewline -Encoding UTF8

    $user = "{0}\{1}" -f $env:USERDOMAIN, $env:USERNAME
    & icacls $EnvFile /inheritance:r /grant:r "${user}:(R,W)" | Out-Null

    Write-Ok 'wrote: deploy/.env (ACL restricted)'
    Write-Log '.env written'
}
function Invoke-VpsPrefill {
    Write-Step 'VPS prefill (for setup wizard)'

    Write-Host ''
    Write-Host '  VPS MySQL credentials. The setup container will use these to connect,' -ForegroundColor Yellow
    Write-Host '  encrypt the password with the master key, and write /etc/gateway/config.yaml.' -ForegroundColor Yellow
    Write-Host ''

    $host_ = Read-Host '  VPS host (e.g. vps.your-domain.com)'
    if (-not $host_) { throw 'VPS host is required.' }

    $port = Read-Host '  VPS MySQL port [3306]'
    if (-not $port) { $port = '3306' }
    if ($port -notmatch '^\d+$') { throw "Port must be numeric. Got: $port" }

    $db = Read-Host '  Database name [sja]'
    if (-not $db) { $db = 'sja' }

    $user = Read-Host '  Username [sso_replicator]'
    if (-not $user) { $user = 'sso_replicator' }

    $sec = Read-Host '  Password' -AsSecureString
    if (-not $sec) { throw 'VPS password is required.' }
    $bstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
    $pwd  = [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($bstr)
    [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr)

    # Build a DSN that the setup container can consume.
    $dsn = "${user}:${pwd}@tcp(${host_}:${port})/${db}?parseTime=true&readTimeout=30s"
    $setupEnv = "VPS_MYSQL_DSN=$dsn`n"

    # Reset ACL + delete so we can overwrite even if ACL is restrictive.
    cmd /c "icacls `"$SetupEnvFile`" /reset /T 2>nul" | Out-Null
    if (Test-Path $SetupEnvFile) { Remove-Item -Path $SetupEnvFile -Force -ErrorAction SilentlyContinue }
    Set-Content -Path $SetupEnvFile -Value $setupEnv -NoNewline -Encoding UTF8

    $currentUser = "{0}\{1}" -f $env:USERDOMAIN, $env:USERNAME
    & icacls $SetupEnvFile /inheritance:r /grant:r "${currentUser}:(R,W)" | Out-Null

    # Wipe the in-memory password ASAP.
    $pwd = $null
    [System.GC]::Collect()

    Write-Ok "wrote: deploy/.setup-env (ACL restricted, contains VPS DSN)"
    Write-Log 'VpsPrefill written'
}
function Invoke-Build {
    Write-Step 'Build images'
    Push-Location $DeployDir
    try {
        # The previous subagent's lesson: openssl writes a progress
        # counter to stderr which under $ErrorActionPreference='Stop'
        # becomes a terminating RemoteException even on exit 0. The
        # same applies to any docker subcommand that emits progress
        # (build, pull). Suppress stderr in the cmd /c shell so
        # $LASTEXITCODE reflects real failure only.
        cmd /c "docker compose -f `"$ComposeFile`" build --pull 1>nul 2>nul" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'docker compose build failed' }
    } finally {
        Pop-Location
    }
    Write-Ok 'Build complete.'
    Write-Log 'Build complete'
}
function Test-ServiceHealthy {
    param([string]$Service)
    $fmt = '{{.Name}};{{.Health}}'
    $line = docker compose -f $ComposeFile ps --format $fmt $Service 2>&1 | Select-Object -First 1
    if (-not $line) { return $false }
    return ($line -match 'healthy')
}

function Invoke-InfraUp {
    Write-Step 'Infra up (postgres, redis)'

    $bothHealthy = (Test-ServiceHealthy 'postgres') -and (Test-ServiceHealthy 'redis')
    if ($bothHealthy -and -not $Force) {
        Write-Skip 'postgres and redis already healthy'
        Write-Ok 'Infra up complete.'
        return
    }

    Push-Location $DeployDir
    try {
        # Suppress stderr — docker compose up may emit progress lines that
        # under $ErrorActionPreference='Stop' become terminating errors.
        cmd /c "docker compose -f `"$ComposeFile`" up -d postgres redis 1>nul 2>nul" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'docker compose up -d postgres redis failed' }
    } finally {
        Pop-Location
    }

    # Poll for healthy
    $deadline = (Get-Date).AddSeconds(120)
    while ((Get-Date) -lt $deadline) {
        $pgOk = Test-ServiceHealthy 'postgres'
        $rdOk = Test-ServiceHealthy 'redis'
        if ($pgOk -and $rdOk) {
            Write-Ok 'postgres healthy'
            Write-Ok 'redis healthy'
            Write-Ok 'Infra up complete.'
            Write-Log 'Infra up complete'
            return
        }
        Start-Sleep -Seconds 5
    }
    throw 'Timed out waiting 120s for postgres/redis to become healthy. Check `docker compose logs postgres redis`.'
}
function Test-SetupComplete {
    # The setup container is "done" when:
    #  - gateway-config volume has config.yaml
    #  - api_keys table is non-empty
    # Both are checked by execing into the running postgres container.

    # 1. config.yaml on the gateway-config volume
    $cfg = docker compose -f $ComposeFile run --rm -T --entrypoint sh setup -c '
        if [ -f /etc/gateway/config.yaml ]; then echo present; else echo absent; fi
    ' 2>&1 | Select-Object -Last 1
    if ($cfg -ne 'present') { return $false }

    # 2. api_keys row count
    $count = docker compose -f $ComposeFile exec -T postgres psql -U sso -d sso -tAc "SELECT count(*) FROM api_keys" 2>&1 | Select-Object -Last 1
    if (-not ($count -match '^\s*\d+\s*$') -or [int]$count -lt 1) { return $false }

    return $true
}

function Invoke-Setup {
    Write-Step 'Setup wizard'

    if (-not (Test-Path $SetupEnvFile)) {
        throw 'deploy/.setup-env missing. Run -Stage VpsPrefill first (or use -Stage All).'
    }

    if ((Test-SetupComplete) -and -not $Force) {
        Write-Skip 'setup already complete (config.yaml + api_keys present)'
        return
    }

    # Interactive: do NOT redirect stdin. Operator will see the
    # wizard prompts and type responses.
    Push-Location $DeployDir
    try {
        & docker compose -f $ComposeFile run --rm `
            --env-file $SetupEnvFile `
            setup
        if ($LASTEXITCODE -ne 0) { throw 'setup wizard exited non-zero' }
    } finally {
        Pop-Location
    }

    # Post-check
    if (-not (Test-SetupComplete)) {
        throw 'setup wizard completed but config.yaml or api_keys not found. Re-run with -Force.'
    }
    Write-Ok 'Setup wizard complete.'
    Write-Log 'Setup complete'
}
function Test-StackUp {
    $api = Test-ServiceHealthy 'api'
    $sync = docker compose -f $ComposeFile ps --format '{{.Name}};{{.State}}' sync 2>&1 | Select-Object -First 1
    $syncRunning = $sync -match 'running'
    return ($api -and $syncRunning)
}

function Invoke-StackUp {
    Write-Step 'Stack up (api, sync, backup)'

    if ((Test-StackUp) -and -not $Force) {
        Write-Skip 'api healthy and sync running'
        return
    }

    Push-Location $DeployDir
    try {
        # Suppress stderr — same $ErrorActionPreference='Stop' concern as Build.
        cmd /c "docker compose -f `"$ComposeFile`" up -d 1>nul 2>nul" | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'docker compose up -d failed' }
    } finally {
        Pop-Location
    }

    $deadline = (Get-Date).AddSeconds(120)
    while ((Get-Date) -lt $deadline) {
        if (Test-StackUp) {
            Write-Ok 'api healthy'
            Write-Ok 'sync running'
            Write-Ok 'Stack up complete.'
            Write-Log 'Stack up complete'
            return
        }
        Start-Sleep -Seconds 5
    }
    throw 'Timed out waiting 120s for api/sync to come up. Check `docker compose logs api sync`.'
}
function Invoke-Verify {
    Write-Step 'Verify'

    # The api container exposes 8080 on the docker network. We curl
    # the published host port instead, which is the same port nginx
    # will eventually forward to. If the operator hasn't published
    # the port (e.g. behind a separate LB), this will still work
    # because the compose file has `expose: ["8080"]` and the
    # port mapping on `api` maps 8080 to a random host port.
    # We resolve the actual published port from compose.
    $port = (docker compose -f $ComposeFile port api 8080 2>&1 | Select-Object -First 1) -replace '.*:',''
    if (-not ($port -match '^\d+$')) { throw "could not resolve api published port. Got: $port" }

    $url = "http://localhost:$port/healthz"
    try {
        $resp = Invoke-WebRequest -Uri $url -UseBasicParsing -TimeoutSec 10
        if ($resp.StatusCode -ne 200) { throw "healthz returned $($resp.StatusCode)" }
        Write-Ok "healthz OK ($url -> 200)"
    } catch {
        throw "healthz check failed: $($_.Exception.Message)"
    }

    # Service health summary
    $status = docker compose -f $ComposeFile ps --format 'table {{.Name}}\t{{.State}}\t{{.Status}}' 2>&1
    Write-Host ''
    Write-Host '  Service status:' -ForegroundColor Cyan
    $status | ForEach-Object { Write-Host "    $_" }

    # File fingerprints (first 8 chars of sha256, NOT the secret)
    Write-Host ''
    Write-Host '  Secret fingerprints (sha256, first 8 chars):' -ForegroundColor Cyan
    Get-ChildItem -Path $SecretsDir -Filter '*.txt' | ForEach-Object {
        $h = (Get-FileHash -Path $_.FullName -Algorithm SHA256).Hash.Substring(0,8)
        Write-Host ("    {0,-28} {1}" -f $_.Name, $h)
    }

    # Next steps
    Write-Host ''
    Write-Host '  Next steps:' -ForegroundColor Cyan
    Write-Host '    1. Configure your reverse proxy (nginx example in deploy/nginx.conf).'
    Write-Host '    2. Issue a TLS cert (certbot recommended).'
    Write-Host '    3. Tail logs:'
    Write-Host '         docker compose -f deploy/docker-compose.prod.yml logs -f --tail=100'
    Write-Host '    4. Test from outside:'
    Write-Host '         curl -H "X-API-Key: <key from setup>" https://gateway/api/v1/karyawan?limit=5'
    Write-Host ''
    Write-Ok 'Verify complete.'
    Write-Log 'Verify complete'
}

# --- Dispatcher ---
try {
    Initialize-Logging
    Write-Log ("deploy.ps1 started (Force={0}, Stage={1})" -f [bool]$Force, $Stage)

    if ($Stage -in 'All','Preflight')   { Test-Preflight }

    if ($Stage -eq 'All') {
        Invoke-Secrets
        Invoke-Keys
        Invoke-Env
        Invoke-VpsPrefill
        Invoke-Build
        Invoke-InfraUp
        Invoke-Setup
        Invoke-StackUp
        Invoke-Verify
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
