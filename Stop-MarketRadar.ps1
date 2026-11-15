$ErrorActionPreference = 'Stop'
$expected = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot 'bin\marketradar.exe'))
$targets = Get-CimInstance Win32_Process -Filter "Name='marketradar.exe'" | Where-Object { $_.ExecutablePath -and [IO.Path]::GetFullPath($_.ExecutablePath) -eq $expected }
foreach ($target in $targets) { Stop-Process -Id $target.ProcessId }
if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'data/logs/admin-next.pid')) {
    $nextID = Get-Content -LiteralPath (Join-Path $PSScriptRoot 'data/logs/admin-next.pid') -ErrorAction SilentlyContinue | Select-Object -First 1
    $nextProcess = Get-CimInstance Win32_Process -Filter "ProcessId=$nextID" -ErrorAction SilentlyContinue
    if ($nextProcess -and $nextProcess.CommandLine -like '*node_modules/next/dist/bin/next*--port 3000*') { Stop-Process -Id $nextProcess.ProcessId -ErrorAction SilentlyContinue }
}
Write-Host 'MarketRadar stopped. Saved profiles and history remain in data/.'
