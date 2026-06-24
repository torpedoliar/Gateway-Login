<#
.SYNOPSIS
    Remote production deploy for SSO Gateway via SSH.

.DESCRIPTION
    Connects to a Linux VPS, uploads the project, and runs the full
    deploy pipeline (bootstrap → build → infra up → setup → stack up → verify).

    Requirements on the remote host:
    - Docker 24+ and docker compose v2 installed
    - SSH key-based authentication configured

.PARAMETER Host
    Remote host (e.g. root@192.168.1.100 or ubuntu@gateway.example.com).

.PARAMETER Port
    SSH port. Default: 22.

.PARAMETER RemotePath
    Target directory on the remote host. Default: /opt/sso-gateway.

.PARAMETER RemoteUser
    OS user that owns the deployment (docker compose runs as this user).
    Default: ubuntu.

.PARAMETER Branch
    Git branch to deploy. Default: main.

.PARAMETER SkipSetup
    Skip the interactive setup wizard stage.

.PARAMETER Force
    Re-run every stage regardless of skip conditions.

.EXAMPLE
    .\deploy-ssh.ps1 -Host "root@192.168.1.100" -SkipSetup

.EXAMPLE
    .\deploy-ssh.ps1 -Host "ubuntu@gateway.example.com" -RemotePath "/opt/sso-gateway" -Force
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory, HelpMessage = 'SSH destination, e.g. root@192.168.1.100')]
    [string]$Host,

    [ValidateRange(1, 65535)]
    [int]$Port = 22,

    [string]$RemotePath = '/opt/sso-gateway',

    [string]$RemoteUser = 'ubuntu',

    [string]$Branch = 'main',

    [switch]$SkipSetup,

    [switch]$Force
)

$ErrorActionPreference = 'Stop'
$ProgressPreference    = 'SilentlyContinue'

# Resolve repo root (script lives in <root>/deploy/deploy-ssh.ps1)
$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot  = Resolve-Path (Join-Path $ScriptDir '..')
Set-Location $RepoRoot

# ── SSH helper ───────────────────────────────────────────────────────────────
function Invoke-Ssh {
    param(
        [Parameter(ValueFromRemainingArguments)]
        [string]$Args
    )
    $full = "ssh -p $Port -o StrictHostKeyChecking=accept-new $Host $Args"
    Write-Verbose "[ssh] $full"
    Invoke-Expression $full
}

function Invoke-SshScript {
    param([string]$Script)
    # Pass script via stdin so it's not stored in ~/.bash_history
    $escaped = $Script -replace "'", "'\"'\"'"
    Invoke-Ssh "bash -lc 'cat << '"'"'SHELL_EOF'"'"' | bash -l /dev/stdin`n$Script`nSHELL_EOF'"
}

# ── Rsync helper ─────────────────────────────────────────────────────────────
function Expand-RsyncExcludes {
    $base = $RepoRoot
    @(
        # Git
        '.git',
        '.gitignore',
        '.gitattributes',
        # Secrets (generated remotely, never uploaded)
        'deploy/secrets',
        'deploy/keys',
        'deploy/.env',
        'deploy/.setup-env',
        'deploy/logs',
        'deploy/backup',
        # Build artifacts
        'bin',
        # Local docs / dev notes
        'docs',
        'tests',
        # Hidden env files at root
        '.env',
        '.env.*',
        # OS noise
        '*.lnk',
        'Thumbs.db'
    ) | ForEach-Object { "$base/$_" }
}

function Write-Step  { param($m) Write-Host ("==> " + $m) -ForegroundColor Cyan }
function Write-Ok     { param($m) Write-Host ("  [ok] "   + $m) -ForegroundColor Green }
function Write-Skip   { param($m) Write-Host ("  [skip] " + $m) -ForegroundColor DarkGray }
function Write-Warn   { param($m) Write-Host ("  [warn] " + $m) -ForegroundColor Yellow }
function Write-Err    { param($m) Write-Host ("  [err] "  + $m) -ForegroundColor Red }

# ── Logging ───────────────────────────────────────────────────────────────────
$LogsDir = Join-Path $ScriptDir 'logs'
if (-not (Test-Path $LogsDir)) {
    New-Item -ItemType Directory -Path $LogsDir -Force | Out-Null
}
$ts       = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$LogFile  = Join-Path $LogsDir "deploy-ssh-$ts.log"
$null     = '' | Set-Content -Path $LogFile -Encoding UTF8
Copy-Item $LogFile (Join-Path $LogsDir 'deploy-ssh.latest.log') -Force

function Write-Log {
    param($Message)
    $line = "[{0}] {1}" -f (Get-Date -Format 'o'), $Message
    Add-Content -Path $LogFile -Value $line
}

# ── Stage 0: Preflight ─────────────────────────────────────────────────────────
function Test-Preflight {
    Write-Step "Preflight (remote: $Host)"

    $fail = $false

    # 1. SSH connectivity
    try {
        $ver = Invoke-Ssh "echo ok 2>&1"
        if ($ver -notmatch 'ok') { throw 'SSH handshake failed' }
    } catch {
        Write-Err "Cannot connect to $Host`:$_"
        $fail = $true
    }

    # 2. Docker on remote
    try {
        $dv = Invoke-Ssh "docker version --format '{{.Server.Version}}' 2>&1"
        if ($LASTEXITCODE -ne 0 -or -not $dv) { throw 'docker version failed' }
        Write-Verbose "Remote Docker: $dv"
    } catch {
        Write-Err 'Docker not reachable on remote host. Is Docker installed and the user in the docker group?'
        $fail = $true
    }

    # 3. docker compose on remote
    try {
        $cv = Invoke-Ssh "docker compose version --format '{{.Version}}' 2>&1"
        if ($LASTEXITCODE -ne 0 -or -not $cv) { throw 'compose version failed' }
        Write-Verbose "Remote docker compose: $cv"
    } catch {
        Write-Err 'docker compose not available on remote host.'
        $fail = $true
    }

    # 4. Remote path writable
    try {
        $testDir = "$RemotePath/__deploy_test__"
        Invoke-Ssh "mkdir -p '$testDir' && rm -rf '$testDir'"
        Write-Verbose "Remote path writable: $RemotePath"
    } catch {
        Write-Err "Cannot write to $RemotePath. Check permissions."
        $fail = $true
    }

    if ($fail) { throw 'Preflight failed.' }
    Write-Ok 'Preflight passed.'
    Write-Log 'Preflight OK'
}

# ── Stage 1: Sync files ────────────────────────────────────────────────────────
function Invoke-Sync {
    Write-Step 'Sync files to remote'

    $excludes = @()
    foreach ($e in Expand-RsyncExcludes) {
        $excludes += '--exclude', $e
    }

    $sshCmd = "ssh -p $Port -o StrictHostKeyChecking=accept-new"
    $rsyncArgs = @(
        '-avz'
        '--delete'
        '-e', $sshCmd
        $excludes
        "$RepoRoot/"
        "$Host`:$RemotePath/"
    )

    Write-Verbose "rsync $(($rsyncArgs -join ' '))"
    & rsync @rsyncArgs 2>&1 | ForEach-Object { Write-Verbose $_ }

    if ($LASTEXITCODE -ne 0) { throw 'rsync failed' }
    Write-Ok "Synced to ${Host}:${RemotePath}"
    Write-Log 'Sync complete'
}

# ── Stage 2: Bootstrap ─────────────────────────────────────────────────────────
function Invoke-Bootstrap {
    Write-Step 'Bootstrap (generate secrets + keys)'

    $script = @"
set -e
cd '$RemotePath'

# Create required directories
mkdir -p deploy/secrets deploy/keys deploy/logs deploy/backup

# Generate random secrets (base64 to avoid special char issues)
if [ ! -f deploy/secrets/postgres_password.txt ]; then
    openssl rand -base64 24 > deploy/secrets/postgres_password.txt
    chmod 600 deploy/secrets/postgres_password.txt
    echo '  [created] deploy/secrets/postgres_password.txt'
fi

if [ ! -f deploy/secrets/redis_password.txt ]; then
    openssl rand -base64 24 > deploy/secrets/redis_password.txt
    chmod 600 deploy/secrets/redis_password.txt
    echo '  [created] deploy/secrets/redis_password.txt'
fi

if [ ! -f deploy/secrets/gateway_master_key.txt ]; then
    openssl rand -base64 32 > deploy/secrets/gateway_master_key.txt
    chmod 600 deploy/secrets/gateway_master_key.txt
    echo '  [created] deploy/secrets/gateway_master_key.txt'
fi

# Generate JWT keypair
if [ ! -f deploy/keys/jwt-private.pem ]; then
    openssl genpkey -algorithm RSA -out deploy/keys/jwt-private.pem -pkeyopt rsa_keygen_bits:2048 2>/dev/null
    openssl rsa -in deploy/keys/jwt-private.pem -pubout -out deploy/keys/jwt-public.pem 2>/dev/null
    chmod 600 deploy/keys/jwt-private.pem deploy/keys/jwt-public.pem
    echo '  [created] deploy/keys/jwt-private.pem (2048-bit RSA)'
    echo '  [created] deploy/keys/jwt-public.pem'
fi

# Ownership
chown -R $RemoteUser:$RemoteUser deploy/secrets deploy/keys deploy/logs deploy/backup
echo 'Bootstrap complete.'
"@

    Invoke-SshScript $script
    Write-Ok 'Bootstrap complete.'
    Write-Log 'Bootstrap complete'
}

# ── Stage 3: Configure ─────────────────────────────────────────────────────────
function Invoke-Configure {
    Write-Step 'Configure environment'

    # Collect inputs
    $vpsHost = Read-Host '  VPS MySQL host'
    if (-not $vpsHost) { throw 'VPS host is required.' }

    $vpsPort = Read-Host '  VPS MySQL port [3306]'
    if (-not $vpsPort) { $vpsPort = '3306' }

    $vpsDb = Read-Host '  Database name [sja]'
    if (-not $vpsDb) { $vpsDb = 'sja' }

    $vpsUser = Read-Host '  Username [sso_replicator]'
    if (-not $vpsUser) { $vpsUser = 'sso_replicator' }

    $vpsSec = Read-Host '  Password' -AsSecureString
    if (-not $vpsSec) { throw 'VPS password is required.' }
    $vpsBstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($vpsSec)
    $vpsPwd  = [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($vpsBstr)
    [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($vpsBstr)

    $iss = Read-Host '  JWT_ISSUER (e.g. https://gateway.example.com)'
    if (-not $iss) { throw 'JWT_ISSUER is required.' }
    if ($iss -notmatch '^https?://') {
        throw "JWT_ISSUER must start with http:// or https://. Got: $iss"
    }

    $s3b = Read-Host '  S3_BUCKET (optional, blank to skip)'
    $s3e = Read-Host '  S3_ENDPOINT (optional, blank to skip)'

    # Escape for shell
    $escDsn  = "${vpsUser}:${vpsPwd}@tcp(${vpsHost}:${vpsPort})/${vpsDb}?parseTime=true&readTimeout=30s"
    $escIss  = $iss -replace "'", "'\"'\"'"
    $escS3b  = $s3b -replace "'", "'\"'\"'"
    $escS3e  = $s3e -replace "'", "'\"'\"'"

    $script = @"
cat > '$RemotePath/deploy/.env' << 'ENVEOF'
VPS_MYSQL_DSN=$escDsn
JWT_ISSUER=$escIss
S3_BUCKET=$escS3b
S3_ENDPOINT=$escS3e
ENVEOF
chown $RemoteUser:$RemoteUser '$RemotePath/deploy/.env'
chmod 600 '$RemotePath/deploy/.env'
echo '  [ok] deploy/.env written'
"@

    Invoke-SshScript $script
    Write-Ok 'deploy/.env written'
    Write-Log '.env written'

    # Wipe password from memory
    $vpsPwd = $null; [System.GC]::Collect()
}

# ── Stage 4: Build ─────────────────────────────────────────────────────────────
function Invoke-Build {
    Write-Step 'Build images on remote'

    $script = @"
cd '$RemotePath'
sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml build --pull
echo "Build exit code: $?"
"@

    $output = Invoke-Ssh "bash -lc 'cat << '"'"'SHELL_EOF'"'"' | bash -l /dev/stdin`n$script`nSHELL_EOF'"
    Write-Verbose $output

    if ($LASTEXITCODE -ne 0) { throw "Build failed on remote. Check logs." }
    Write-Ok 'Build complete.'
    Write-Log 'Build complete'
}

# ── Stage 5: Infra Up ─────────────────────────────────────────────────────────
function Invoke-InfraUp {
    Write-Step 'Start infra (postgres, redis)'

    $script = @"
cd '$RemotePath'
sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml up -d postgres redis

# Wait for healthy (up to 120s)
for i in $(seq 1 24); do
    pg=\$(sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml ps --format '{{.Name}};{{.Health}}' postgres 2>/dev/null)
    rd=\$(sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml ps --format '{{.Name}};{{.Health}}' redis 2>/dev/null)
    if echo "$pg" | grep -q 'healthy' && echo "$rd" | grep -q 'healthy'; then
        echo '  [ok] postgres healthy'
        echo '  [ok] redis healthy'
        exit 0
    fi
    sleep 5
done
echo '  [err] Timed out waiting for postgres/redis to become healthy.'
echo '  Logs:'
sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml logs --tail=50 postgres redis
exit 1
"@

    $output = Invoke-Ssh "bash -lc 'cat << '"'"'SHELL_EOF'"'"' | bash -l /dev/stdin`n$script`nSHELL_EOF'"
    Write-Verbose $output

    if ($LASTEXITCODE -ne 0) { throw 'InfraUp failed. Check remote logs.' }
    Write-Ok 'Infra up complete.'
    Write-Log 'InfraUp complete'
}

# ── Stage 6: Setup ─────────────────────────────────────────────────────────────
function Invoke-RemoteSetup {
    Write-Step 'Setup wizard (interactive — input required)'

    if ($SkipSetup) {
        Write-Skip 'Setup wizard skipped (-SkipSetup)'
        return
    }

    Write-Host ''
    Write-Host '  The setup wizard will run interactively on the remote host.' -ForegroundColor Yellow
    Write-Host '  You will be prompted for: VPS host, port, database, username, password.' -ForegroundColor Yellow
    Write-Host ''

    $confirm = Read-Host '  Press Enter to continue, or Ctrl+C to abort'
    if (-not $confirm) {
        Write-Host '  Launching setup...'
    }

    # Copy .setup-env with VPS DSN
    $vpsHost = Read-Host '  VPS MySQL host'
    if (-not $vpsHost) { Write-Skip 'Setup wizard skipped (no VPS host)'; return }
    $vpsPort = Read-Host '  VPS MySQL port [3306]'
    if (-not $vpsPort) { $vpsPort = '3306' }
    $vpsDb = Read-Host '  Database name [sja]'
    if (-not $vpsDb) { $vpsDb = 'sja' }
    $vpsUser = Read-Host '  Username [sso_replicator]'
    if (-not $vpsUser) { $vpsUser = 'sso_replicator' }
    $vpsSec = Read-Host '  Password' -AsSecureString
    if (-not $vpsSec) { throw 'VPS password required for setup.' }
    $vpsBstr = [System.Runtime.InteropServices.Marshal]::SecureStringToBSTR($vpsSec)
    $vpsPwd  = [System.Runtime.InteropServices.Marshal]::PtrToStringAuto($vpsBstr)
    [System.Runtime.InteropServices.Marshal]::ZeroFreeBSTR($vpsBstr)

    $dsn = "${vpsUser}:${vpsPwd}@tcp(${vpsHost}:${vpsPort})/${vpsDb}?parseTime=true&readTimeout=30s"
    $vpsPwd = $null; [System.GC]::Collect()

    $escDsn = $dsn -replace "'", "'\"'\"'"
    $setupScript = @"
cat > '$RemotePath/deploy/.setup-env' << 'ENVEOF'
VPS_MYSQL_DSN=$escDsn
ENVEOF
chown $RemoteUser:$RemoteUser '$RemotePath/deploy/.setup-env'
chmod 600 '$RemotePath/deploy/.setup-env'
"@

    Invoke-SshScript $setupScript

    # Run setup container interactively via pseudo-TTY
    Write-Host ''
    Write-Host '  Starting setup wizard on remote host...' -ForegroundColor Cyan
    Write-Host '  Follow the prompts on the SSH session.' -ForegroundColor Yellow
    Write-Host ''

    $cmd = "cd '$RemotePath' && sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml run --rm --env-file deploy/.setup-env setup"

    # Use ssh with -tt for forced pseudo-TTY
    $fullSsh = "ssh -tt -p $Port -o StrictHostKeyChecking=accept-new $Host"
    $process = Start-Process -FilePath "powershell" -ArgumentList "-NoProfile", "-Command", "$fullSsh `"$cmd`"" -Wait -NoNewWindow -PassThru

    if ($process.ExitCode -ne 0 -and $process.ExitCode -ne 255) {
        throw "Setup wizard exited with code $($process.ExitCode)"
    }

    Write-Ok 'Setup wizard complete.'
    Write-Log 'Setup complete'
}

# ── Stage 7: Stack Up ─────────────────────────────────────────────────────────
function Invoke-StackUp {
    Write-Step 'Start stack (api, sync, backup)'

    $script = @"
cd '$RemotePath'
sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml up -d

# Wait for api healthy (up to 120s)
for i in $(seq 1 24); do
    api=\$(sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml ps --format '{{.Name}};{{.Health}}' api 2>/dev/null)
    if echo "$api" | grep -q 'healthy'; then
        echo '  [ok] api healthy'
        exit 0
    fi
    sleep 5
done
echo '  [warn] Timed out waiting for api to become healthy.'
sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml logs --tail=30 api
exit 0
"@

    $output = Invoke-Ssh "bash -lc 'cat << '"'"'SHELL_EOF'"'"' | bash -l /dev/stdin`n$script`nSHELL_EOF'"
    Write-Verbose $output

    Write-Ok 'Stack up complete.'
    Write-Log 'StackUp complete'
}

# ── Stage 8: Verify ────────────────────────────────────────────────────────────
function Invoke-Verify {
    Write-Step 'Verify'

    $script = @"
cd '$RemotePath'
echo ''
echo '=== Service status ==='
sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml ps

echo ''
echo '=== Health check ==='
port=\$(sudo -u $RemoteUser docker compose -f deploy/docker-compose.prod.yml port api 8080 2>/dev/null | sed 's/.*://')
if [ -z `"$port`" ]; then
    echo '  [err] Could not resolve api port'
    exit 1
fi
echo '  API port: '"'"'"$port"'"'"'

# Try localhost first, then public IP
for try_host in 127.0.0.1 localhost; do
    url=""http://$try_host:$port/healthz""
    echo ""  Checking $url...""
    status=\$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 ""$url"" 2>/dev/null)
    if [ ""$status"" = ""200"" ]; then
        echo '  [ok] healthz OK ('"$url"')'
        exit 0
    else
        echo '  [info] healthz returned '"'"'"$status"'"'"' on '"'"'"$try_host"'"'"'
    fi
done

echo '  [warn] Could not reach healthz. This is normal if nginx is not configured yet.'
exit 0
"@

    $output = Invoke-Ssh "bash -lc 'cat << '"'"'SHELL_EOF'"'"' | bash -l /dev/stdin`n$script`nSHELL_EOF'"
    Write-Verbose $output
    Write-Host $output

    Write-Host ''
    Write-Host '  Next steps:' -ForegroundColor Cyan
    Write-Host "    1. SSH to $Host and configure TLS/reverse proxy."
    Write-Host "    2. Run: ssh -t $Host 'sudo -u $RemoteUser docker compose -f $RemotePath/deploy/docker-compose.prod.yml logs -f'"
    Write-Host ''

    Write-Ok 'Verify complete.'
    Write-Log 'Verify complete'
}

# ── Dispatcher ────────────────────────────────────────────────────────────────
Write-Host ''
Write-Host "  SSO Gateway — Remote Deploy" -ForegroundColor Magenta
Write-Host "  Host    : $Host`:$Port" -ForegroundColor DarkGray
Write-Host "  Path    : $RemotePath" -ForegroundColor DarkGray
Write-Host "  User    : $RemoteUser" -ForegroundColor DarkGray
Write-Host "  Branch  : $Branch" -ForegroundColor DarkGray
Write-Host ''

try {
    Initialize-Logging
    Write-Log ("deploy-ssh.ps1 started (Force=$Force, SkipSetup=$SkipSetup)")
    Write-Log "Host: $Host`:$Port, Path: $RemotePath, User: $RemoteUser"

    # Always run preflight
    Test-Preflight

    # Sync + bootstrap
    Invoke-Sync
    Invoke-Bootstrap

    # Configure (interactive)
    Invoke-Configure

    # Build + deploy
    Invoke-Build
    Invoke-InfraUp
    Invoke-RemoteSetup
    Invoke-StackUp
    Invoke-Verify

    Write-Log 'deploy-ssh.ps1 completed'
    Write-Host ''
    Write-Ok "Deploy complete. Log: $LogFile"
} catch {
    Write-Log ("FATAL: " + $_.Exception.Message)
    Write-Host ''
    Write-Err $_.Exception.Message
    Write-Host "Check log: $LogFile" -ForegroundColor Yellow
    exit 1
}
