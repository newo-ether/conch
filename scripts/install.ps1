<#
.SYNOPSIS
    Install Conch Shell Server as a Windows managed background service.

.DESCRIPTION
    Downloads (or builds) the conch binary, generates an API key, and registers
    either a machine-wide nssm service or a per-user Scheduled Task. Interactive
    by default - use -Yes for scripting.

.PARAMETER ApiKey
    Pre-shared API key. If omitted, a random 32-byte key is generated.

.PARAMETER Port
    Listen port. Default: 14216.

.PARAMETER HostAddr
    Listen address. Default: 0.0.0.0.

.PARAMETER TimeoutSec
    Default command timeout in seconds. Default: 30.

.PARAMETER MaxTimeoutSec
    Maximum command timeout in seconds. Default: 1800.

.PARAMETER NoAuth
    Disable authentication. Insecure, dev only.

.PARAMETER BinaryPath
    Path to a pre-built conch.exe. Skips download / Go build.

.PARAMETER McpBinaryPath
    Path to a pre-built conch-mcp.exe. Skips download / Go build.

.PARAMETER Prefix
    Install root directory. Defaults to $env:ProgramFiles\Conch in system mode
    or $env:LOCALAPPDATA\Conch in user mode.

.PARAMETER NoStart
    Install but do not start the service or background task.

.PARAMETER Yes
    Skip all prompts, accept all defaults. Useful for scripting.

.PARAMETER Uninstall
    Remove one selected installation mode. When both modes exist, -Mode is
    required so an uninstall cannot remove the other mode accidentally.

.PARAMETER Mode
    Install mode: 'user' is the default for a new installation and creates a
    per-user background task that owns the Conch process and starts at logon.
    'system' registers a machine-wide Windows service and requires administrator
    privileges. An update automatically retains the one existing mode.

.EXAMPLE
    # New per-user install (default; random key, download from GitHub Releases)
    .\install.ps1

.EXAMPLE
    # Explicit machine-wide install (run as Administrator)
    .\install.ps1 -Mode system -Yes

.EXAMPLE
    # Custom port and key
    .\install.ps1 -Port 8080 -ApiKey "my-secret-key"

.EXAMPLE
    # Custom install location
    .\install.ps1 -Prefix "D:\Conch"

.EXAMPLE
    # Uninstall
    .\install.ps1 -Uninstall

.EXAMPLE
    # Per-user background process (auto-start at logon, runs as you)
    .\install.ps1 -Mode user
#>

param(
    [string]$ApiKey        = "",
    [ValidateRange(1, 65535)]
    [int]   $Port          = 14216,
    [string]$HostAddr      = "0.0.0.0",
    [ValidateRange(1, 604800)]
    [int]   $TimeoutSec    = 30,
    [ValidateRange(1, 604800)]
    [int]   $MaxTimeoutSec = 1800,
    [switch]$NoAuth        = $false,
    [string]$BinaryPath    = "",
    [string]$McpBinaryPath = "",
    [ValidatePattern('^(latest|v[0-9]+\.[0-9]+\.[0-9]+)$')]
    [string]$Version       = "latest",
    [string]$Prefix        = "",
    [switch]$NoStart       = $false,
    [switch]$Yes           = $false,
    [switch]$Uninstall     = $false,
    $Mode                  = "__unset__"
)

& {
if ($Mode -eq "__unset__") { $Mode = "" }
if ($Mode -and $Mode -notin @('system', 'user')) {
    throw "-Mode must be 'system' or 'user' (got '$Mode')."
}
try {

$ErrorActionPreference = "Stop"
$Host.UI.RawUI.WindowTitle = "Conch Installer"

if ($TimeoutSec -gt $MaxTimeoutSec) {
    throw "TimeoutSec must not exceed MaxTimeoutSec"
}
if ([string]::IsNullOrWhiteSpace($HostAddr)) {
    throw "HostAddr must not be empty"
}
foreach ($configValue in @($ApiKey, $HostAddr)) {
    if ($configValue -match "[\r\n]") {
        throw "Configuration values must not contain newlines"
    }
}

# ============================================================================
# Robustness: retry helper, trap, rollback state
# ============================================================================

$Script:RollbackStack = [System.Collections.Generic.List[object]]::new()

function Push-Rollback {
    param([ScriptBlock]$Action, [string]$Description)
    $Script:RollbackStack.Insert(0, @{ Action = $Action; Desc = $Description })
}

function Invoke-Rollback {
    if ($Script:RollbackStack.Count -eq 0) { return }
    Write-Host ""
    Write-Host "  Cleaning up partial installation..." -ForegroundColor Yellow
    foreach ($entry in $Script:RollbackStack) {
        Write-Host "    ${Yellow}>${Reset} $($entry.Desc)" -ForegroundColor Yellow
        try { & $entry.Action } catch { }
    }
}

function Retry-Command {
    param(
        [ScriptBlock]$Script,
        [int]$MaxAttempts = 3,
        [int]$DelaySeconds = 2,
        [string]$Description = "operation"
    )
    $attempt = 0
    $lastError = $null
    while ($attempt -lt $MaxAttempts) {
        $attempt++
        try {
            $result = & $Script
            return $result
        } catch {
            $lastError = $_
            if ($attempt -lt $MaxAttempts) {
                Write-Warn "Retry $attempt/$MaxAttempts for $Description... (waiting ${DelaySeconds}s)"
                Start-Sleep -Seconds $DelaySeconds
                $DelaySeconds = [Math]::Min($DelaySeconds * 2, 15)
            }
        }
    }
    throw $lastError
}

# ============================================================================
# Output helpers
# ============================================================================

function Write-Banner {
    $boxW = 46  # internal width between box borders
    $t1 = "Conch Shell Server"
    $t2 = "Windows Installer"
    Write-Host ""
    Write-Host "  +----------------------------------------------+" -ForegroundColor Cyan
    Write-Host ("  |          {0}{1}|" -f $t1, (' ' * ($boxW - 10 - $t1.Length))) -ForegroundColor Cyan
    Write-Host ("  |          {0}{1}|" -f $t2, (' ' * ($boxW - 10 - $t2.Length))) -ForegroundColor Cyan
    Write-Host "  +----------------------------------------------+" -ForegroundColor Cyan
    Write-Host ""
}

function Write-Step {
    param([string]$Current, [string]$Total, [string]$Text)
    Write-Host "  [$Current/$Total] " -ForegroundColor Cyan -NoNewline
    Write-Host $Text
}

function Write-OK   { Write-Host "    ${Green}+${Reset} $args" }
function Write-Warn { Write-Host "    ${Yellow}!${Reset} $args" }
function Write-Err  { Write-Host "    ${Red}x${Reset} $args" }
function Write-Info { Write-Host "    ${Cyan}>${Reset} $args" }

function Write-ErrorExit {
    param([string]$Message, [switch]$NoRollback)
    Write-Host ""
    Write-Err $Message
    if (-not $NoRollback) { Invoke-Rollback }
    Write-Host ""
    throw $Message
}

# ANSI color codes (fallback for older consoles)
$Reset  = [char]27 + "[0m"
$Bold   = [char]27 + "[1m"
$Cyan   = [char]27 + "[36m"
$Green  = [char]27 + "[32m"
$Yellow = [char]27 + "[33m"
$Red    = [char]27 + "[31m"

# Detect if console supports ANSI (Windows 10 1511+)
$AnsiOk = $Host.UI.SupportsVirtualTerminal -or
          ($env:WT_SESSION -or $env:ConEmuANSI -or $env:TERM -match 'xterm')

if (-not $AnsiOk) {
    $Reset = $Bold = $Cyan = $Green = $Yellow = $Red = ""
    function Write-OK   { Write-Host "    + $args" -ForegroundColor Green }
    function Write-Warn { Write-Host "    ! $args" -ForegroundColor Yellow }
    function Write-Err  { Write-Host "    x $args" -ForegroundColor Red }
    function Write-Info { Write-Host "    > $args" }
}

function Prompt-User {
    param([string]$Message, [string]$Default = "Y")
    if ($Yes) { return ($Default -eq "Y") }
    if ($env:CONCH_YES -eq "1") { return ($Default -eq "Y") }
    if ([Console]::IsInputRedirected) { return ($Default -eq "Y") }
    $choices = if ($Default -eq "Y") { "[Y/n]" } else { "[y/N]" }
    $reply = Read-Host "    ? ${Message} ${choices}"
    if ([string]::IsNullOrWhiteSpace($reply)) { return ($Default -eq "Y") }
    return $reply -notmatch '^[nN]'
}

# ============================================================================
# Banner
# ============================================================================
Write-Banner

# ============================================================================
# Install mode resolution (system service vs user background task)
# ============================================================================
function Select-InstallMode {
    param(
        [string]$RequestedMode,
        [bool]$ForUninstall,
        [bool]$HasCustomPrefix,
        [bool]$CustomPrefixExists,
        [bool]$SystemPresent,
        [bool]$UserPresent
    )

    if ($RequestedMode) { return $RequestedMode }

    if ($ForUninstall -and $HasCustomPrefix) {
        throw "Specify -Mode system or -Mode user when uninstalling a custom -Prefix."
    }
    if ($SystemPresent -and $UserPresent) {
        throw "Both system and user installations exist. Specify -Mode system or -Mode user."
    }
    if ($SystemPresent) { return "system" }
    if ($UserPresent) { return "user" }

    if (-not $ForUninstall -and $HasCustomPrefix -and $CustomPrefixExists) {
        throw "The custom -Prefix already exists but its installation mode cannot be identified. Specify -Mode system or -Mode user."
    }

    # New installations default to least-privilege user mode. With nothing to
    # uninstall, use the same default so an ordinary user gets a safe no-op.
    return "user"
}

function Resolve-InstallMode {
    param(
        [string]$RequestedMode,
        [bool]$ForUninstall,
        [string]$InstallPrefix
    )

    if ($RequestedMode) { return $RequestedMode }

    $modeProbeTask = $null
    if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
        $modeProbeTask = Get-ScheduledTask -TaskPath "\" -TaskName "Conch" -ErrorAction SilentlyContinue
    }
    $systemPresent = [bool](
        (Get-Service -Name "Conch" -ErrorAction SilentlyContinue) -or
        (Test-Path -LiteralPath "$env:ProgramFiles\Conch")
    )
    $userPresent = [bool](
        $modeProbeTask -or
        (Test-Path -LiteralPath "$env:LOCALAPPDATA\Conch")
    )
    $customPrefixExists = [bool](
        $InstallPrefix -and (Test-Path -LiteralPath $InstallPrefix)
    )

    return Select-InstallMode `
        -RequestedMode $RequestedMode `
        -ForUninstall $ForUninstall `
        -HasCustomPrefix ([bool]$InstallPrefix) `
        -CustomPrefixExists $customPrefixExists `
        -SystemPresent $systemPresent `
        -UserPresent $userPresent
}

$Mode = Resolve-InstallMode `
    -RequestedMode $Mode `
    -ForUninstall ([bool]$Uninstall) `
    -InstallPrefix $Prefix

if ($Mode -eq "user" -and ([Security.Principal.WindowsIdentity]::GetCurrent()).IsSystem) {
    Write-ErrorExit "User mode must be run from your own interactive session, not as SYSTEM or a service. Open a normal PowerShell window and re-run." -NoRollback
}

# ============================================================================
# Mode-specific constants must exist before Find-Nssm runs so a system upgrade
# can reuse the wrapper stored in its existing install directory.
# ============================================================================
$ServiceName = "Conch"
$TaskName   = "Conch"
$TaskPath   = "\"
$InstallDir = if ($Prefix) { $Prefix } elseif ($Mode -eq "user") { "$env:LOCALAPPDATA\Conch" } else { "$env:ProgramFiles\Conch" }
$BinPath    = "$InstallDir\conch.exe"
$McpBinPath = "$InstallDir\conch-mcp.exe"
$EnvFile    = "$InstallDir\env.txt"
$EnvFileTmp = "$InstallDir\env.txt.tmp"
$LaunchScript = "$InstallDir\launch.ps1"
$PidFile      = "$InstallDir\conch.pid"
$LogFile      = "$InstallDir\conch.log"
$ErrorLogFile = "$InstallDir\conch-error.log"
$NssmParametersPath = "HKLM:\SYSTEM\CurrentControlSet\Services\$ServiceName\Parameters"

# ============================================================================
# Step 1 - Environment checks
# ============================================================================
$Step = 1
$TotalSteps = if ($Uninstall) { 2 } else { 6 }
Write-Step $Step $TotalSteps "Checking environment..."

# --- PowerShell version (minimum 5.1) ---
if ($PSVersionTable.PSVersion -lt [Version]"5.1") {
    Write-ErrorExit "PowerShell 5.1 or later required. Current: $($PSVersionTable.PSVersion)" -NoRollback
}
Write-OK "PowerShell $($PSVersionTable.PSVersion)"

# --- Admin check ---
$isAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()
).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if ($Mode -eq "system" -and -not $isAdmin) {
    Write-Err "Administrator privileges required for system-mode install or uninstall."
    Write-Host "    Right-click PowerShell -> Run as Administrator, then re-run."
    if (-not $Uninstall) {
        Write-Host "    Or install as a per-user background task: .\install.ps1 -Mode user"
    }
    throw "Administrator privileges required."
}
if ($Uninstall) {
    Write-OK "Uninstall mode"
} elseif ($Mode -eq "system") {
    Write-OK "Administrator"
} else {
    Write-OK "User mode (no administrator required)"
}

# --- OS version sanity check ---
$osVer = [Environment]::OSVersion.Version
Write-OK "OS: Windows $($osVer.Major).$($osVer.Minor) (build $($osVer.Build))"

# --- nssm: locate or install (must succeed early, before any downloads) ---
function Find-Nssm {
    # 1. Already in PATH?
    $cmd = Get-Command nssm -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd }

    # 2. Known install locations
    $paths = @(
        "$env:ProgramFiles\nssm\win64\nssm.exe",
        "$env:ProgramFiles\nssm\nssm.exe",
        "${env:ProgramFiles(x86)}\nssm\nssm.exe",
        "$env:ChocolateyInstall\bin\nssm.exe",
        "$env:LOCALAPPDATA\Microsoft\WinGet\Packages\NSSM.NSSM_*\win64\nssm.exe"
    )
    # The installer's own directory is checked first so an upgrade can reuse the
    # nssm.exe it installed previously instead of falling through to winget.
    if (-not [string]::IsNullOrWhiteSpace($InstallDir)) {
        $paths = @("$InstallDir\nssm.exe") + $paths
    }
    foreach ($p in $paths) {
        $resolved = Get-Item $p -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($resolved) { return Get-Command $resolved.FullName }
    }

    return $null
}

function Install-Nssm {
    # A. winget (fastest, most reliable)
    Write-Info "Trying winget install NSSM.NSSM..."
    try {
        winget install NSSM.NSSM --accept-source-agreements --accept-package-agreements --silent 2>&1 | Out-Null
        Start-Sleep -Seconds 3
        $env:Path = [System.Environment]::GetEnvironmentVariable("Path", "Machine") + ";" +
                    [System.Environment]::GetEnvironmentVariable("Path", "User")
        $found = Find-Nssm
        if ($found) { return $found }
    } catch { }

    # Do not fall back to an unsigned direct archive. Installers must never execute a
    # service wrapper that was not authenticated by the configured package manager.
    Write-Warn "winget could not install NSSM; refusing an unverified direct download."
    return $null
}

if ($Mode -eq "system") {
    $nssm = Find-Nssm
    if ($nssm) {
        Write-OK "nssm: $($nssm.Source)"
    } elseif (-not $Uninstall) {
        $nssm = Install-Nssm
        if ($nssm) {
            Write-OK "nssm installed: $($nssm.Source)"
        } else {
            Write-Err "nssm (Non-Sucking Service Manager) is required."
            Write-Host "    Install it manually, then re-run this script:"
            Write-Host "      winget install NSSM.NSSM"
            Write-Host "    Or download from: https://nssm.cc/download"
            throw "nssm not found and could not be installed automatically."
        }
    }
} else {
    $nssm = $null
    Write-OK "User mode - no service manager required"
}

# --- Internet connectivity check (non-blocking, just a warning) ---
if (-not $Uninstall -and -not $BinaryPath) {
    try {
        $connTest = [Net.NetworkInformation.NetworkInterface]::GetIsNetworkAvailable()
        if (-not $connTest) {
            Write-Warn "No network connection detected - download may fail"
        } else {
            Write-OK "Network available"
        }
    } catch {
        # Connectivity check is best-effort
    }
}

# --- Port conflict check ---
# netstat is used directly because Get-NetTCPConnection can hang indefinitely while loading the
# NetTCPIP provider on some Windows service hosts.
if (-not $Uninstall) {
    $portInUse = $null
    $netstat = cmd /c "netstat -ano -p tcp 2>nul" 2>$null
    if ($netstat) {
        $match = $netstat | Select-String ":$Port\s+.*LISTENING" | Select-Object -First 1
        if ($match) {
            $parts = ([string]$match) -split '\s+'
            $ownerPid = $parts[-1]
            $portInUse = [PSCustomObject]@{ OwningProcess = [int]$ownerPid }
        }
    }
    if ($portInUse) {
        $proc = Get-Process -Id $portInUse.OwningProcess -ErrorAction SilentlyContinue
        $procName = if ($proc) { $proc.ProcessName } else { "unknown" }
        if ($procName -eq "conch") {
            Write-Info "Port $Port is held by an existing Conch process - continuing"
        } else {
            Write-Warn "Port $Port is already in use by: $procName"
            if (-not $Yes -and -not (Prompt-User "Continue anyway?" -Default "Y")) {
                Write-Info "Aborted. Choose a different port with -Port <number>"
                return
            }
        }
    } else {
        Write-OK "Port $Port available"
    }
}

# $ScriptDir is $null when invoked via irm | iex (no actual script file).
$ScriptDir = if ($MyInvocation.MyCommand.Path) {
    Split-Path -Parent $MyInvocation.MyCommand.Path
} else {
    Get-Location
}
$RepoDir = if (Test-Path "$ScriptDir\go.mod") {
    Resolve-Path "$ScriptDir"
} elseif (Test-Path "$ScriptDir\..\go.mod") {
    Resolve-Path "$ScriptDir\.."
} else {
    $tmpDir = Join-Path $env:TEMP "conch-install"
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    $tmpDir
}

$GitHubReleases = if ($Version -eq "latest") {
    "https://github.com/newo-ether/conch/releases/latest/download"
} else {
    "https://github.com/newo-ether/conch/releases/download/$Version"
}
$ChecksumManifest = "$RepoDir\checksums-$Version.txt"

# ============================================================================
# Helper: stop and wait for a service
# ============================================================================
function Stop-ServiceWait {
    param([string]$Name, [int]$TimeoutSec = 15)
    $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if (-not $svc -or $svc.Status -eq "Stopped") { return $true }
    if ($svc.Status -ne "Running") {
        # Try sc.exe stop anyway for hung states
        cmd /c "sc.exe stop `"$Name`" >nul 2>&1"
        Start-Sleep -Seconds 2
    }
    cmd /c "sc.exe stop `"$Name`" >nul 2>&1"
    while ($TimeoutSec -gt 0) {
        Start-Sleep -Seconds 1
        try { $svc.Refresh() } catch { return $true }
        if ($svc.Status -eq "Stopped") { return $true }
        $TimeoutSec--
    }
    Write-Warn "Service '$Name' did not stop within timeout. Refusing to kill processes by name."
    return $false
}

# Start through the Windows Service Controller, then treat the observed service
# state as authoritative. NSSM may report START_PENDING on stderr even though the
# service is starting normally, which PowerShell 5 turns into a terminating error
# when ErrorActionPreference is Stop.
function Start-ServiceWait {
    param([string]$Name, [int]$TimeoutSec = 15)

    $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if (-not $svc) {
        throw "Service '$Name' is not registered."
    }
    if ($svc.Status -eq "Running") {
        return $true
    }

    if ($svc.Status -eq "Stopped") {
        try {
            Start-Service -Name $Name -ErrorAction Stop
        } catch {
            $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
            if (-not $svc -or $svc.Status -notin @("Running", "StartPending")) {
                throw
            }
        }
    }

    while ($TimeoutSec -gt 0) {
        $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
        if (-not $svc) {
            return $false
        }
        if ($svc.Status -eq "Running") {
            return $true
        }
        if ($svc.Status -eq "Stopped") {
            return $false
        }
        Start-Sleep -Seconds 1
        $TimeoutSec--
    }
    return $false
}

# Native stderr must not become a terminating PowerShell error before the exit
# code is inspected. Keep all NSSM calls behind this boundary.
function Invoke-Nssm {
    param(
        [string]$Path,
        [string[]]$Arguments,
        [switch]$AllowFailure
    )

    $previousErrorAction = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        & $Path @Arguments 2>&1 | Out-Null
        $exitCode = $LASTEXITCODE
    } finally {
        $ErrorActionPreference = $previousErrorAction
    }

    if (-not $AllowFailure -and $exitCode -ne 0) {
        throw "nssm $($Arguments[0]) failed (exit code $exitCode)"
    }
}

# ============================================================================
# Helper: atomic file write (write temp, then move)
# ============================================================================
function Protect-SecretFile {
    param([string]$Path)
    if ((Get-Item -LiteralPath $Path).Attributes -band [IO.FileAttributes]::ReparsePoint) {
        throw 'Secret files must not be links'
    }
    if ($Mode -eq "user") {
        $userSid = [Security.Principal.WindowsIdentity]::GetCurrent().User
        $acl = New-Object Security.AccessControl.FileSecurity
        $acl.SetAccessRuleProtection($true, $false)
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule(
            [Security.Principal.SecurityIdentifier]'S-1-5-18', 'FullControl', 'Allow')))
        $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule(
            $userSid, 'FullControl', 'Allow')))
        Set-Acl -LiteralPath $Path -AclObject $acl
    } else {
        $acl = New-Object Security.AccessControl.FileSecurity
        $acl.SetSecurityDescriptorSddlForm('O:BAG:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)')
        Set-Acl -LiteralPath $Path -AclObject $acl
    }
}

function Protect-ServiceSecrets {
    param([string]$Path)
    if (-not (Test-Path -LiteralPath $Path)) { New-Item -Path $Path -Force | Out-Null }
    $acl = New-Object Security.AccessControl.RegistrySecurity
    $acl.SetSecurityDescriptorSddlForm('O:BAG:BAD:P(A;CI;KA;;;SY)(A;CI;KA;;;BA)')
    Set-Acl -LiteralPath $Path -AclObject $acl
}

function Write-AtomicConfig {
    param([string]$Content, [string]$Target)
    $swapID = [Guid]::NewGuid().ToString("N")
    $tmp = "$Target.tmp.$swapID"
    $backup = "$Target.swap-backup.$swapID"
    $utf8 = New-Object Text.UTF8Encoding($false)
    try {
        # Set permissions while the new file is still empty, before writing any secret.
        New-Item -ItemType File -Path $tmp -ErrorAction Stop | Out-Null
        Protect-SecretFile $tmp
        [IO.File]::WriteAllText($tmp, $Content, $utf8)
        if (Test-Path -LiteralPath $Target) {
            Protect-SecretFile $Target
            [IO.File]::Replace($tmp, $Target, $backup, $true)
        } else {
            Move-Item -LiteralPath $tmp -Destination $Target -ErrorAction Stop
        }
    } finally {
        Remove-Item -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
        Remove-Item -Force -LiteralPath $backup -ErrorAction SilentlyContinue
    }
}

# ============================================================================
# Helper: safe file/path removal with retry
# ============================================================================
function Remove-Safe {
    param([string]$Path, [string]$Label)
    if (-not (Test-Path -LiteralPath $Path)) { return $true }
    try {
        Retry-Command -Script {
            Remove-Item -Recurse -Force -LiteralPath $Path -ErrorAction Stop
        } -MaxAttempts 3 -DelaySeconds 1 -Description "removing $Label"
        return -not (Test-Path -LiteralPath $Path)
    } catch {
        Write-Warn "Could not remove $Label. It may be locked by another process."
        Write-Warn "  Please close any programs using it and delete manually: $Path"
        return $false
    }
}

function Test-SamePath {
    param([string]$Left, [string]$Right)
    if ([string]::IsNullOrWhiteSpace($Left) -or [string]::IsNullOrWhiteSpace($Right)) {
        return $false
    }
    return [StringComparer]::OrdinalIgnoreCase.Equals(
        [IO.Path]::GetFullPath($Left),
        [IO.Path]::GetFullPath($Right)
    )
}

function Get-ProcessesAtExactPath {
    param([string[]]$Paths)
    $expected = @{}
    foreach ($path in $Paths) {
        if (-not [string]::IsNullOrWhiteSpace($path)) {
            $expected[[IO.Path]::GetFullPath($path).ToLowerInvariant()] = $true
        }
    }

    foreach ($process in @(Get-Process -ErrorAction SilentlyContinue)) {
        try {
            $actual = $process.Path
            if ($actual -and $expected.ContainsKey([IO.Path]::GetFullPath($actual).ToLowerInvariant())) {
                $process
            }
        } catch {
            # Processes owned by another identity may hide their executable path.
        }
    }
}

function Stop-ProcessesAtExactPath {
    param([string[]]$Paths)
    foreach ($process in @(Get-ProcessesAtExactPath -Paths $Paths)) {
        Stop-Process -Id $process.Id -Force -ErrorAction Stop
        try { $process.WaitForExit(5000) | Out-Null } catch { }
    }
}

function Stop-UserTaskProcess {
    param(
        [string]$Name = $TaskName,
        [string]$Path = "\",
        [string]$ProcessIdFile = $PidFile,
        [string]$ExpectedBinary = $BinPath
    )

    if (Get-Command Stop-ScheduledTask -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskPath $Path -TaskName $Name -ErrorAction SilentlyContinue
    }
    Start-Sleep -Milliseconds 250

    if (Test-Path -LiteralPath $ProcessIdFile) {
        $recordedPid = 0
        [void][int]::TryParse(
            ([IO.File]::ReadAllText($ProcessIdFile).Trim()),
            [ref]$recordedPid
        )
        if ($recordedPid -gt 0) {
            $process = Get-Process -Id $recordedPid -ErrorAction SilentlyContinue
            if ($process) {
                $actualPath = $null
                try { $actualPath = $process.Path } catch { }
                if (-not (Test-SamePath -Left $actualPath -Right $ExpectedBinary)) {
                    Write-Warn "Ignoring stale PID file; process $recordedPid is not $ExpectedBinary"
                } else {
                    Stop-Process -Id $recordedPid -Force -ErrorAction Stop
                    try { $process.WaitForExit(5000) | Out-Null } catch { }
                }
            }
        }
        Remove-Item -Force -LiteralPath $ProcessIdFile -ErrorAction SilentlyContinue
    }

    # Safely adopt user installs created by older launchers that did not write a PID file.
    Stop-ProcessesAtExactPath -Paths @($ExpectedBinary)
}

function New-UserLauncherContent {
    param(
        [string]$InstallRoot,
        [string]$ConfigPath,
        [string]$ServerPath,
        [string]$ProcessIdFile,
        [string]$StdoutPath,
        [string]$StderrPath
    )

    $template = @'
$ErrorActionPreference = "Stop"
$installDir = '__INSTALL_ROOT__'
$configPath = '__CONFIG_PATH__'
$serverPath = '__SERVER_PATH__'
$pidFile = '__PID_FILE__'
$stdoutPath = '__STDOUT_PATH__'
$stderrPath = '__STDERR_PATH__'

foreach ($line in [IO.File]::ReadAllLines($configPath)) {
    if ($line -match '^[A-Za-z_][A-Za-z0-9_]*=') {
        $separator = $line.IndexOf('=')
        $name = $line.Substring(0, $separator)
        $value = $line.Substring($separator + 1)
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}

$child = $null
$exitCode = 1
try {
    $child = Start-Process -FilePath $serverPath -WorkingDirectory $installDir -WindowStyle Hidden -PassThru -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath
    [void]$child.Handle
    [IO.File]::WriteAllText($pidFile, [string]$child.Id, [Text.Encoding]::ASCII)
    $child.WaitForExit()
    $child.Refresh()
    $exitCode = $child.ExitCode
} finally {
    if (Test-Path -LiteralPath $pidFile) {
        $recorded = [IO.File]::ReadAllText($pidFile).Trim()
        if ($child -and $recorded -eq [string]$child.Id) {
            Remove-Item -Force -LiteralPath $pidFile -ErrorAction SilentlyContinue
        }
    }
}
[Environment]::Exit($exitCode)
'@

    $values = @{
        "__INSTALL_ROOT__" = $InstallRoot
        "__CONFIG_PATH__" = $ConfigPath
        "__SERVER_PATH__" = $ServerPath
        "__PID_FILE__" = $ProcessIdFile
        "__STDOUT_PATH__" = $StdoutPath
        "__STDERR_PATH__" = $StderrPath
    }
    foreach ($entry in $values.GetEnumerator()) {
        $template = $template.Replace($entry.Key, $entry.Value.Replace("'", "''"))
    }
    return $template
}

# ============================================================================
# Helper: validate binary looks real
# ============================================================================
function Test-ValidBinary {
    param([string]$Path)
    if (-not (Test-Path $Path)) { return $false }
    $size = (Get-Item $Path).Length
    # A valid Go binary should be at least 1 MB
    return $size -gt 1048576
}

# ============================================================================
# Step 2 - Uninstall (if requested)
# ============================================================================
if ($Uninstall) {
    Write-Step $Step $TotalSteps "Uninstalling Conch ($Mode mode)..."

    if ($Mode -eq "user" -and -not (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue)) {
        throw "The ScheduledTasks PowerShell module is required to uninstall user mode safely."
    }

    $existingSvc = if ($Mode -eq "system") {
        Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    } else {
        $null
    }
    $existingTask = if ($Mode -eq "user") {
        Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
    } else {
        $null
    }
    $existingDir = Test-Path -LiteralPath $InstallDir

    if (-not $existingSvc -and -not $existingTask -and -not $existingDir) {
        Write-Warn "No $Mode-mode Conch installation found."
        return
    }

    if (-not $Yes) {
        if (-not (Prompt-User "Remove the $Mode-mode Conch installation and its files?" -Default "Y")) {
            Write-Info "Aborted by user."
            return
        }
    }

    if ($Mode -eq "system" -and $existingSvc) {
        Write-Info "Stopping system service..."
        if (-not (Stop-ServiceWait -Name $ServiceName)) {
            throw "Service '$ServiceName' did not stop cleanly."
        }
        $removeNssm = Find-Nssm
        if ($removeNssm) {
            Invoke-Nssm -Path $removeNssm.Source -Arguments @("remove", $ServiceName, "confirm")
        } else {
            & sc.exe delete $ServiceName | Out-Null
            if ($LASTEXITCODE -ne 0) {
                throw "Failed to delete system service '$ServiceName' (exit code $LASTEXITCODE)."
            }
        }
        Write-OK "System service removed: $ServiceName"
    }

    if ($Mode -eq "user" -and $existingTask) {
        Write-Info "Stopping user background task..."
        Stop-UserTaskProcess
        Unregister-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -Confirm:$false -ErrorAction Stop
        Write-OK "User background task removed: $TaskName"
    }

    Stop-ProcessesAtExactPath -Paths @($BinPath, $McpBinPath)

    if ($existingDir) {
        if (-not (Remove-Safe $InstallDir "$Mode-mode install directory")) {
            throw "Failed to remove $InstallDir"
        }
        Write-OK "Removed: $InstallDir"
    }

    $Step++
    Write-Step $Step $TotalSteps "Done."
    Write-Host ""
    Write-OK "The $Mode-mode Conch installation has been uninstalled."
    Write-Host ""
    return
}

# ============================================================================
# Step 2 - Detect & handle existing installation
# ============================================================================
$allSystemSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
$allUserTask = $null
if (Get-Command Get-ScheduledTask -ErrorAction SilentlyContinue) {
    $allUserTask = Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
}

$systemInstallDir = "$env:ProgramFiles\Conch"
$userInstallDir = "$env:LOCALAPPDATA\Conch"
$systemModePresent = [bool]($allSystemSvc -or (Test-Path -LiteralPath $systemInstallDir))
$userModePresent = [bool]($allUserTask -or (Test-Path -LiteralPath $userInstallDir))

if ($Mode -eq "user" -and $systemModePresent) {
    throw "A system-mode Conch installation already exists. Refusing an implicit mode migration; uninstall it explicitly with -Uninstall -Mode system first."
}
if ($Mode -eq "system" -and $userModePresent) {
    throw "A user-mode Conch installation already exists. Refusing an implicit mode migration; uninstall it explicitly with -Uninstall -Mode user first."
}

$existingSvc = if ($Mode -eq "system") { $allSystemSvc } else { $null }
$existingTask = if ($Mode -eq "user") { $allUserTask } else { $null }
if ($existingSvc) {
    $existingRegistration = Get-ItemProperty -LiteralPath $NssmParametersPath -ErrorAction Stop
    if (-not $existingRegistration.Application -or
        -not (Test-SamePath -Left $existingRegistration.Application -Right $BinPath)) {
        throw "The existing Conch service is registered to a different application. Refusing an implicit service or prefix migration."
    }
    if ($existingRegistration.AppDirectory -and
        -not (Test-SamePath -Left $existingRegistration.AppDirectory -Right $InstallDir)) {
        throw "The existing Conch service uses a different working directory. Refusing an implicit prefix migration."
    }
}
$existingDir = Test-Path -LiteralPath $InstallDir
$ServiceWasRunning = [bool]($existingSvc -and $existingSvc.Status -eq "Running")
$TaskWasRunning = [bool]($existingTask -and $existingTask.State -eq "Running")
if ($existingTask -and @(Get-ProcessesAtExactPath -Paths @($BinPath)).Count -gt 0) {
    $TaskWasRunning = $true
}
$IsUpgrade = [bool]($existingSvc -or $existingTask -or $existingDir)

if ($IsUpgrade) {
    Write-Step $Step $TotalSteps "Existing $Mode-mode installation detected"
    if ($existingSvc)  { Write-Warn "Service:  $ServiceName ($($existingSvc.Status))" }
    if ($existingTask) { Write-Warn "Task:     $TaskName ($($existingTask.State))" }
    if ($existingDir)  { Write-Warn "Location: $InstallDir" }
    Write-Info "Performing an in-place upgrade; configuration and durable job state will be preserved."
}

$Step++

# ============================================================================
# Step 3 - Acquire binary
# ============================================================================
Write-Step $Step $TotalSteps "Acquiring binaries..."

function Download-Url {
    param([string]$Url, [string]$Dest, [string]$Description)
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    Retry-Command -Script {
        $client = New-Object System.Net.WebClient
        try {
            $client.Headers.Add("User-Agent", "Conch-Installer/$Version")
            $client.DownloadFile($Url, $Dest)
        } finally {
            $client.Dispose()
        }
        if (-not (Test-Path $Dest)) { throw "Download completed but file not found" }
    } -MaxAttempts 3 -DelaySeconds 3 -Description $Description
}

function Get-ExpectedReleaseHash {
    param([string]$Name, [switch]$Refresh)
    if ($Refresh -and (Test-Path -LiteralPath $ChecksumManifest)) {
        Remove-Item -LiteralPath $ChecksumManifest -Force -ErrorAction SilentlyContinue
    }
    if (-not (Test-Path -LiteralPath $ChecksumManifest)) {
        Write-Info "Downloading signed-release checksum manifest..."
        Download-Url "$GitHubReleases/checksums.txt" $ChecksumManifest "download checksums.txt"
    }
    $pattern = "^([A-Fa-f0-9]{64})\s+\*?$([Regex]::Escape($Name))$"
    foreach ($line in Get-Content -LiteralPath $ChecksumManifest) {
        if ($line -match $pattern) { return $Matches[1].ToLowerInvariant() }
    }
    throw "checksums.txt does not contain $Name"
}

function Download-File {
    param([string]$Name, [string]$Dest)
    $url = "$GitHubReleases/$Name"
    Write-Info "Downloading $Name from release $Version..."
    # The first attempt trusts a cached checksum manifest; a second attempt
    # refreshes it. A stale manifest (release assets replaced under the same
    # version string) must never strand an install on old hashes.
    foreach ($refresh in @($false, $true)) {
        try {
            $expected = Get-ExpectedReleaseHash $Name -Refresh:$refresh
            Download-Url $url $Dest "download $Name"
            $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $Dest).Hash.ToLowerInvariant()
            if ($actual -ne $expected) {
                throw "SHA-256 mismatch for $Name (expected $expected, got $actual)"
            }
            Write-OK "Verified SHA-256: $Name"
            return $true
        } catch {
            Write-Warn "Verified download failed: $($_.Exception.Message.Split([Environment]::NewLine)[0])"
            if (Test-Path -LiteralPath $Dest) { Remove-Item -LiteralPath $Dest -Force -ErrorAction SilentlyContinue }
        }
    }
    return $false
}

$SrcBin = $null
$ServerBinName = "conch-windows-amd64.exe"
$McpBinName    = "conch-mcp-windows-amd64.exe"

if ($BinaryPath) {
    if (-not (Test-Path $BinaryPath)) {
        Write-ErrorExit "Binary not found: $BinaryPath"
    }
    $SrcBin = Resolve-Path $BinaryPath
    if (-not (Test-ValidBinary $SrcBin)) {
        Write-Warn "Binary at $SrcBin seems small - it may not be a valid executable"
        if (-not $Yes -and -not (Prompt-User "Use this binary anyway?" -Default "N")) {
            Write-ErrorExit "Aborted. Provide a valid binary with -BinaryPath."
        }
    }
    Write-OK "Using provided binary: $SrcBin"

} elseif (Download-File $ServerBinName "$RepoDir\conch.exe") {
    $SrcBin = "$RepoDir\conch.exe"
    if (-not (Test-ValidBinary $SrcBin)) {
        Write-Warn "Downloaded file appears invalid. Trying alternatives..."
        Remove-Item $SrcBin -Force -ErrorAction SilentlyContinue
        $SrcBin = $null
    }
}

if (-not $SrcBin) {
    Write-Warn "GitHub download failed or produced invalid file, trying alternatives..."

    $GoBin = Get-Command go -ErrorAction SilentlyContinue
    if ($GoBin -and (Test-Path "$RepoDir\go.mod")) {
        Write-Info "Building from source (go build)..."
        Push-Location $RepoDir
        try {
            & go build -o conch.exe .
            if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
            $SrcBin = "$RepoDir\conch.exe"
            Write-OK "Built from source"
        } catch {
            Write-Warn "Build failed: $($_.Exception.Message)"
        } finally { Pop-Location }
    }

    if (-not $SrcBin -and (Test-Path "$RepoDir\conch.exe")) {
        $SrcBin = "$RepoDir\conch.exe"
        Write-OK "Using local conch.exe"
    }

    if (-not $SrcBin) {
        $pathBin = Get-Command conch -ErrorAction SilentlyContinue
        if ($pathBin) {
            $SrcBin = $pathBin.Source
            Write-OK "Using conch from PATH: $SrcBin"
        }
    }

    if (-not $SrcBin) {
        Write-ErrorExit "Could not acquire binary. Download from: https://github.com/newo-ether/conch/releases/latest`n  Then retry with: .\install.ps1 -BinaryPath <path-to-conch.exe>"
    }
}

if (-not (Test-ValidBinary $SrcBin)) {
    Write-Warn "Binary at $SrcBin is smaller than expected ($((Get-Item $SrcBin).Length) bytes)"
    Write-Warn "  Installation may succeed but the server might not work."
}

# --- MCP binary acquisition (same step) ---
$SrcMcp = $null
if ($McpBinaryPath) {
    if (-not (Test-Path $McpBinaryPath)) {
        Write-Warn "MCP binary not found: $McpBinaryPath - skipping conch-mcp"
    } else {
        $SrcMcp = Resolve-Path $McpBinaryPath
        Write-OK "Using provided MCP binary: $SrcMcp"
    }
} elseif (Download-File $McpBinName "$RepoDir\conch-mcp.exe") {
    $SrcMcp = "$RepoDir\conch-mcp.exe"
} else {
    $GoBin = Get-Command go -ErrorAction SilentlyContinue
    if ($GoBin -and (Test-Path "$RepoDir\go.mod")) {
        Write-Info "Building conch-mcp from source..."
        Push-Location $RepoDir
        try {
            & go build -o conch-mcp.exe ./cmd/mcp
            if ($LASTEXITCODE -ne 0) { throw "go build mcp failed with exit code $LASTEXITCODE" }
            $SrcMcp = "$RepoDir\conch-mcp.exe"
            Write-OK "MCP built from source"
        } catch {
            Write-Warn "Failed to build conch-mcp: $($_.Exception.Message)"
        } finally { Pop-Location }
    } elseif (Test-Path "$RepoDir\conch-mcp.exe") {
        $SrcMcp = "$RepoDir\conch-mcp.exe"
    } elseif (Get-Command conch-mcp -ErrorAction SilentlyContinue) {
        $SrcMcp = (Get-Command conch-mcp).Source
    }
}
if (-not $SrcMcp) {
    Write-Warn "conch-mcp not available - MCP bridge will not be installed"
}

function Assert-BinaryVersion {
    param([string]$Path, [string]$Label)
    $reported = & $Path --version 2>&1 | Out-String
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($reported)) {
        throw "$Label did not return version metadata"
    }
    if ($Version -ne "latest" -and $reported -notmatch [Regex]::Escape($Version)) {
        throw "$Label reports '$($reported.Trim())', requested $Version"
    }
    Write-OK "$Label version: $($reported.Trim())"
}
Assert-BinaryVersion $SrcBin "conch"
if ($SrcMcp) { Assert-BinaryVersion $SrcMcp "conch-mcp" }

$Step++

# ============================================================================
# Step 4 - Install files
# ============================================================================
Write-Step $Step $TotalSteps "Installing files..."

# Delay the first upgrade side effect until release binaries have been acquired and verified.
if ($Mode -eq "system" -and $existingSvc) {
    Push-Rollback {
        if ($ServiceWasRunning) {
            Start-ServiceWait -Name $ServiceName -TimeoutSec 15 | Out-Null
        }
    } "Restore previous service running state"
    if (-not (Stop-ServiceWait -Name $ServiceName)) {
        throw "Service '$ServiceName' did not stop cleanly; refusing to replace its binary."
    }
} elseif ($Mode -eq "user" -and $IsUpgrade) {
    Push-Rollback {
        if ($TaskWasRunning -and (Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue)) {
            Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
        }
    } "Restore previous user task running state"
    Stop-UserTaskProcess
}

# Create install directory with rollback registration
if (-not (Test-Path $InstallDir)) {
    Retry-Command -Script {
        New-Item -ItemType Directory -Force -Path $InstallDir -ErrorAction Stop | Out-Null
    } -MaxAttempts 3 -DelaySeconds 1 -Description "creating install directory"
    Push-Rollback { Remove-Item -Recurse -Force $InstallDir -ErrorAction SilentlyContinue } "Remove created directory: $InstallDir"
    Write-OK "Created: $InstallDir"
}

function Copy-IfDifferent {
    param([string]$Source, [string]$Dest, [string]$Label)
    $srcPath = (Resolve-Path $Source).Path
    $dstPath = $Dest
    if (Test-Path $Dest) {
        $dstPath = (Resolve-Path $Dest).Path
    }
    if ($srcPath -eq $dstPath) {
        Write-OK "$Label already in place (same file)"
        return
    }

    # Stage first, then swap paths. A running MCP executable can keep using its
    # renamed image while new invocations immediately resolve to the new binary.
    $swapID = [Guid]::NewGuid().ToString("N")
    $staged = "$Dest.new.$swapID"
    $retired = "$Dest.retired.$swapID"
    Copy-Item -Force -LiteralPath $Source -Destination $staged -ErrorAction Stop
    try {
        if (Test-Path $Dest) {
            Move-Item -Force -LiteralPath $Dest -Destination $retired -ErrorAction Stop
        }
        Move-Item -Force -LiteralPath $staged -Destination $Dest -ErrorAction Stop
    } catch {
        if (-not (Test-Path $Dest) -and (Test-Path $retired)) {
            Move-Item -Force -LiteralPath $retired -Destination $Dest -ErrorAction SilentlyContinue
        }
        throw
    } finally {
        Remove-Item -Force -LiteralPath $staged -ErrorAction SilentlyContinue
        Remove-Item -Force -LiteralPath $retired -ErrorAction SilentlyContinue
    }
    Write-OK "$Label installed"
}

$ServerBackup = "$BinPath.previous"
$McpBackup = "$McpBinPath.previous"
$ServerExisted = Test-Path -LiteralPath $BinPath
$McpExisted = Test-Path -LiteralPath $McpBinPath
if ($ServerExisted) {
    Copy-Item -Force -LiteralPath $BinPath -Destination $ServerBackup
    Push-Rollback {
        if (Test-Path $ServerBackup) {
            if ($Mode -eq "system") {
                Stop-ServiceWait -Name $ServiceName | Out-Null
            } else {
                Stop-UserTaskProcess
            }
            Copy-Item -Force -LiteralPath $ServerBackup -Destination $BinPath
            if ($existingSvc) {
                $rollbackNssm = Find-Nssm
                if ($rollbackNssm -and (Test-Path $EnvFile)) {
                    $rollbackEnv = @(
                        Get-Content -LiteralPath $EnvFile |
                            Where-Object { $_ -match '^[A-Za-z_][A-Za-z0-9_]*=' }
                    )
                    Invoke-Nssm -Path $rollbackNssm.Source -Arguments (@("set", $ServiceName, "AppEnvironmentExtra") + $rollbackEnv) -AllowFailure
                }
            }
        }
    } "Restore previous conch.exe and registration"
}
if ($McpExisted) {
    Copy-Item -Force -LiteralPath $McpBinPath -Destination $McpBackup
    Push-Rollback {
        if (Test-Path $McpBackup) {
            Copy-Item -Force -LiteralPath $McpBackup -Destination $McpBinPath
        }
    } "Restore previous conch-mcp.exe"
}

Copy-IfDifferent $SrcBin $BinPath "conch.exe"
if (-not $ServerExisted) {
    Push-Rollback {
        Remove-Item -Force -LiteralPath $BinPath -ErrorAction SilentlyContinue
    } "Remove newly installed conch.exe"
}
if ($SrcMcp) {
    Copy-IfDifferent $SrcMcp $McpBinPath "conch-mcp.exe"
    if (-not $McpExisted) {
        Push-Rollback {
            Remove-Item -Force -LiteralPath $McpBinPath -ErrorAction SilentlyContinue
        } "Remove newly installed conch-mcp.exe"
    }
}

$Step++

# ============================================================================
# Step 5 - Configuration
# ============================================================================
Write-Step $Step $TotalSteps "Configuring..."

# Preserve every existing setting by default; only explicitly supplied parameters are changed.
function Set-EnvValue {
    param(
        [System.Collections.Generic.List[string]]$Lines,
        [string]$Name,
        [string]$Value
    )
    for ($i = 0; $i -lt $Lines.Count; $i++) {
        if ($Lines[$i] -match "^$([Regex]::Escape($Name))=") {
            $Lines[$i] = "$Name=$Value"
            return
        }
    }
    $Lines.Add("$Name=$Value")
}

$EnvBackup = "$EnvFile.previous"
foreach ($existingSecret in @(Get-ChildItem -LiteralPath $InstallDir -File -Force |
        Where-Object { $_.Name -eq 'env.txt' -or $_.Name -like 'env.txt.*' })) {
    Protect-SecretFile $existingSecret.FullName
}
if (Test-Path $EnvFile) {
    Write-AtomicConfig ([IO.File]::ReadAllText($EnvFile)) $EnvBackup
    Push-Rollback {
        Copy-Item -Force -LiteralPath $EnvBackup -Destination $EnvFile -ErrorAction SilentlyContinue
    } "Restore previous configuration"
} else {
    Push-Rollback {
        Remove-Item -Force -LiteralPath $EnvFile -ErrorAction SilentlyContinue
    } "Remove newly created configuration"
}

$configLines = [System.Collections.Generic.List[string]]::new()
if (Test-Path $EnvFile) {
    foreach ($line in Get-Content -LiteralPath $EnvFile) { $configLines.Add($line) }
    Write-OK "Preserving existing configuration and durable job settings"
} else {
    $configLines.Add("CONCH_PORT=$Port")
    $configLines.Add("CONCH_HOST=$HostAddr")
    $configLines.Add("CONCH_TIMEOUT=$TimeoutSec")
    $configLines.Add("CONCH_MAX_TIMEOUT=$MaxTimeoutSec")
    $configLines.Add("CONCH_ALLOW_NO_AUTH=$($NoAuth.ToString().ToLowerInvariant())")
}

if ($PSBoundParameters.ContainsKey("Port")) {
    Set-EnvValue $configLines "CONCH_PORT" $Port
}
if ($PSBoundParameters.ContainsKey("HostAddr")) {
    Set-EnvValue $configLines "CONCH_HOST" $HostAddr
}
if ($PSBoundParameters.ContainsKey("TimeoutSec")) {
    Set-EnvValue $configLines "CONCH_TIMEOUT" $TimeoutSec
}
if ($PSBoundParameters.ContainsKey("MaxTimeoutSec")) {
    Set-EnvValue $configLines "CONCH_MAX_TIMEOUT" $MaxTimeoutSec
}
if ($PSBoundParameters.ContainsKey("NoAuth")) {
    Set-EnvValue $configLines "CONCH_ALLOW_NO_AUTH" $NoAuth.ToString().ToLowerInvariant()
}

if (-not $ApiKey) {
    foreach ($line in $configLines) {
        if ($line -match "^CONCH_API_KEY=(.+)$") {
            $ApiKey = $Matches[1].Trim()
            break
        }
    }
}
if (-not $ApiKey) {
    $bytes = New-Object byte[] 32
    (New-Object Security.Cryptography.RNGCryptoServiceProvider).GetBytes($bytes)
    $ApiKey = [Convert]::ToBase64String($bytes).TrimEnd("=")
    Write-OK "Generated new API key"
}
if ($PSBoundParameters.ContainsKey("ApiKey") -or -not ($configLines -match "^CONCH_API_KEY=")) {
    Set-EnvValue $configLines "CONCH_API_KEY" $ApiKey
}

$configContent = [string]::Join([Environment]::NewLine, $configLines)
Write-AtomicConfig $configContent $EnvFile
Write-OK "Config written atomically: $EnvFile"

foreach ($line in $configLines) {
    if ($line -match "^CONCH_PORT=(\d+)$") { $Port = [int]$Matches[1] }
    if ($line -eq "CONCH_ALLOW_NO_AUTH=true") { $NoAuth = $true }
}
if ($NoAuth) {
    Write-Warn "Authentication is DISABLED - do not expose to untrusted networks!"
}

$Step++

# ============================================================================
# Step 6 - Register & start (system service or user background task)
# ============================================================================
if ($Mode -eq "system") {
    Write-Step $Step $TotalSteps "Registering service..."
} else {
    Write-Step $Step $TotalSteps "Registering background task..."
}

if ($Mode -eq "system") {
    # nssm was already acquired in Step 1; verify it still resolves
    if (-not $nssm) {
        $nssm = Get-Command nssm -ErrorAction SilentlyContinue
        if (-not $nssm) {
            throw "nssm lost after install. Please re-run the script."
        }
    }
    Write-OK "nssm ready: $($nssm.Source)"
    $nssmExe = $nssm.Source

    # Update an existing service in place. Service identity, start type, display
    # name and recovery policy belong to the existing deployment and are not
    # rewritten during an upgrade.
    $existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($existingSvc) {
        Write-Info "Updating existing service registration in place..."
        Stop-ServiceWait -Name $ServiceName | Out-Null
        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "Application", $BinPath)
        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "AppDirectory", $InstallDir)
    } else {
        Write-Info "Creating service..."
        Invoke-Nssm -Path $nssmExe -Arguments @("install", $ServiceName, $BinPath)
        Push-Rollback {
            Invoke-Nssm -Path $nssmExe -Arguments @("remove", $ServiceName, "confirm") -AllowFailure
        } "Remove created service: $ServiceName"

        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "AppDirectory", $InstallDir)
        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "Start", "SERVICE_AUTO_START")
        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "ObjectName", "NT AUTHORITY\SYSTEM")
        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "DisplayName", "Conch Shell Server")
        Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "AppExit", "Default", "Restart")
    }

    # Environment variables
    Protect-ServiceSecrets $NssmParametersPath
    $envLines = @(
        Get-Content -LiteralPath $EnvFile |
            Where-Object { $_ -match '^[A-Za-z_][A-Za-z0-9_]*=' }
    )
    $environmentArguments = @("set", $ServiceName, "AppEnvironmentExtra") + $envLines
    Invoke-Nssm -Path $nssmExe -Arguments $environmentArguments

    Write-OK "Service registered: $ServiceName (auto-start, auto-restart on failure)"

} else {
    # ========================================================================
    # User mode: synchronous PowerShell launcher + logon scheduled task
    # ========================================================================

    foreach ($requiredCommand in @(
        "Get-ScheduledTask",
        "Export-ScheduledTask",
        "Register-ScheduledTask",
        "Unregister-ScheduledTask",
        "New-ScheduledTaskAction",
        "New-ScheduledTaskTrigger",
        "New-ScheduledTaskPrincipal",
        "New-ScheduledTaskSettingsSet"
    )) {
        if (-not (Get-Command $requiredCommand -ErrorAction SilentlyContinue)) {
            throw "The ScheduledTasks PowerShell module is missing required command: $requiredCommand"
        }
    }

    $launchBackup = "$LaunchScript.previous"
    if (Test-Path -LiteralPath $LaunchScript) {
        Copy-Item -Force -LiteralPath $LaunchScript -Destination $launchBackup
        Push-Rollback {
            Copy-Item -Force -LiteralPath $launchBackup -Destination $LaunchScript -ErrorAction SilentlyContinue
        } "Restore previous user launcher"
    } else {
        Push-Rollback {
            Remove-Item -Force -LiteralPath $LaunchScript -ErrorAction SilentlyContinue
        } "Remove newly created user launcher"
    }

    $launch = New-UserLauncherContent -InstallRoot $InstallDir -ConfigPath $EnvFile -ServerPath $BinPath -ProcessIdFile $PidFile -StdoutPath $LogFile -StderrPath $ErrorLogFile
    [IO.File]::WriteAllText($LaunchScript, $launch, (New-Object Text.UTF8Encoding($false)))
    Write-OK "Synchronous launcher written: $LaunchScript"

    $currentUser = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    $taskAction = New-ScheduledTaskAction -Execute "powershell.exe" -Argument (
        '-NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "' +
        $LaunchScript + '"'
    )
    $taskTrigger = New-ScheduledTaskTrigger -AtLogOn -User $currentUser
    $taskPrincipal = New-ScheduledTaskPrincipal -UserId $currentUser -LogonType Interactive -RunLevel Limited
    $taskSettings = New-ScheduledTaskSettingsSet `
        -RestartCount 999 `
        -RestartInterval (New-TimeSpan -Minutes 1) `
        -ExecutionTimeLimit ([TimeSpan]::Zero) `
        -MultipleInstances IgnoreNew `
        -AllowStartIfOnBatteries `
        -DontStopIfGoingOnBatteries

    $taskBeforeRegistration = Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue
    if ($taskBeforeRegistration) {
        $taskBackupXml = Export-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName
        Push-Rollback {
            Stop-UserTaskProcess
            Register-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -Xml $taskBackupXml -Force | Out-Null
        } "Restore previous scheduled task definition"
    } else {
        Push-Rollback {
            Stop-UserTaskProcess
            Unregister-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue
        } "Remove created scheduled task: $TaskName"
    }

    Register-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName `
        -Action $taskAction `
        -Trigger $taskTrigger `
        -Principal $taskPrincipal `
        -Settings $taskSettings `
        -Description "Conch Shell Server (user mode, runs as $currentUser)" `
        -Force | Out-Null

    Write-OK "Scheduled task registered: $TaskName (owns the server process and restarts on failure)"
}

# Start now
$DoStart = -not $NoStart
if (-not $NoStart -and -not $Yes) {
    Write-Host ""
    $DoStart = Prompt-User "Start Conch now?" -Default "Y"
}

if ($DoStart) {
    if ($Mode -eq "system") {
        Write-Info "Starting service..."
        $started = Start-ServiceWait -Name $ServiceName -TimeoutSec 15
        if (-not $started) {
            Write-ErrorExit "Service '$ServiceName' did not reach Running state within 15 seconds."
        }
        Write-OK "Service started"
    } else {
        Write-Info "Starting user background task..."
        Stop-UserTaskProcess
        Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction Stop

        $ownedProcessReady = $false
        for ($attempt = 0; $attempt -lt 20; $attempt++) {
            Start-Sleep -Milliseconds 250
            if (Test-Path -LiteralPath $PidFile) {
                $ownedPid = 0
                [void][int]::TryParse(([IO.File]::ReadAllText($PidFile).Trim()), [ref]$ownedPid)
                $ownedProcess = if ($ownedPid -gt 0) {
                    Get-Process -Id $ownedPid -ErrorAction SilentlyContinue
                } else {
                    $null
                }
                if ($ownedProcess) {
                    $ownedPath = $null
                    try { $ownedPath = $ownedProcess.Path } catch { }
                    if (Test-SamePath -Left $ownedPath -Right $BinPath) {
                        $ownedProcessReady = $true
                        break
                    }
                }
            }
        }
        if (-not $ownedProcessReady) {
            throw "The user task did not start an owned Conch process."
        }
        Write-OK "User task started and owns process $ownedPid"
    }

    # Quick health check
    try {
        Start-Sleep -Seconds 1
        $health = Invoke-RestMethod -Uri "http://localhost:$Port/health" -TimeoutSec 5 -ErrorAction SilentlyContinue
        if (-not $health -or $health.status -ne "ok" -or -not $health.version) {
            throw "health response is missing status/version"
        }
        if ($Version -ne "latest" -and $health.version -ne $Version) {
            throw "installed version $($health.version) does not match requested $Version"
        }
        Write-OK "Health/version check passed: $($health.version)"
    } catch {
        Write-ErrorExit "Health/version verification failed: $($_.Exception.Message)"
    }
} else {
    Write-Info "Conch installed but not started. Start manually:"
    if ($Mode -eq "system") {
        Write-Info "  Start-Service -Name $ServiceName"
    } else {
        Write-Info "  Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName"
    }
}

# ============================================================================
# Done
# ============================================================================
Write-Host ""
$boxW = 46; $t = "Installation Complete"
Write-Host "  +----------------------------------------------+" -ForegroundColor Green
Write-Host ("  |  ${Bold}{0}${Reset}{1}|" -f $t, (' ' * ($boxW - 2 - $t.Length))) -ForegroundColor Green
Write-Host "  +----------------------------------------------+" -ForegroundColor Green
Write-Host ""
Write-Host "  ${Cyan}Health check:${Reset}   curl.exe -s http://localhost:$Port/health"
Write-Host "  ${Cyan}API key:${Reset}       stored in protected config (not printed)"
Write-Host "  ${Cyan}Config file:${Reset}   $EnvFile"
Write-Host ""
Write-Host "  ${Cyan}Manage:${Reset}"
if ($Mode -eq "system") {
    Write-Host "    Stop:        nssm stop $ServiceName"
    Write-Host "    Start:       nssm start $ServiceName"
    Write-Host "    Status:      nssm status $ServiceName"
    Write-Host "    Uninstall:   .\install.ps1 -Uninstall -Mode system"
    Write-Host ""
    Write-Host "  ${Yellow}Change API key:${Reset} (editing env.txt alone is NOT enough on Windows)"
    Write-Host "    1. Edit config:  notepad $EnvFile"
    Write-Host "    2. Reload env:   nssm set $ServiceName AppEnvironmentExtra (Get-Content $EnvFile)"
    Write-Host "    3. Restart:      nssm restart $ServiceName"
} else {
    Write-Host "    Stop:        Stop-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName"
    Write-Host "    Start:       Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName"
    Write-Host "    Task:        Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName"
    Write-Host "    Stdout log:  $LogFile"
    Write-Host "    Stderr log:  $ErrorLogFile"
    Write-Host "    Uninstall:   .\install.ps1 -Uninstall -Mode user"
    Write-Host ""
    Write-Host "  ${Yellow}Change API key:${Reset}"
    Write-Host "    1. Edit config:  notepad $EnvFile"
    Write-Host "    2. Restart:      Stop-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName; Start-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName"
}
Write-Host ""

} catch {
    Write-Host ""
    Write-Err "FATAL: $($_.Exception.Message)"
    Invoke-Rollback
    Write-Host ""
    Write-Err "Installation failed. The system has been restored to its previous state."
    Write-Host "  For manual installation help: https://github.com/newo-ether/conch"
    Write-Host ""
    throw
}

}
