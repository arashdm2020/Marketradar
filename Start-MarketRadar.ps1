$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

New-Item -ItemType Directory -Path 'bin','data','data/logs' -Force | Out-Null
$launchLog = Join-Path $PSScriptRoot 'data/logs/launcher.log'
Set-Content -LiteralPath $launchLog -Value '' -Encoding UTF8
function Write-LaunchLog([string]$message) {
    $line = ('{0} {1}' -f (Get-Date).ToUniversalTime().ToString('s'), $message)
    Add-Content -LiteralPath $launchLog -Value $line -Encoding UTF8
    Write-Host $message
}

# One process hosts Telegram polling, web panels, analysis, and alerts.
$lock = [System.Threading.Mutex]::new($false, 'Local\MarketRadarTradeBot')
$owned = $false
try {
    $owned = $lock.WaitOne(0)
    if (-not $owned) { throw 'MarketRadar launcher is already running.' }

    if (-not (Test-Path -LiteralPath '.env')) { throw 'Local .env file is missing.' }
    $settings = @{}
    foreach ($line in Get-Content -LiteralPath '.env') {
        $entry = $line.Trim()
        if ($entry -and -not $entry.StartsWith('#') -and $entry.Contains('=')) {
            $parts = $entry.Split('=', 2)
            $settings[$parts[0].Trim()] = $parts[1].Trim().Trim('"').Trim("'")
        }
    }
    foreach ($key in $settings.Keys) {
        if (-not [Environment]::GetEnvironmentVariable($key)) {
            [Environment]::SetEnvironmentVariable($key, $settings[$key])
        }
    }
    $missing = @('TWELVEDATA_API_KEY','NVIDIA_API_KEY','TELEGRAM_BOT_TOKEN') | Where-Object { -not [Environment]::GetEnvironmentVariable($_) }
    if ($missing.Count -gt 0) { throw ('Missing required configuration: ' + ($missing -join ', ')) }

    # Interactive testing must never issue a BNB invoice.
    [Environment]::SetEnvironmentVariable('WEB_BSC_TEST_INVOICES_ENABLED', 'false')
    # Mainnet sweeping stays fail-closed in the local all-in-one launcher.
    [Environment]::SetEnvironmentVariable('TREASURY_SWEEP_ENABLED', 'false')
    $address = [Environment]::GetEnvironmentVariable('ADMIN_LISTEN_ADDR')
    if (-not $address -or $address -eq 'disabled') {
        $address = '127.0.0.1:8088'
        [Environment]::SetEnvironmentVariable('ADMIN_LISTEN_ADDR', $address)
    }
    if ($address -notmatch '^(127\.0\.0\.1|localhost):[0-9]+$') { throw 'Web listener must use localhost.' }

    $env:GOMAXPROCS = '2'
    Write-LaunchLog 'Building the current MarketRadar source.'
    & go build -p 1 -o '.\bin\marketradar.exe' .
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed.' }
    if (-not (Test-Path -LiteralPath 'admin-next/node_modules/next/dist/bin/next')) {
        & npm.cmd ci --prefix 'admin-next' --no-audit --no-fund
        if ($LASTEXITCODE -ne 0) { throw 'Next.js dependency install failed.' }
    }
    & npm.cmd run build --prefix 'admin-next'
    if ($LASTEXITCODE -ne 0) { throw 'Next.js build failed.' }

    $token = [Environment]::GetEnvironmentVariable('TELEGRAM_BOT_TOKEN')
    try {
        $me = Invoke-RestMethod -Uri ('https://api.telegram.org/bot' + $token + '/getMe') -TimeoutSec 12
        if ($me.ok) {
            [Environment]::SetEnvironmentVariable('TELEGRAM_BOT_USERNAME', [string]$me.result.username)
            Write-LaunchLog ('Telegram identity: @' + $me.result.username + ' (ID ' + $me.result.id + ').')
        } else { Write-LaunchLog 'Telegram getMe did not confirm the bot; polling will retry.' }
    } catch { Write-LaunchLog 'Telegram getMe is unavailable; web and analysis will start while polling retries.' }
    try {
        $hook = Invoke-RestMethod -Uri ('https://api.telegram.org/bot' + $token + '/getWebhookInfo') -TimeoutSec 12
        if ($hook.ok -and $hook.result.url) { Write-LaunchLog 'A Telegram webhook is configured; long polling requires its removal.' }
    } catch { Write-LaunchLog 'Telegram webhook check is unavailable; polling will retry.' }

    $logOut = Join-Path $PSScriptRoot 'data/logs/marketradar.out.log'
    $logErr = Join-Path $PSScriptRoot 'data/logs/marketradar.err.log'
    $process = Start-Process -FilePath '.\bin\marketradar.exe' -ArgumentList 'bot' `
        -WorkingDirectory $PSScriptRoot -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $logOut -RedirectStandardError $logErr
    Set-Content -LiteralPath (Join-Path $PSScriptRoot 'data/logs/marketradar.pid') -Value $process.Id
    Write-LaunchLog ('MarketRadar process started: PID ' + $process.Id + '; database ' + [Environment]::GetEnvironmentVariable('SQLITE_PATH'))
    $nextDir = Join-Path $PSScriptRoot 'admin-next'
    $nextOut = Join-Path $PSScriptRoot 'data/logs/admin-next.out.log'
    $nextErr = Join-Path $PSScriptRoot 'data/logs/admin-next.err.log'
    $next = Start-Process -FilePath (Get-Command node.exe).Source -ArgumentList @('node_modules/next/dist/bin/next','start','--hostname','127.0.0.1','--port','3000') `
        -WorkingDirectory $nextDir -WindowStyle Hidden -PassThru -RedirectStandardOutput $nextOut -RedirectStandardError $nextErr
    Set-Content -LiteralPath (Join-Path $PSScriptRoot 'data/logs/admin-next.pid') -Value $next.Id

    $webURL = 'http://' + $address + '/app/'
    $webReady = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        $process.Refresh()
        if ($process.HasExited) { break }
        try {
            $response = Invoke-WebRequest -Uri $webURL -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -eq 200) { $webReady = $true; break }
        } catch {}
        Start-Sleep -Seconds 1
    }
    if ($webReady) { Write-LaunchLog ('Web panels ready: ' + $webURL + ' and http://' + $address + '/') }
    else { Write-LaunchLog ('Web startup could not be verified; inspect ' + $logErr) }
    $nextReady = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) {
        $next.Refresh()
        if ($next.HasExited) { break }
        try { $response = Invoke-WebRequest -Uri 'http://127.0.0.1:3000/' -UseBasicParsing -TimeoutSec 2; if ($response.StatusCode -eq 200) { $nextReady = $true; break } } catch {}
        Start-Sleep -Seconds 1
    }
    if (-not $nextReady) { throw 'Next.js admin panel did not become ready.' }
    Write-LaunchLog ('Next.js admin panel ready: http://127.0.0.1:3000/ (PID ' + $next.Id + ')')
    Write-LaunchLog ('Live logs: ' + $logErr + ' and ' + $launchLog)
    $process.WaitForExit()
    if ($next -and -not $next.HasExited) { Stop-Process -Id $next.Id -ErrorAction SilentlyContinue }
    Write-LaunchLog ('MarketRadar process exited with code ' + $process.ExitCode)
    if ($process.ExitCode -ne 0) { exit $process.ExitCode }
} catch {
    Write-LaunchLog ('Launcher failed: ' + $_.Exception.Message)
    if ($process -and -not $process.HasExited) { Stop-Process -Id $process.Id -ErrorAction SilentlyContinue }
    exit 1
} finally {
    if ($next -and -not $next.HasExited) { Stop-Process -Id $next.Id -ErrorAction SilentlyContinue }
    if ($owned) { $lock.ReleaseMutex() }
    $lock.Dispose()
}
