<#
.SYNOPSIS
    Install Conch Shell Server as a Windows service.

.DESCRIPTION
    Downloads (or builds) the conch binary, generates an API key, and registers
    a Windows service via nssm. Interactive by default - use -Yes for scripting.

.PARAMETER ApiKey
    Pre-shared API key. If omitted, a random 32-byte key is generated.

.PARAMETER Port
    Listen port. Default: 14216.

.PARAMETER HostAddr
    Listen address. Default: 0.0.0.0.

.PARAMETER TimeoutSec
    Default command timeout in seconds. Default: 30.

.PARAMETER MaxTimeoutSec
    Maximum command timeout in seconds. Default: 120.

.PARAMETER NoAuth
    Disable authentication. Insecure, dev only.

.PARAMETER BinaryPath
    Path to a pre-built conch.exe. Skips download / Go build.

.PARAMETER McpBinaryPath
    Path to a pre-built conch-mcp.exe. Skips download / Go build.

.PARAMETER Prefix
    Install root directory. Default: $env:ProgramFiles\Conch.

.PARAMETER NoStart
    Install but do not start the service.

.PARAMETER Yes
    Skip all prompts, accept all defaults. Useful for scripting.

.PARAMETER Uninstall
    Remove the service, binary, and config.

.EXAMPLE
    # Interactive install (random key, download from GitHub Releases)
    .\install.ps1

.EXAMPLE
    # Non-interactive (scripting / CI)
    .\install.ps1 -Yes

.EXAMPLE
    # Custom port and key
    .\install.ps1 -Port 8080 -ApiKey "my-secret-key"

.EXAMPLE
    # Custom install location
    .\install.ps1 -Prefix "D:\Conch"

.EXAMPLE
    # Uninstall
    .\install.ps1 -Uninstall
#>

param(
    [string]$ApiKey        = "",
    [ValidateRange(1, 65535)]
    [int]   $Port          = 14216,
    [string]$HostAddr      = "0.0.0.0",
    [ValidateRange(1, 604800)]
    [int]   $TimeoutSec    = 30,
    [ValidateRange(1, 604800)]
    [int]   $MaxTimeoutSec = 120,
    [switch]$NoAuth        = $false,
    [string]$BinaryPath    = "",
    [string]$McpBinaryPath = "",
    [ValidatePattern('^(latest|v[0-9]+\.[0-9]+\.[0-9]+)$')]
    [string]$Version       = "latest",
    [string]$Prefix        = "",
    [switch]$NoStart       = $false,
    [switch]$Yes           = $false,
    [switch]$Uninstall     = $false
)

& {

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

if (-not $isAdmin) {
    Write-Err "Administrator privileges required."
    Write-Host "    Right-click PowerShell -> Run as Administrator, then re-run."
    throw "Administrator privileges required."
}
Write-OK "Administrator"

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

# ============================================================================
# Constants
# ============================================================================
$ServiceName = "Conch"
$InstallDir  = if ($Prefix) { $Prefix } else { "$env:ProgramFiles\Conch" }
$BinPath     = "$InstallDir\conch.exe"
$McpBinPath  = "$InstallDir\conch-mcp.exe"
$EnvFile     = "$InstallDir\env.txt"
$EnvFileTmp  = "$InstallDir\env.txt.tmp"

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
    Write-Warn "Service '$Name' did not stop within timeout - forcing..."
    Get-Process -Name $Name -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
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
function Write-AtomicConfig {
    param([string]$Content, [string]$Target)
    $swapID = [Guid]::NewGuid().ToString("N")
    $tmp = "$Target.tmp.$swapID"
    $backup = "$Target.swap-backup.$swapID"
    $utf8 = New-Object Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($tmp, $Content, $utf8)
    try {
        if (Test-Path -LiteralPath $Target) {
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
    if (-not (Test-Path $Path)) { return }
    try {
        Retry-Command -Script {
            Remove-Item -Recurse -Force $Path -ErrorAction Stop
        } -MaxAttempts 3 -DelaySeconds 1 -Description "removing $Label"
    } catch {
        Write-Warn "Could not remove $Label. It may be locked by another process."
        Write-Warn "  Please close any programs using it and delete manually: $Path"
    }
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
    Write-Step $Step $TotalSteps "Uninstalling Conch..."

    $existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    $existingDir = Test-Path $InstallDir

    if (-not $existingSvc -and -not $existingDir) {
        Write-Warn "No existing Conch installation found."
        return
    }

    if (-not $Yes) {
        if (-not (Prompt-User "Remove Conch completely (service + files)?" -Default "Y")) {
            Write-Info "Aborted by user."
            return
        }
    }

    if ($existingSvc) {
        Write-Info "Stopping service..."
        Stop-ServiceWait -Name $ServiceName
        cmd /c "sc.exe delete `"$ServiceName`" >nul 2>&1"
        cmd /c "nssm remove `"$ServiceName`" confirm >nul 2>&1"
        Write-OK "Service removed: $ServiceName"
    }

    Get-Process -Name "conch", "conch-mcp" -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue

    if ($existingDir) {
        Remove-Safe $InstallDir "install directory"
        if (-not (Test-Path $InstallDir)) {
            Write-OK "Removed: $InstallDir"
        }
    }

    $Step++
    Write-Step $Step $TotalSteps "Done."
    Write-Host ""
    Write-OK "Conch has been uninstalled."
    Write-Host ""
    return
}

# ============================================================================
# Step 2 - Detect & handle existing installation
# ============================================================================
$existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
$existingDir = Test-Path $InstallDir
$ServiceWasRunning = [bool]($existingSvc -and $existingSvc.Status -eq "Running")
$IsUpgrade = [bool]($existingSvc -or $existingDir)

if ($IsUpgrade) {
    Write-Step $Step $TotalSteps "Existing installation detected"
    if ($existingSvc) { Write-Warn "Service:  $ServiceName ($($existingSvc.Status))" }
    if ($existingDir) { Write-Warn "Location: $InstallDir" }
    Write-Info "Performing an in-place upgrade; configuration and durable job state will be preserved."
}

$Step++

# ============================================================================
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
if ($existingSvc) {
    Push-Rollback {
        if ($ServiceWasRunning) {
            Start-ServiceWait -Name $ServiceName -TimeoutSec 15 | Out-Null
        }
    } "Restore previous service running state"
    if (-not (Stop-ServiceWait -Name $ServiceName)) {
        throw "Service '$ServiceName' did not stop cleanly; refusing to replace its binary."
    }
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
if (Test-Path $BinPath) {
    Copy-Item -Force -LiteralPath $BinPath -Destination $ServerBackup
    Push-Rollback {
        if (Test-Path $ServerBackup) {
            Stop-ServiceWait -Name $ServiceName | Out-Null
            Copy-Item -Force -LiteralPath $ServerBackup -Destination $BinPath
            if ($existingSvc) {
                $rollbackNssm = Get-Command nssm -ErrorAction SilentlyContinue
                if ($rollbackNssm -and (Test-Path $EnvFile)) {
                    $rollbackEnv = @(
                        Get-Content -LiteralPath $EnvFile |
                            Where-Object { $_ -match '^[A-Za-z_][A-Za-z0-9_]*=' }
                    )
                    Invoke-Nssm -Path $rollbackNssm.Source -Arguments (@("set", $ServiceName, "AppEnvironmentExtra") + $rollbackEnv) -AllowFailure
                }
            }
        }
    } "Restore previous conch.exe and service registration"
}
if (Test-Path $McpBinPath) {
    Copy-Item -Force -LiteralPath $McpBinPath -Destination $McpBackup
    Push-Rollback {
        if (Test-Path $McpBackup) {
            Copy-Item -Force -LiteralPath $McpBackup -Destination $McpBinPath
        }
    } "Restore previous conch-mcp.exe"
}

Copy-IfDifferent $SrcBin $BinPath "conch.exe"
if ($SrcMcp) {
    Copy-IfDifferent $SrcMcp $McpBinPath "conch-mcp.exe"
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
if (Test-Path $EnvFile) {
    Copy-Item -Force -LiteralPath $EnvFile -Destination $EnvBackup
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
# Step 6 - Register & start service
# ============================================================================
Write-Step $Step $TotalSteps "Registering service..."

# nssm was already acquired in Step 1; verify it still resolves
if (-not $nssm) {
    $nssm = Get-Command nssm -ErrorAction SilentlyContinue
    if (-not $nssm) {
        throw "nssm lost after install. Please re-run the script."
    }
}
Write-OK "nssm ready: $($nssm.Source)"
$nssmExe = $nssm.Source

# Update an existing service in place so a later failure can retain its registration.
$existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existingSvc) {
    Write-Info "Updating existing service registration in place..."
    Stop-ServiceWait -Name $ServiceName | Out-Null
    Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "Application", $BinPath)
} else {
    Write-Info "Creating service..."
    Invoke-Nssm -Path $nssmExe -Arguments @("install", $ServiceName, $BinPath)
    Push-Rollback {
        Invoke-Nssm -Path $nssmExe -Arguments @("remove", $ServiceName, "confirm") -AllowFailure
    } "Remove created service: $ServiceName"
}

Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "AppDirectory", $InstallDir)
Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "Start", "SERVICE_AUTO_START")
Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "ObjectName", "NT AUTHORITY\SYSTEM")
Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "DisplayName", "Conch Shell Server")

# Set failure recovery: restart on failure (3 times)
Invoke-Nssm -Path $nssmExe -Arguments @("set", $ServiceName, "AppExit", "Default", "Restart")

# Environment variables
$envLines = @(
    Get-Content -LiteralPath $EnvFile |
        Where-Object { $_ -match '^[A-Za-z_][A-Za-z0-9_]*=' }
)
$environmentArguments = @("set", $ServiceName, "AppEnvironmentExtra") + $envLines
Invoke-Nssm -Path $nssmExe -Arguments $environmentArguments

Write-OK "Service registered: $ServiceName (auto-start, auto-restart on failure)"

# Start service
$DoStart = -not $NoStart
if (-not $NoStart -and -not $Yes) {
    Write-Host ""
    $DoStart = Prompt-User "Start the Conch service now?" -Default "Y"
}

if ($DoStart) {
    Write-Info "Starting service..."
    $started = Start-ServiceWait -Name $ServiceName -TimeoutSec 15

    if ($started) {
        Write-OK "Service started"
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
        Write-ErrorExit "Service '$ServiceName' did not reach Running state within 15 seconds."
    }
} else {
    Write-Info "Service installed but not started. Start manually:"
    Write-Info "  Start-Service -Name $ServiceName"
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
Write-Host "    Stop:        nssm stop $ServiceName"
Write-Host "    Start:       nssm start $ServiceName"
Write-Host "    Status:      nssm status $ServiceName"
Write-Host "    Uninstall:   .\install.ps1 -Uninstall"
Write-Host ""
Write-Host "  ${Yellow}Change API key:${Reset} (editing env.txt alone is NOT enough on Windows)"
Write-Host "    1. Edit config:  notepad $EnvFile"
Write-Host "    2. Reload env:   nssm set $ServiceName AppEnvironmentExtra (Get-Content $EnvFile)"
Write-Host "    3. Restart:      nssm restart $ServiceName"
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
