[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet("up", "status", "logs", "smoke", "down", "reset", "help", "")]
    [string]$Command = "",

    [Parameter(Position = 1)]
    [string]$Service = "",

    [switch]$Yes
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$RootDir = Split-Path -Parent $PSScriptRoot
Set-Location $RootDir

function Show-Usage {
    @"
Usage: .\scripts\cordova-dev.ps1 <command> [options]

Commands:
  up              Prepare local dev config/certs, start dev stack, enroll & start mock-node
  status          Show status of dev Compose services (including mock-node)
  logs [service]  Follow logs (optionally for a specific service, e.g. server or mock-node)
  smoke           Run end-to-end Compose dev smoke test (UI, auth, mock-node, SSE)
  down            Stop dev services (preserves persistent volumes and .local/ secrets)
  reset [-Yes]    Stop services AND delete persistent volumes + .local/ (requires confirmation)
"@
}

function Invoke-Compose {
    param([string[]]$ComposeArgs)
    $baseArgs = @("--env-file", ".local/dev.env", "-f", "infra/compose.yaml", "-f", "infra/compose.dev.yaml")
    & docker compose @baseArgs @ComposeArgs
    if ($LASTEXITCODE -ne 0) {
        throw "docker compose failed with exit code $LASTEXITCODE"
    }
}

function Require-Docker {
    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw "'docker' CLI not found. Install Docker Desktop first."
    }
    & docker compose version *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Docker Compose v2 ('docker compose') is required."
    }
    & docker info *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Docker daemon is not running or not reachable."
    }
}

function Require-Files {
    $required = @(
        "infra/compose.yaml",
        "infra/compose.dev.yaml",
        "infra/nginx/nginx.conf",
        "infra/redis/redis.conf"
    )
    foreach ($f in $required) {
        if (-not (Test-Path -LiteralPath $f -PathType Leaf)) {
            throw "Required file missing: $f"
        }
    }
}

function New-RandomHex32 {
    $bytes = New-Object byte[] 32
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $rng.GetBytes($bytes)
    }
    finally {
        $rng.Dispose()
    }
    return ([System.BitConverter]::ToString($bytes) -replace "-", "").ToLowerInvariant()
}

function Initialize-DevEnv {
    $localDir = Join-Path $RootDir ".local"
    $certsDir = Join-Path $localDir "certs"
    if (-not (Test-Path -LiteralPath $certsDir)) {
        New-Item -ItemType Directory -Path $certsDir -Force | Out-Null
    }

    $crtPath = Join-Path $certsDir "server.crt"
    $keyPath = Join-Path $certsDir "server.key"
    if (-not (Test-Path -LiteralPath $crtPath) -or -not (Test-Path -LiteralPath $keyPath)) {
        if (Get-Command openssl -ErrorAction SilentlyContinue) {
            $prevPathConv = $env:MSYS_NO_PATHCONV
            $env:MSYS_NO_PATHCONV = "1"
            try {
                & openssl req -x509 -newkey rsa:2048 -nodes -days 365 `
                    -keyout $keyPath -out $crtPath `
                    -subj "/CN=localhost" `
                    -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" *> $null
                if ($LASTEXITCODE -ne 0) { throw "openssl failed to generate dev certs" }
            }
            finally {
                $env:MSYS_NO_PATHCONV = $prevPathConv
            }
        }
        else {
            & docker run --rm -v "${certsDir}:/certs" alpine/openssl `
                req -x509 -newkey rsa:2048 -nodes -days 365 `
                -keyout /certs/server.key -out /certs/server.crt `
                -subj "/CN=localhost" `
                -addext "subjectAltName=DNS:localhost,IP:127.0.0.1" *> $null
            if ($LASTEXITCODE -ne 0) { throw "alpine/openssl container failed to generate dev certs" }
        }
        Write-Host "Generated self-signed dev certs in .local/certs/"
    }
    else {
        Write-Host "Keeping existing dev certs in .local/certs/"
    }

    $envPath = Join-Path $localDir "dev.env"
    if (-not (Test-Path -LiteralPath $envPath)) {
        $lines = @(
            "# Auto-generated local development configuration (never used in production).",
            "MONERO_NETWORK=stagenet",
            "DEV_MODE=1",
            "DEV_INSECURE_COOKIES=0",
            ("TOKENIZER_SECRET=" + (New-RandomHex32)),
            ("API_KEY_PEPPER=" + (New-RandomHex32)),
            ("NODE_SECRET_MASTER_KEY=" + (New-RandomHex32)),
            ""
        )
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($envPath, ($lines -join "`n"), $utf8NoBom)
        Write-Host "Generated local dev secrets in .local/dev.env"
    }
    else {
        Write-Host "Keeping existing .local/dev.env"
    }
}

function Test-Port8443InUse {
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $iar = $client.BeginConnect("127.0.0.1", 8443, $null, $null)
        $connected = $iar.AsyncWaitHandle.WaitOne(500, $false)
        if ($connected -and $client.Connected) {
            $client.EndConnect($iar)
            return $true
        }
        return $false
    }
    catch {
        return $false
    }
    finally {
        $client.Dispose()
    }
}

function Assert-Port8443Available {
    $nginxCid = (& docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml ps -q nginx 2>$null)
    if ($nginxCid) {
        return
    }
    if (Test-Port8443InUse) {
        throw "Port 8443 on 127.0.0.1 is already in use by another process. Free port 8443 before starting Cordova."
    }
}

function Write-ComposeDiagnostics {
    Write-Host "=== CORDOVA DEV FAILURE DIAGNOSTICS ===" -ForegroundColor Red
    & docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node ps -a
    $cids = (& docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node ps -aq 2>$null)
    if ($cids) {
        Write-Host "--- Container states, exit codes, and healthchecks ---"
        foreach ($cid in $cids) {
            if ($cid) {
                & docker inspect --format '{{.Name}} status={{.State.Status}} exit_code={{.State.ExitCode}} health={{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' $cid
            }
        }
        Write-Host "--- Container logs ---"
        & docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node logs --no-color --tail=200
    }
    Write-Host "=======================================" -ForegroundColor Red
}

function Write-Utf8JsonFile {
    param(
        [string]$Path,
        [hashtable]$Data
    )
    $json = ($Data | ConvertTo-Json -Depth 10 -Compress)
    $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
    [System.IO.File]::WriteAllText($Path, $json, $utf8NoBom)
}

function Initialize-DemoAndMockNode {
    if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) {
        throw "curl.exe is required on Windows (included in Windows 10/11)."
    }

    $baseUrl = "https://127.0.0.1:8443"
    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cordova-up-" + [System.Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    $loginJob = $null

    try {
        Write-Host "Provisioning local demo account and enrolling mock-node..."
        Invoke-Compose @("--profile", "mock-node", "build", "mock-node")

        $cookieFile = Join-Path $tmpDir "cookies.txt"
        $bodyFile = Join-Path $tmpDir "body.json"
        $demoUser = "devdemo"
        $demoPass = "correcthorsebatterystaple"
        $csrf = ""

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            username = $demoUser
            password = $demoPass
        }
        $loginJson = (& curl.exe -sk -c $cookieFile -X POST "$baseUrl/api/auth/login" -H "Content-Type: application/json" --data-binary "@$bodyFile" 2>$null) | Out-String
        if ($LASTEXITCODE -eq 0 -and $loginJson) {
            try {
                $csrf = ($loginJson | ConvertFrom-Json).csrf_token
            }
            catch {}
        }

        if (-not $csrf) {
            Write-Utf8JsonFile -Path $bodyFile -Data @{
                username               = $demoUser
                password               = $demoPass
                privacy_notice_version = "v1"
            }
            $regJson = (& curl.exe -skf -c $cookieFile -X POST "$baseUrl/api/auth/register" -H "Content-Type: application/json" --data-binary "@$bodyFile") | Out-String
            if ($LASTEXITCODE -ne 0) { throw "Failed to register local demo account" }
            $csrf = ($regJson | ConvertFrom-Json).csrf_token
        }
        if (-not $csrf) { throw "Missing csrf_token from demo authentication" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            amount_piconero = "10000000000"
        }
        & curl.exe -skf -b $cookieFile -H "X-CSRF-Token: $csrf" -X POST "$baseUrl/api/dev/deposit" -H "Content-Type: application/json" --data-binary "@$bodyFile" *> $null
        if ($LASTEXITCODE -ne 0) { throw "Dev deposit failed" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            name = "dev-up-key"
        }
        $keyJson = (& curl.exe -skf -b $cookieFile -H "X-CSRF-Token: $csrf" -X POST "$baseUrl/api/keys" -H "Content-Type: application/json" --data-binary "@$bodyFile") | Out-String
        if ($LASTEXITCODE -ne 0) { throw "Demo API key creation failed" }
        $apiKey = ($keyJson | ConvertFrom-Json).secret
        if (-not $apiKey) { throw "Missing demo API key secret" }

        $demoEnvPath = Join-Path $RootDir ".local\demo.env"
        $demoLines = @(
            "# Auto-generated local demo credentials for https://localhost:8443",
            "DEMO_USERNAME=$demoUser",
            "DEMO_PASSWORD=$demoPass",
            "DEMO_API_KEY=$apiKey",
            ""
        )
        $utf8NoBom = New-Object System.Text.UTF8Encoding($false)
        [System.IO.File]::WriteAllText($demoEnvPath, ($demoLines -join "`n"), $utf8NoBom)

        $loginLog = Join-Path $tmpDir "node-login.log"
        $loginJob = Start-Job -ScriptBlock {
            param($dir, $log)
            Set-Location $dir
            & docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node run --rm -T --no-deps mock-node login --server http://server:8080 --name dev-mock-node *> $log
        } -ArgumentList $RootDir, $loginLog

        $userCode = ""
        for ($i = 0; $i -lt 60; $i++) {
            if (Test-Path -LiteralPath $loginLog) {
                $content = Get-Content -LiteralPath $loginLog -Raw -ErrorAction SilentlyContinue
                if ($content -match "enter code:\s*([A-Za-z0-9-]+)") {
                    $userCode = $Matches[1].Trim()
                    break
                }
            }
            Start-Sleep -Milliseconds 500
        }
        if (-not $userCode) { throw "mock-node did not output enrollment user_code" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            user_code = $userCode
            name      = "dev-mock-node"
        }
        & curl.exe -skf -b $cookieFile -H "X-CSRF-Token: $csrf" -X POST "$baseUrl/api/nodes/link" -H "Content-Type: application/json" --data-binary "@$bodyFile" *> $null
        if ($LASTEXITCODE -ne 0) { throw "mock-node link failed" }

        Wait-Job $loginJob | Out-Null

        Invoke-Compose @("--profile", "mock-node", "up", "-d", "--force-recreate", "--no-deps", "mock-node")

        $modelReady = $false
        for ($i = 0; $i -lt 40; $i++) {
            $models = (& curl.exe -skf "$baseUrl/v1/models") | Out-String
            if ($models -match '"availability":"available"') {
                $modelReady = $true
                break
            }
            Start-Sleep -Milliseconds 500
        }
        if (-not $modelReady) { throw "Model cordova-tiny-chat did not become available after mock-node started" }

        Write-Host ""
        Write-Host "Cordova dev environment is ready (with connected mock-node):"
        Write-Host "  Web UI:        https://localhost:8443  (self-signed dev TLS cert)"
        Write-Host "  Demo login:    $demoUser / $demoPass  (saved in .local/demo.env)"
        Write-Host "  Demo API key:  $apiKey"
        Write-Host "  Model ready:   cordova-tiny-chat (availability: available)"
        Write-Host ""
        Write-Host "Test SSE completion right now (PowerShell):"
        Write-Host "  '{`"model`":`"cordova-tiny-chat`",`"stream`":true,`"messages`":[{`"role`":`"user`",`"content`":`"hello`"}]}' | curl.exe -skN https://127.0.0.1:8443/v1/chat/completions -H `"Authorization: Bearer $apiKey`" -H `"Content-Type: application/json`" --data-binary `"@-`""
        Write-Host ""
        Write-Host "Manage stack:"
        Write-Host "  Status:   .\scripts\cordova-dev.ps1 status"
        Write-Host "  Logs:     .\scripts\cordova-dev.ps1 logs [service]"
        Write-Host "  Smoke:    .\scripts\cordova-dev.ps1 smoke"
        Write-Host "  Stop:     .\scripts\cordova-dev.ps1 down"
    }
    finally {
        if ($loginJob) {
            Remove-Job -Job $loginJob -Force -ErrorAction SilentlyContinue
        }
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-Smoke {
    Require-Docker
    Require-Files
    Initialize-DevEnv

    if (-not (Get-Command curl.exe -ErrorAction SilentlyContinue)) {
        throw "curl.exe is required on Windows for the smoke test (included in Windows 10/11)."
    }

    $wasRunning = [bool](& docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml ps -q nginx 2>$null)
    $smokePassed = $false

    Write-Host "[1/5] Validating Compose configuration..."
    Invoke-Compose @("config", "-q")
    $services = (& docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml config --services)
    foreach ($forbidden in @("monerod", "monero-wallet-rpc", "payments")) {
        if ($services -contains $forbidden) {
            throw "FAIL: $forbidden must not be enabled in default dev Compose profile"
        }
    }

    $tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cordova-smoke-" + [System.Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmpDir -Force | Out-Null
    $loginJob = $null

    try {
        Write-Host "[2/5] Building and starting dev Compose stack..."
        Invoke-Compose @("up", "-d", "--build", "--wait")

        $baseUrl = "https://127.0.0.1:8443"
        Write-Host "[3/5] Checking web UI and auth/billing flow via nginx ($baseUrl)..."
        $html = (& curl.exe -skf "$baseUrl/") | Out-String
        if ($LASTEXITCODE -ne 0 -or $html -notmatch "Cordova") {
            throw "FAIL: web UI check failed"
        }

        $cookieFile = Join-Path $tmpDir "cookies.txt"
        $bodyFile = Join-Path $tmpDir "body.json"
        $uname = "smoke_" + [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
        Write-Utf8JsonFile -Path $bodyFile -Data @{
            username               = $uname
            password               = "correcthorsebatterystaple"
            privacy_notice_version = "v1"
        }
        $regJson = (& curl.exe -skf -c $cookieFile -X POST "$baseUrl/api/auth/register" -H "Content-Type: application/json" --data-binary "@$bodyFile") | Out-String
        if ($LASTEXITCODE -ne 0) { throw "FAIL: register request failed" }
        $regObj = $regJson | ConvertFrom-Json
        $csrf = $regObj.csrf_token
        if (-not $csrf) { throw "FAIL: missing csrf_token" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            amount_piconero = "10000000000"
        }
        & curl.exe -skf -b $cookieFile -H "X-CSRF-Token: $csrf" -X POST "$baseUrl/api/dev/deposit" -H "Content-Type: application/json" --data-binary "@$bodyFile" *> $null
        if ($LASTEXITCODE -ne 0) { throw "FAIL: dev deposit failed" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            name = "compose-smoke-key"
        }
        $keyJson = (& curl.exe -skf -b $cookieFile -H "X-CSRF-Token: $csrf" -X POST "$baseUrl/api/keys" -H "Content-Type: application/json" --data-binary "@$bodyFile") | Out-String
        if ($LASTEXITCODE -ne 0) { throw "FAIL: API key creation failed" }
        $apiKey = ($keyJson | ConvertFrom-Json).secret
        if (-not $apiKey) { throw "FAIL: missing API key secret" }

        Write-Host "[4/5] Enrolling mock-node container and testing SSE completion..."
        Invoke-Compose @("--profile", "mock-node", "build", "mock-node")

        $loginLog = Join-Path $tmpDir "node-login.log"
        $loginJob = Start-Job -ScriptBlock {
            param($dir, $log)
            Set-Location $dir
            & docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node run --rm -T --no-deps mock-node login --server http://server:8080 --name compose-mock-node *> $log
        } -ArgumentList $RootDir, $loginLog

        $userCode = ""
        for ($i = 0; $i -lt 60; $i++) {
            if (Test-Path -LiteralPath $loginLog) {
                $content = Get-Content -LiteralPath $loginLog -Raw -ErrorAction SilentlyContinue
                if ($content -match "enter code:\s*([A-Za-z0-9-]+)") {
                    $userCode = $Matches[1].Trim()
                    break
                }
            }
            Start-Sleep -Milliseconds 500
        }
        if (-not $userCode) { throw "FAIL: mock-node did not output enrollment user_code" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            user_code = $userCode
            name      = "compose-mock-node"
        }
        & curl.exe -skf -b $cookieFile -H "X-CSRF-Token: $csrf" -X POST "$baseUrl/api/nodes/link" -H "Content-Type: application/json" --data-binary "@$bodyFile" *> $null
        if ($LASTEXITCODE -ne 0) { throw "FAIL: node link failed" }

        Wait-Job $loginJob | Out-Null

        Invoke-Compose @("--profile", "mock-node", "up", "-d", "--force-recreate", "--no-deps", "mock-node")

        $modelReady = $false
        for ($i = 0; $i -lt 40; $i++) {
            $models = (& curl.exe -skf "$baseUrl/v1/models") | Out-String
            if ($models -match '"availability":"available"') {
                $modelReady = $true
                break
            }
            Start-Sleep -Milliseconds 500
        }
        if (-not $modelReady) { throw "FAIL: model did not become available after mock-node started" }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            model    = "cordova-tiny-chat"
            messages = @(
                @{
                    role    = "user"
                    content = "hello compose"
                }
            )
        }
        $resp = (& curl.exe -skf -X POST "$baseUrl/v1/chat/completions" `
            -H "Authorization: Bearer $apiKey" `
            -H "Content-Type: application/json" `
            --data-binary "@$bodyFile") | Out-String
        if ($LASTEXITCODE -ne 0 -or $resp -notmatch "Hello from Cordova Rust node") {
            throw "FAIL: non-stream completion check failed"
        }

        Write-Utf8JsonFile -Path $bodyFile -Data @{
            model    = "cordova-tiny-chat"
            stream   = $true
            messages = @(
                @{
                    role    = "user"
                    content = "hello compose stream"
                }
            )
        }
        $sse = (& curl.exe -skfN -X POST "$baseUrl/v1/chat/completions" `
            -H "Authorization: Bearer $apiKey" `
            -H "Content-Type: application/json" `
            --data-binary "@$bodyFile") | Out-String
        if ($LASTEXITCODE -ne 0 -or $sse -notmatch "Cordova" -or $sse -notmatch "data: \[DONE\]") {
            throw "FAIL: SSE completion check failed"
        }

        $smokePassed = $true
        Write-Host "PASS: Compose dev smoke (nginx TLS + server + redis + kafka + tokenizer + mock-node + SSE)"
    }
    catch {
        Write-ComposeDiagnostics
        throw
    }
    finally {
        if ($loginJob) {
            Remove-Job -Job $loginJob -Force -ErrorAction SilentlyContinue
        }
        if ($wasRunning -and $smokePassed) {
            Write-Host "[5/5] Keeping dev Compose stack and mock-node running..."
        }
        else {
            Write-Host "[5/5] Stopping dev Compose services (preserving persistent volumes)..."
            & docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node down *> $null
        }
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

switch ($Command) {
    "up" {
        Require-Docker
        Require-Files
        Initialize-DevEnv
        Assert-Port8443Available
        Write-Host "Validating Compose configuration..."
        Invoke-Compose @("config", "-q")
        $services = (& docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml config --services)
        foreach ($forbidden in @("monerod", "monero-wallet-rpc", "payments")) {
            if ($services -contains $forbidden) {
                throw "FAIL: $forbidden must not be enabled in default dev Compose profile"
            }
        }
        try {
            Write-Host "Building and starting Cordova dev stack (without Monero/payments)..."
            Invoke-Compose @("up", "-d", "--build", "--wait")
            Initialize-DemoAndMockNode
        }
        catch {
            Write-ComposeDiagnostics
            throw
        }
    }
    "status" {
        Require-Docker
        Require-Files
        if (-not (Test-Path -LiteralPath ".local/dev.env")) {
            Write-Host "Dev environment is not initialized yet (.local/dev.env missing). Run: .\scripts\cordova-dev.ps1 up"
            exit 0
        }
        Invoke-Compose @("--profile", "mock-node", "ps")
    }
    "logs" {
        Require-Docker
        Require-Files
        if (-not (Test-Path -LiteralPath ".local/dev.env")) {
            throw ".local/dev.env missing. Run '.\scripts\cordova-dev.ps1 up' first."
        }
        $logArgs = @("--profile", "mock-node", "logs", "-f", "--tail=100")
        if ($Service) { $logArgs += $Service }
        Invoke-Compose $logArgs
    }
    "smoke" {
        Invoke-Smoke
    }
    "down" {
        Require-Docker
        Require-Files
        if (-not (Test-Path -LiteralPath ".local/dev.env")) {
            Write-Host "Nothing to stop (.local/dev.env missing)."
            exit 0
        }
        Write-Host "Stopping Cordova dev services (preserving persistent volumes and .local/)..."
        Invoke-Compose @("--profile", "mock-node", "down")
    }
    "reset" {
        Require-Docker
        Require-Files
        if (-not $Yes) {
            Write-Host "WARNING: This will stop all Cordova dev containers and permanently delete Docker volumes and .local/ secrets/certs."
            $confirm = Read-Host "Type DELETE to confirm"
            if ($confirm -ne "DELETE") {
                Write-Host "Aborted."
                exit 1
            }
        }
        if (Test-Path -LiteralPath ".local/dev.env") {
            & docker compose --env-file .local/dev.env -f infra/compose.yaml -f infra/compose.dev.yaml --profile mock-node --profile payments --profile ops down -v --remove-orphans
        }
        if (Test-Path -LiteralPath ".local") {
            Remove-Item -LiteralPath ".local" -Recurse -Force
        }
        Write-Host "Cordova dev volumes and .local/ removed."
    }
    default {
        Show-Usage
        if ($Command -ne "help") { exit 1 }
    }
}
