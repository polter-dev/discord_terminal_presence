#Requires -Version 7
# Throwaway (#662): from the shipped archive, check that every way of ending the
# autostart task stops the daemon, and that disable, uninstall and stop still work.
param([Parameter(Mandatory)][string]$Dist)

$ErrorActionPreference = 'Stop'
$TaskPath = '\Terminal Presence\'
$TaskName = '\Terminal Presence\termp'
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

function Wait-Until([scriptblock]$Condition, [int]$Seconds = 20) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    do {
        $value = & $Condition
        if ($value) { return $value }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)
    return $null
}

function Get-Daemon {
    @(Get-CimInstance Win32_Process -Filter "Name = 'termp.exe'" |
        Where-Object { $_.ExecutablePath -and $_.ExecutablePath.StartsWith($script:Dir, 'OrdinalIgnoreCase') -and $_.CommandLine -match 'start --foreground' })
}

function Describe {
    $d = @(Get-Daemon | ForEach-Object {
        $p = Get-CimInstance Win32_Process -Filter "ProcessId = $($_.ParentProcessId)"
        "daemon pid $($_.ProcessId) parent=$(if ($p) { "$($p.Name)($($_.ParentProcessId))" } else { "<exited>($($_.ParentProcessId))" })"
    })
    $task = Get-ScheduledTask -TaskPath $TaskPath -TaskName 'termp' -ErrorAction SilentlyContinue
    $w = @(Get-Process termpw -ErrorAction SilentlyContinue).Count
    (($d + @("termpw.exe processes: $w", "task: $(if ($task) { $task.State } else { 'not registered' })")) -join "`n")
}

function Wait-DaemonUp([string]$Label) {
    $up = Wait-Until { (Get-Daemon).Count -gt 0 } 30
    Check "${Label}: daemon is running" ([bool]$up) (Describe)
    return [bool]$up
}

function Wait-DaemonGone([string]$Label, [string]$Output = '') {
    $gone = Wait-Until { (Get-Daemon).Count -eq 0 } 20
    Check "${Label}: daemon is gone" ([bool]$gone) ($Output + "`n" + (Describe))
}

function Run-Task {
    $null = Invoke-Native schtasks @('/Run', '/TN', $TaskName)
}

$zip = @(Get-ChildItem $Dist -Filter 'termp_*_windows_amd64.zip')[0]
$script:Dir = Join-Path $env:LOCALAPPDATA 'Programs\termp'
Expand-Archive $zip.FullName -DestinationPath $script:Dir -Force
$termp = Join-Path $script:Dir 'termp.exe'

Write-Host "`n== Stop-ScheduledTask"
$r = Invoke-Native $termp @('autostart', 'install')
Check 'autostart install exits 0' ($r.Code -eq 0) $r.Output
Run-Task
if (Wait-DaemonUp 'after install') {
    Stop-ScheduledTask -TaskPath $TaskPath -TaskName 'termp'
    Wait-DaemonGone 'Stop-ScheduledTask'
    $s = Invoke-Native $termp @('status')
    Write-Host ($s.Output -replace '(?m)^', '        ')
}
$null = Invoke-Native $termp @('stop')

Write-Host "`n== schtasks /End"
Run-Task
if (Wait-DaemonUp 'schtasks /Run') {
    $r = Invoke-Native schtasks @('/End', '/TN', $TaskName)
    Wait-DaemonGone 'schtasks /End' $r.Output
}
$null = Invoke-Native $termp @('stop')

Write-Host "`n== termp autostart disable"
Run-Task
if (Wait-DaemonUp 'before disable') {
    $r = Invoke-Native $termp @('autostart', 'disable')
    Check 'autostart disable exits 0' ($r.Code -eq 0) $r.Output
    Wait-DaemonGone 'autostart disable'
    $task = Get-ScheduledTask -TaskPath $TaskPath -TaskName 'termp' -ErrorAction SilentlyContinue
    Check 'autostart disable leaves the task Disabled' ($task -and "$($task.State)" -eq 'Disabled') (Describe)
}

Write-Host "`n== termp stop"
$r = Invoke-Native $termp @('autostart', 'enable')
Check 'autostart enable exits 0' ($r.Code -eq 0) $r.Output
if (-not (Wait-Until { (Get-Daemon).Count -gt 0 } 10)) { Run-Task }
if (Wait-DaemonUp 'after enable') {
    $r = Invoke-Native $termp @('stop')
    Check 'termp stop exits 0' ($r.Code -eq 0) $r.Output
    Wait-DaemonGone 'termp stop'
}

Write-Host "`n== termp autostart uninstall"
Run-Task
if (Wait-DaemonUp 'before uninstall') {
    $r = Invoke-Native $termp @('autostart', 'uninstall')
    Check 'autostart uninstall exits 0' ($r.Code -eq 0) $r.Output
    Wait-DaemonGone 'autostart uninstall'
    Check 'autostart uninstall removes the task' (-not (Get-ScheduledTask -TaskPath $TaskPath -TaskName 'termp' -ErrorAction SilentlyContinue)) (Describe)
}
$null = Invoke-Native $termp @('stop')

Write-Host "`n== termp setup with start at login turned off"
$r = Invoke-Native python @((Join-Path $PSScriptRoot 'drive-setup-tui.py'), $termp)
Check 'setup (defaults) applies' ($r.Code -eq 0 -and $r.Output -match 'Autostart: installed') ($r.Output | Select-String 'Autostart:' | Out-String)
if (-not (Wait-Until { (Get-Daemon).Count -gt 0 } 10)) { Run-Task }
if (Wait-DaemonUp 'after setup (defaults)') {
    $r = Invoke-Native python @((Join-Path $PSScriptRoot 'drive-setup-tui.py'), $termp, '--toggle-first-row')
    Check 'setup with start at login off applies and removes autostart' ($r.Code -eq 0 -and $r.Output -match 'Autostart: removed') ($r.Output | Select-String 'Autostart:|Setup applied' | Out-String)
    Wait-DaemonGone 'setup with start at login off'
}
$null = Invoke-Native $termp @('stop')
$null = Invoke-Native $termp @('autostart', 'uninstall')

if ($script:Failures.Count) {
    Write-Host "`n$($script:Failures.Count) check(s) failed:"
    $script:Failures | ForEach-Object { Write-Host "  $_" }
    exit 1
}
Write-Host "`nAll checks passed."
exit 0
