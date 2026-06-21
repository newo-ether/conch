<#
.SYNOPSIS
    Install Conch Shell Server as a Windows service.

.DESCRIPTION
    Downloads (or builds) the conch binary, generates an API key, and registers
    a Windows service via nssm. Interactive by default — use -Yes for scripting.

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
    [int]   $Port          = 14216,
    [string]$HostAddr      = "0.0.0.0",
    [int]   $TimeoutSec    = 30,
    [int]   $MaxTimeoutSec = 120,
    [switch]$NoAuth        = $false,
    [string]$BinaryPath    = "",
    [string]$McpBinaryPath = "",
    [string]$Prefix        = "",
    [switch]$NoStart       = $false,
    [switch]$Yes           = $false,
    [switch]$Uninstall     = $false
)

& {

$ErrorActionPreference = "Stop"
$Host.UI.RawUI.WindowTitle = "Conch Installer"

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
        Write-Host "    ${Yellow}→${Reset} $($entry.Desc)" -ForegroundColor Yellow
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

# Global error trap — attempt rollback on fatal error
trap {
    Write-Err "FATAL: $($_.Exception.Message)"
    Invoke-Rollback
    Write-Host ""
    Write-Err "Installation failed. The system has been restored to its previous state."
    Write-Host "  For manual installation help: https://github.com/newo-ether/conch"
    Write-Host ""
    exit 1
}

# ============================================================================
# Output helpers
# ============================================================================

function Write-Banner {
    Write-Host ""
    Write-Host "  ╔══════════════════════════════════════════════╗" -ForegroundColor Cyan
    Write-Host "  ║          Conch Shell Server                  ║" -ForegroundColor Cyan
    Write-Host "  ║          Windows Installer                   ║" -ForegroundColor Cyan
    Write-Host "  ╚══════════════════════════════════════════════╝" -ForegroundColor Cyan
    Write-Host ""
}

function Write-Step {
    param([string]$Current, [string]$Total, [string]$Text)
    Write-Host "  [$Current/$Total] " -ForegroundColor Cyan -NoNewline
    Write-Host $Text
}

function Write-OK   { Write-Host "    ${Cyan}✓${Reset} $args" }
function Write-Warn { Write-Host "    ${Yellow}⚠${Reset} $args" }
function Write-Err  { Write-Host "    ${Red}✗${Reset} $args" }
function Write-Info { Write-Host "    ${Cyan}→${Reset} $args" }

function Write-ErrorExit {
    param([string]$Message, [switch]$NoRollback)
    Write-Host ""
    Write-Err $Message
    if (-not $NoRollback) { Invoke-Rollback }
    Write-Host ""
    exit 1
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
# Step 1 — Environment checks
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
    exit 1
}
Write-OK "Administrator"

# --- OS version sanity check ---
$osVer = [Environment]::OSVersion.Version
Write-OK "OS: Windows $($osVer.Major).$($osVer.Minor) (build $($osVer.Build))"

# --- nssm check ---
$nssm = Get-Command nssm -ErrorAction SilentlyContinue
if ($nssm) {
    Write-OK "nssm: $($nssm.Source)"
} elseif (-not $Uninstall) {
    Write-Info "nssm not found — will install automatically"
}

# --- Internet connectivity check (non-blocking, just a warning) ---
if (-not $Uninstall -and -not $BinaryPath) {
    try {
        $connTest = [Net.NetworkInformation.NetworkInterface]::GetIsNetworkAvailable()
        if (-not $connTest) {
            Write-Warn "No network connection detected — download may fail"
        } else {
            Write-OK "Network available"
        }
    } catch {
        # Connectivity check is best-effort
    }
}

# --- Port conflict check ---
if (-not $Uninstall) {
    $portInUse = Get-NetTCPConnection -LocalPort $Port -ErrorAction SilentlyContinue |
        Where-Object { $_.State -eq "Listen" }
    if ($portInUse) {
        $proc = Get-Process -Id $portInUse.OwningProcess -ErrorAction SilentlyContinue
        $procName = if ($proc) { $proc.ProcessName } else { "unknown" }
        Write-Warn "Port $Port is already in use by: $procName"
        if (-not $Yes) {
            if (-not (Prompt-User "Continue anyway?" -Default "Y")) {
                Write-Info "Aborted. Choose a different port with -Port <number>"
                exit 0
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

$GitHubReleases = "https://github.com/newo-ether/conch/releases/latest/download"

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
    Write-Warn "Service '$Name' did not stop within timeout — forcing..."
    Get-Process -Name $Name -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    return $false
}

# ============================================================================
# Helper: atomic file write (write temp, then move)
# ============================================================================
function Write-AtomicConfig {
    param([string]$Content, [string]$Target)
    $tmp = "$Target.tmp"
    $Content | Out-File -FilePath $tmp -Encoding ASCII -NoNewline
    Move-Item -Force $tmp $Target -ErrorAction Stop
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
# Step 2 — Uninstall (if requested)
# ============================================================================
if ($Uninstall) {
    Write-Step $Step $TotalSteps "Uninstalling Conch..."

    $existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    $existingDir = Test-Path $InstallDir

    if (-not $existingSvc -and -not $existingDir) {
        Write-Warn "No existing Conch installation found."
        exit 0
    }

    if (-not $Yes) {
        if (-not (Prompt-User "Remove Conch completely (service + files)?" -Default "Y")) {
            Write-Info "Aborted by user."
            exit 0
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
    exit 0
}

# ============================================================================
# Step 2 — Detect & handle existing installation
# ============================================================================

$existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
$existingDir = Test-Path $InstallDir

if ($existingSvc -or $existingDir) {
    Write-Step $Step $TotalSteps "Existing installation detected"

    if ($existingSvc) { Write-Warn "Service:  $ServiceName ($($existingSvc.Status))" }
    if ($existingDir) { Write-Warn "Location: $InstallDir" }

    if (-not $Yes) {
        Write-Host ""
        $remove = Prompt-User "Remove existing installation before proceeding?" -Default "Y"
    } else {
        $remove = $true
    }

    if ($remove) {
        Write-Info "Removing existing installation..."
        if ($existingSvc) {
            Stop-ServiceWait -Name $ServiceName
            cmd /c "sc.exe delete `"$ServiceName`" >nul 2>&1"
            cmd /c "nssm remove `"$ServiceName`" confirm >nul 2>&1"
            Write-OK "Service removed"
        }
        Get-Process -Name "conch", "conch-mcp" -ErrorAction SilentlyContinue |
            Stop-Process -Force -ErrorAction SilentlyContinue
        if ($existingDir) {
            Remove-Safe $InstallDir "install directory"
            if (-not (Test-Path $InstallDir)) {
                Write-OK "Directory removed: $InstallDir"
            }
        }
    } else {
        Write-Info "Keeping existing files. Proceeding with in-place update."
    }
}

$Step++

# ============================================================================
# Step 3 — Acquire binary
# ============================================================================
Write-Step $Step $TotalSteps "Acquiring conch binary..."

function Download-File {
    param([string]$Name, [string]$Dest)
    $url = "$GitHubReleases/$Name"
    Write-Info "Downloading $Name..."
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls13
        $client = New-Object System.Net.WebClient
        $client.Headers.Add("User-Agent", "Conch-Installer/1.0")
        Retry-Command -Script {
            $client.DownloadFile($url, $Dest)
            if (-not (Test-Path $Dest)) { throw "Download completed but file not found" }
        } -MaxAttempts 3 -DelaySeconds 3 -Description "download $Name"
        Write-OK "Downloaded: $Name"
        return $true
    } catch {
        Write-Warn "Download failed: $($_.Exception.Message.Split([Environment]::NewLine)[0])"
        if (Test-Path $Dest) { Remove-Item $Dest -Force -ErrorAction SilentlyContinue }
        return $false
    }
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
        Write-Warn "Binary at $SrcBin seems small — it may not be a valid executable"
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

$Step++

# ============================================================================
# Step 4 — Install files
# ============================================================================
Write-Step $Step $TotalSteps "Installing files..."

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
    # Stop any running process using the destination
    $destName = [IO.Path]::GetFileNameWithoutExtension($Dest)
    Get-Process -Name $destName -ErrorAction SilentlyContinue |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Retry-Command -Script {
        Copy-Item -Force $Source $Dest -ErrorAction Stop
    } -MaxAttempts 3 -DelaySeconds 1 -Description "copying $Label"
    Write-OK "$Label installed"
}

Copy-IfDifferent $SrcBin $BinPath "conch.exe"

# MCP binary
$SrcMcp = $null
if ($McpBinaryPath) {
    if (-not (Test-Path $McpBinaryPath)) {
        Write-Warn "MCP binary not found: $McpBinaryPath — skipping conch-mcp"
    } else {
        $SrcMcp = Resolve-Path $McpBinaryPath
        Write-OK "Using provided MCP binary"
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

if ($SrcMcp) {
    Copy-IfDifferent $SrcMcp $McpBinPath "conch-mcp.exe"
} else {
    Write-Warn "conch-mcp not available — MCP bridge will not be installed"
}

$Step++

# ============================================================================
# Step 5 — Configuration
# ============================================================================
Write-Step $Step $TotalSteps "Configuring..."

# API Key
if (-not $ApiKey) {
    if (Test-Path $EnvFile) {
        $existing = Get-Content $EnvFile -Raw -ErrorAction SilentlyContinue
        if ($existing -match "CONCH_API_KEY=(\S+)") {
            $ApiKey = $Matches[1].Trim()
            Write-OK "Reusing API key from existing env.txt"
        }
    }
    if (-not $ApiKey) {
        $bytes = New-Object byte[] 32
        [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
        $ApiKey = [Convert]::ToBase64String($bytes).TrimEnd('=')
        Write-OK "Generated new API key"
    }
}

$maskedKey = if ($ApiKey.Length -gt 8) {
    $ApiKey.Substring(0, 4) + "..." + $ApiKey.Substring($ApiKey.Length - 4)
} else { $ApiKey }

$configContent = @"
CONCH_API_KEY=$ApiKey
CONCH_PORT=$Port
CONCH_HOST=$HostAddr
CONCH_TIMEOUT=$TimeoutSec
CONCH_MAX_TIMEOUT=$MaxTimeoutSec
CONCH_ALLOW_NO_AUTH=$($NoAuth.ToString().ToLower())
"@

Write-AtomicConfig $configContent $EnvFile
Write-OK "Config written: $EnvFile"
Write-Info "API key: $maskedKey"

if ($NoAuth) {
    Write-Warn "Authentication is DISABLED — do not expose to untrusted networks!"
}

$Step++

# ============================================================================
# Step 6 — Register & start service
# ============================================================================
Write-Step $Step $TotalSteps "Registering service..."

# Ensure nssm is available
if (-not $nssm) {
    Write-Info "Installing nssm via winget..."

    # Try multiple known nssm paths
    $nssmPaths = @(
        "$env:ProgramFiles\nssm\nssm.exe",
        "${env:ProgramFiles(x86)}\nssm\nssm.exe",
        "$env:ChocolateyInstall\bin\nssm.exe",
        "$env:SystemRoot\nssm.exe"
    )
    foreach ($p in $nssmPaths) {
        if (Test-Path $p) {
            $nssm = Get-Command $p -ErrorAction SilentlyContinue
            if ($nssm) { break }
        }
    }

    if (-not $nssm) {
        try {
            winget install NSSM.NSSM --accept-source-agreements --accept-package-agreements --silent 2>&1 | Out-Null
            Start-Sleep -Seconds 3
            # Refresh PATH
            $env:Path = [System.Environment]::GetEnvironmentVariable("Path", "Machine") + ";" +
                        [System.Environment]::GetEnvironmentVariable("Path", "User")
            $nssm = Get-Command nssm -ErrorAction SilentlyContinue
            if (-not $nssm) {
                # Try the default winget install location
                $defPath = "$env:ProgramFiles\nssm\nssm.exe"
                if (Test-Path $defPath) { $nssm = Get-Command $defPath }
            }
        } catch {
            Write-Warn "winget install failed: $($_.Exception.Message)"
        }
    }

    if (-not $nssm) {
        Write-ErrorExit "nssm is required but could not be installed automatically.`n  Install manually: winget install NSSM.NSSM`n  Or download from: https://nssm.cc/download"
    }
    Write-OK "nssm ready: $($nssm.Source)"
}

# Clean up any leftover service registration
$existingSvc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($existingSvc) {
    Write-Info "Removing stale service registration..."
    Stop-ServiceWait -Name $ServiceName
    cmd /c "sc.exe delete `"$ServiceName`" >nul 2>&1"
    cmd /c "nssm remove `"$ServiceName`" confirm >nul 2>&1"
    Start-Sleep -Seconds 2
    # Verify removal
    $stillThere = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($stillThere) {
        Write-Warn "Failed to remove existing service. Trying force removal..."
        cmd /c "sc.exe delete `"$ServiceName`" >nul 2>&1"
        Start-Sleep -Seconds 3
        $stillThere = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($stillThere) {
            Write-ErrorExit "Cannot remove existing service '$ServiceName'.`n  Please remove it manually (sc.exe delete $ServiceName) or reboot and retry."
        }
    }
}

# Register with nssm
Write-Info "Creating service..."
$nssmExe = $nssm.Source

& $nssmExe install $ServiceName "$BinPath" 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) { Write-ErrorExit "nssm install failed (exit code $LASTEXITCODE)" }
Push-Rollback { cmd /c "nssm remove `"$ServiceName`" confirm >nul 2>&1" } "Remove created service: $ServiceName"

& $nssmExe set $ServiceName AppDirectory "$InstallDir" 2>&1 | Out-Null
& $nssmExe set $ServiceName Start SERVICE_AUTO_START 2>&1 | Out-Null
& $nssmExe set $ServiceName ObjectName "NT AUTHORITY\SYSTEM" 2>&1 | Out-Null
& $nssmExe set $ServiceName DisplayName "Conch Shell Server" 2>&1 | Out-Null

# Set failure recovery: restart on failure (3 times)
cmd /c "nssm set `"$ServiceName`" AppExit Default Restart >nul 2>&1"

# Environment variables
$envLines = @(Get-Content $EnvFile)
& $nssmExe set $ServiceName AppEnvironmentExtra $envLines 2>&1 | Out-Null

Write-OK "Service registered: $ServiceName (auto-start, auto-restart on failure)"

# Start service
$DoStart = -not $NoStart
if (-not $NoStart -and -not $Yes) {
    Write-Host ""
    $DoStart = Prompt-User "Start the Conch service now?" -Default "Y"
}

if ($DoStart) {
    Write-Info "Starting service..."
    & $nssmExe start $ServiceName 2>&1 | Out-Null

    # Wait and verify with retry
    $started = $false
    for ($i = 0; $i -lt 5; $i++) {
        Start-Sleep -Seconds 2
        $svcCheck = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
        if ($svcCheck -and $svcCheck.Status -eq "Running") {
            $started = $true
            break
        }
        if ($i -lt 4) {
            Write-Info "Waiting for service to start... (attempt $($i + 2)/5)"
            & $nssmExe start $ServiceName 2>&1 | Out-Null
        }
    }

    if ($started) {
        Write-OK "Service started"
        # Quick health check
        try {
            Start-Sleep -Seconds 1
            $health = Invoke-RestMethod -Uri "http://localhost:$Port/health" -TimeoutSec 5 -ErrorAction SilentlyContinue
            if ($health) {
                Write-OK "Health check passed: localhost:$Port/health"
            }
        } catch {
            Write-Warn "Health check failed — service may still be initializing"
        }
    } else {
        Write-Warn "Service may not have started within timeout"
        Write-Warn "  Check logs: Get-EventLog -LogName Application -Source `"$ServiceName`" -Newest 10"
        Write-Warn "  Or: nssm status $ServiceName"
    }
} else {
    Write-Info "Service installed but not started. Start manually:"
    Write-Info "  nssm start $ServiceName"
}

# ============================================================================
# Done
# ============================================================================
Write-Host ""
Write-Host "  ╔══════════════════════════════════════════════╗" -ForegroundColor Green
Write-Host "  ║  ${Bold}Installation Complete${Reset}                          " -ForegroundColor Green
Write-Host "  ╚══════════════════════════════════════════════╝" -ForegroundColor Green
Write-Host ""
Write-Host "  ${Cyan}Health check:${Reset}   curl.exe -s http://localhost:$Port/health"
Write-Host "  ${Cyan}API key:${Reset}       $maskedKey"
Write-Host "  ${Cyan}Config file:${Reset}   $EnvFile"
Write-Host ""
Write-Host "  ${Cyan}Manage:${Reset}"
Write-Host "    Stop:        nssm stop $ServiceName"
Write-Host "    Start:       nssm start $ServiceName"
Write-Host "    Status:      nssm status $ServiceName"
Write-Host "    Uninstall:   .\install.ps1 -Uninstall"
Write-Host ""

}
