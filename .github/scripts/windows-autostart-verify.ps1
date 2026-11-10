#Requires -Version 7
<#
Runs the Windows autostart checks that #472 and #545 left without a recorded
run, against the archive and Scoop manifest GoReleaser produced in -Dist.

It registers and runs the real "\Terminal Presence\termp" scheduled task, so
it is only for a disposable CI runner. It cannot observe a real logon or
whether a console window is visible; the runner has no interactive desktop.
Every check prints PASS or FAIL and the script exits 1 if any failed.
#>
param([Parameter(Mandatory)][string]$Dist)

$ErrorActionPreference = 'Stop'
$TaskName = '\Terminal Presence\termp'
$Log = Join-Path $env:LOCALAPPDATA 'termp\termp.log'
$script:Failures = [System.Collections.Generic.List[string]]::new()

function Check([string]$Name, [bool]$Ok, [string]$Detail = '') {
    if ($Ok) { Write-Host "PASS  $Name" } else { Write-Host "FAIL  $Name"; $script:Failures.Add($Name) }
    if ($Detail.Trim()) { Write-Host ($Detail.TrimEnd() -replace '(?m)^', '        ') }
}

function Invoke-Native([string]$Exe, [string[]]$Arguments) {
    $ErrorActionPreference = 'Continue'
    $output = & $Exe @Arguments 2>&1 | ForEach-Object { "$_" } | Out-String
    [pscustomobject]@{ Code = $LASTEXITCODE; Output = $output }
}

function Wait-Until([scriptblock]$Condition, [int]$Seconds = 30) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $value = & $Condition
        if ($value) { return $value }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    return $null
}

function Get-TaskAction {
    $query = Invoke-Native schtasks @('/Query', '/TN', $TaskName, '/XML')
    if ($query.Code -ne 0) { return $null }
    # The declaration says UTF-16 but this is already a decoded string.
    ([xml]($query.Output -replace '<\?xml[^>]*\?>', '')).Task.Actions.Exec
}

function Format-Action($Action) {
    if (-not $Action) { return 'no task registered' }
    "Command:   $($Action.Command)`nArguments: $($Action.Arguments)"
}

# termp.exe daemons whose image lives under $Dir.
function Get-Daemon([string]$Dir) {
    @(Get-CimInstance Win32_Process -Filter "Name = 'termp.exe'" |
        Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($Dir, 'OrdinalIgnoreCase') -and $_.CommandLine -match 'start --foreground' })
}

function Get-LogLines {
    if (Test-Path $Log) { @(Get-Content $Log) } else { @() }
}

# Leave no daemon behind between checks. `termp stop` also catches a daemon
# that schtasks /End left running, so one failed check cannot cascade.
function Reset-Daemon([string]$Dir, [string]$Termp) {
    $null = Invoke-Native schtasks @('/End', '/TN', $TaskName)
    $null = Invoke-Native $Termp @('stop')
    $null = Wait-Until { (Get-Daemon $Dir).Count -eq 0 } 20
}

# schtasks /Run, then confirm the daemon starts, logs its start, stays up, and
# that schtasks /End stops it.
function Test-TaskRun([string]$Label, [string]$Dir, [string]$Termp, [string]$WantArgument, [string]$WantParent, [string]$WantTrigger) {
    Reset-Daemon $Dir $Termp
    $logBefore = (Get-LogLines).Count
    $runStart = Get-Date
    $run = Invoke-Native schtasks @('/Run', '/TN', $TaskName)
    Check "${Label}: schtasks /Run exits 0" ($run.Code -eq 0) $run.Output

    $daemon = Wait-Until { Get-Daemon $Dir | Where-Object { $_.CreationDate -ge $runStart -and $_.CommandLine -like "*$WantArgument*" } }
    Check "${Label}: daemon process starts with $WantArgument" ([bool]$daemon) `
        ((@(Get-Daemon $Dir) | ForEach-Object { "pid $($_.ProcessId) started $($_.CreationDate): $($_.CommandLine)" }) -join "`n")
    if (-not $daemon) { Reset-Daemon $Dir $Termp; return }
    $daemon = @($daemon)[0]
    if ($WantParent) {
        $parent = Get-CimInstance Win32_Process -Filter "ProcessId = $($daemon.ParentProcessId)"
        Check "${Label}: daemon parent is $WantParent" ($parent.Name -eq $WantParent) "daemon pid $($daemon.ProcessId), parent: $($parent.ExecutablePath)"
    }

    $wantLine = "daemon started: *trigger=`"$WantTrigger`""
    $started = Wait-Until { Get-LogLines | Select-Object -Skip $logBefore | Where-Object { $_ -like "*$wantLine*" } } 15
    Check "${Label}: termp.log records '$wantLine'" ([bool]$started) `
        ("last lines of ${Log}:`n" + ((Get-LogLines | Select-Object -Last 8) -join "`n"))

    Start-Sleep -Seconds 15
    Check "${Label}: daemon is still running 15s later" ([bool](Get-Process -Id $daemon.ProcessId -ErrorAction SilentlyContinue)) `
        ("last lines of ${Log}:`n" + ((Get-LogLines | Select-Object -Last 8) -join "`n"))

    $end = Invoke-Native schtasks @('/End', '/TN', $TaskName)
    $stopped = Wait-Until { -not (Get-Process -Id $daemon.ProcessId -ErrorAction SilentlyContinue) } 20
    Check "${Label}: schtasks /End stops the daemon" ($end.Code -eq 0 -and $stopped) `
        ($end.Output + $(if (-not $stopped) { "daemon pid $($daemon.ProcessId) is still running" }))
    Reset-Daemon $Dir $Termp
}

function Test-Uninstall([string]$Label, [string]$Termp) {
    $uninstall = Invoke-Native $Termp @('autostart', 'uninstall')
    Check "${Label}: autostart uninstall (no --force) exits 0 and removes the task" ($uninstall.Code -eq 0 -and -not (Get-TaskAction)) $uninstall.Output
}

function Test-ShippedLayout {
    $zips = @(Get-ChildItem $Dist -Filter 'termp_*_windows_amd64.zip')
    if ($zips.Count -ne 1) { throw "want exactly one windows_amd64 archive in $Dist, found $($zips.Count)" }
    $dir = Join-Path $env:LOCALAPPDATA 'Programs\termp'
    Expand-Archive $zips[0].FullName -DestinationPath $dir -Force
    $termp = Join-Path $dir 'termp.exe'
    $launcher = Join-Path $dir 'termpw.exe'
    Check '#545: shipped archive puts termp.exe and termpw.exe side by side' ((Test-Path $termp) -and (Test-Path $launcher)) `
        "$($zips[0].Name): $((Get-ChildItem $dir).Name -join ', ')"

    $install = Invoke-Native $termp @('autostart', 'install')
    Check '#545 shipped layout: autostart install exits 0 with no missing-launcher warning' `
        ($install.Code -eq 0 -and $install.Output -notmatch 'termpw\.exe was not found') $install.Output
    $action = Get-TaskAction
    Check '#545 shipped layout: task action is termpw.exe with no arguments' `
        ($action -and $action.Command -ieq $launcher -and -not $action.Arguments) (Format-Action $action)
    Test-TaskRun '#545 shipped layout' $dir $termp '--internal-daemon-log' 'termpw.exe' 'login service'

    # termpw.exe can take a moment to exit after the daemon it waits on.
    $null = Wait-Until { Remove-Item $launcher -ErrorAction SilentlyContinue; -not (Test-Path $launcher) } 15
    $install = Invoke-Native $termp @('autostart', 'install')
    $warning = "Warning: termpw.exe was not found at `"$launcher`""
    Check '#545 fallback: autostart install exits 0 and warns that termpw.exe is missing' `
        ($install.Code -eq 0 -and $install.Output.Contains($warning)) "want: $warning`n$($install.Output)"
    $action = Get-TaskAction
    Check '#545 fallback: task action is termp.exe start --foreground --internal-autostart' `
        ($action -and $action.Command -ieq $termp -and $action.Arguments -eq 'start --foreground --internal-autostart') (Format-Action $action)
    # No parent check: Task Scheduler starts termp.exe directly here.
    Test-TaskRun '#545 fallback' $dir $termp '--internal-autostart' '' 'Windows Task Scheduler fallback'
    Test-Uninstall '#545' $termp
}

function Write-ScoopManifest([pscustomobject]$Manifest, [string]$Path) {
    $Manifest | ConvertTo-Json -Depth 10 | Set-Content -Path $Path -Encoding utf8
}

function Test-Scoop {
    # Pinned installer commit; the installer itself fetches Scoop's current release.
    $installer = Join-Path $env:RUNNER_TEMP 'install-scoop.ps1'
    Invoke-WebRequest 'https://raw.githubusercontent.com/ScoopInstaller/Install/1e2f334083d609986d8c8bc9e31ae8e87c39fab4/install.ps1' -OutFile $installer
    & $installer -RunAsAdmin
    $scoop = Join-Path $env:USERPROFILE 'scoop'
    $env:PATH = "$(Join-Path $scoop 'shims');$env:PATH"

    # The generated manifest points at a release that is not published, so
    # serve the snapshot archive locally. Its hash is GoReleaser's, unchanged.
    $port = 8731
    $server = Start-Process python -ArgumentList '-m', 'http.server', $port, '--bind', '127.0.0.1', '--directory', (Resolve-Path $Dist) -PassThru -WindowStyle Hidden
    try {
        $null = Wait-Until { try { (Invoke-WebRequest "http://127.0.0.1:$port/" -UseBasicParsing).StatusCode -eq 200 } catch { $false } } 20
        $manifestPath = Join-Path (New-Item -ItemType Directory -Force (Join-Path $env:RUNNER_TEMP 'scoop-manifest')) 'termp.json'
        $manifest = Get-Content (Join-Path $Dist 'scoop\termp.json') -Raw | ConvertFrom-Json
        $zip = Split-Path $manifest.architecture.'64bit'.url -Leaf
        $manifest.architecture.'64bit'.url = "http://127.0.0.1:$port/$zip"
        Write-ScoopManifest $manifest $manifestPath

        $null = Invoke-Native scoop @('install', $manifestPath)
        $appRoot = Join-Path $scoop 'apps\termp'
        $current = Join-Path $appRoot 'current'
        $launcher = Join-Path $current 'termpw.exe'
        Check '#472: scoop install from the generated manifest puts termp.exe and termpw.exe in apps\termp\current' `
            ((Test-Path (Join-Path $current 'termp.exe')) -and (Test-Path $launcher)) "manifest version $($manifest.version)"

        $termp = (Get-Command termp).Source
        Check '#472: termp on PATH is the Scoop shim' ($termp -ieq (Join-Path $scoop 'shims\termp.exe')) $termp

        $setup = Invoke-Native python @((Join-Path $PSScriptRoot 'drive-setup-tui.py'), $termp)
        Check '#472 Scoop: interactive termp setup (real ConPTY) applies with autostart on' ($setup.Code -eq 0) $setup.Output
        $action = Get-TaskAction
        Check '#472 Scoop: setup registered the task on apps\termp\current\termpw.exe' `
            ($action -and $action.Command -ieq $launcher -and -not $action.Arguments) (Format-Action $action)

        $install = Invoke-Native $termp @('autostart', 'install')
        $diagnostic = if ($install.Code -ne 0) { "`n" + (Invoke-Native fsutil @('reparsepoint', 'query', $current)).Output }
        Check '#472 Scoop: termp autostart install exits 0 with no missing-launcher warning' `
            ($install.Code -eq 0 -and $install.Output -notmatch 'termpw\.exe was not found') ($install.Output + $diagnostic)
        $action = Get-TaskAction
        Check '#472 Scoop: autostart install registered the task on apps\termp\current\termpw.exe' `
            ($action -and $action.Command -ieq $launcher -and -not $action.Arguments) (Format-Action $action)
        Test-TaskRun '#472 Scoop' $appRoot $termp '--internal-daemon-log' 'termpw.exe' 'login service'

        # A Scoop update installs a new version directory and cleanup deletes the
        # old one; the task must survive that and still be recognized as ours.
        $oldVersionDir = Join-Path $appRoot $manifest.version
        $manifest.version = "$($manifest.version)-update"
        Write-ScoopManifest $manifest $manifestPath
        $null = Invoke-Native scoop @('update', 'termp')
        $null = Invoke-Native scoop @('cleanup', 'termp')
        $newTarget = (Get-Item $current).Target
        Check '#472 Scoop: scoop update + cleanup replaced the old version directory' `
            (-not (Test-Path $oldVersionDir) -and "$newTarget" -like "*$($manifest.version)*") "current -> $newTarget"
        Test-TaskRun '#472 Scoop after update' $appRoot $termp '--internal-daemon-log' 'termpw.exe' 'login service'
        $install = Invoke-Native $termp @('autostart', 'install')
        Check '#472 Scoop after update: autostart install (no --force) still owns the task' ($install.Code -eq 0) $install.Output
        Reset-Daemon $appRoot $termp
        Test-Uninstall '#472 Scoop' $termp
    } finally {
        Stop-Process -Id $server.Id -ErrorAction SilentlyContinue
    }
}

foreach ($part in 'Test-ShippedLayout', 'Test-Scoop') {
    Write-Host "`n== $part"
    try { & $part } catch { Check "$part completed without an unexpected error" $false ($_ | Out-String) }
}
$null = Invoke-Native schtasks @('/End', '/TN', $TaskName)

if ($script:Failures.Count) {
    Write-Host "`n$($script:Failures.Count) check(s) failed:"
    $script:Failures | ForEach-Object { Write-Host "  $_" }
    exit 1
}
Write-Host "`nAll checks passed."
exit 0
