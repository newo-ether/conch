<#
.SYNOPSIS
    Install Conch Shell Server as a Windows service.

.DESCRIPTION
    Builds (or copies) the conch binary, generates an API key, and registers
    a Windows service via sc.exe. The binary includes native SCM support.

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
    Path to a pre-built conch.exe. Skips Go build.

.PARAMETER Prefix
    Install root directory. Default: $env:ProgramFiles\Conch.

.PARAMETER NoStart
    Install but don't start the service.

.PARAMETER Yes
    Skip all prompts, accept all defaults.

.PARAMETER Uninstall
    Remove the service, binary, and config.

.EXAMPLE
    # Full auto-install (random key, build from source)
    .\install.ps1

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
    [string]$ApiKey = "",
    [int]$Port = 14216,
    [string]$HostAddr = "0.0.0.0",
    [int]$TimeoutSec = 30,
    [int]$MaxTimeoutSec = 120,
    [switch]$NoAuth = $false,
    [string]$BinaryPath = "",
    [string]$McpBinaryPath = "",
    [string]$Prefix = "",
    [switch]$NoStart = $false,
    [switch]$Yes = $false,
    [switch]$Uninstall = $false
)

$ErrorActionPreference = "Stop"

# --- Admin check (before anything else) ---
$isAdmin = ([Security.Principal.WindowsPrincipal] `
    [Security.Principal.WindowsIdentity]::GetCurrent()
).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)

if (-not $isAdmin) {
    Write-Host "[x] Administrator privileges required." -ForegroundColor Red
    Write-Host "    Right-click PowerShell -> Run as Administrator, then re-run:" -ForegroundColor Red
    Write-Host "    .\install.ps1" -ForegroundColor Red
    exit 1
}

$ServiceName = "Conch"
$InstallDir  = if ($Prefix) { $Prefix } else { "$env:ProgramFiles\Conch" }
$BinPath     = "$InstallDir\conch.exe"
$McpBinPath  = "$InstallDir\conch-mcp.exe"
$EnvFile     = "$InstallDir\env.txt"
# $ScriptDir is $null when invoked via irm | iex (no actual script file).
# In that case, use the current directory as the fallback working root.
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
    # Fallback: use a temp staging directory (GitHub download doesn't need the repo)
    $tmpDir = Join-Path $env:TEMP "conch-install"
    New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null
    $tmpDir
}

function Write-Success  { Write-Host "[+] $args" -ForegroundColor Green }
function Write-Warn     { Write-Host "[!] $args" -ForegroundColor Yellow }
function Write-ErrorExit { Write-Host "[x] $args" -ForegroundColor Red; exit 1 }

# --- Helper: stop and wait for a service ---
function Stop-ServiceWait {
    param([string]$Name, [int]$TimeoutSec = 10)
    $svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
    if (-not $svc -or $svc.Status -ne "Running") { return }
    sc.exe stop $Name 2>&1 | Out-Null
    while ($TimeoutSec -gt 0) {
        Start-Sleep -Seconds 1
        $svc.Refresh()
        if ($svc.Status -ne "Running") { return }
        $TimeoutSec--
    }
    Write-Warn "Service $Name did not stop within timeout"
}

# --- Uninstall ---
if ($Uninstall) {
    Write-Success "Uninstalling Conch..."

    $svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
    if ($svc) {
        Stop-ServiceWait -Name $ServiceName
        sc.exe delete $ServiceName 2>&1 | Out-Null
        cmd /c "nssm remove $ServiceName confirm >nul 2>&1"
        Write-Success "Removed service: $ServiceName"
    } else {
        Write-Warn "Service not found: $ServiceName"
    }

    Get-Process -Name "conch" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

    if (Test-Path $InstallDir) {
        Remove-Item -Recurse -Force $InstallDir
        Write-Success "Removed: $InstallDir"
    }

    Write-Success "Uninstall complete."
    exit 0
}

$nssm = Get-Command nssm -ErrorAction SilentlyContinue

# --- Stop and remove existing service ---
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc) {
    Write-Warn "Removing existing service..."
    Stop-ServiceWait -Name $ServiceName
    sc.exe delete $ServiceName 2>&1 | Out-Null
    if ($nssm) { cmd /c "nssm remove $ServiceName confirm >nul 2>&1" }
}
Get-Process -Name "conch" -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

# --- Build or locate binary ---
$SrcBin = $null
$GitHubReleases = "https://github.com/newo-ether/conch/releases/latest/download"

function Download-File {
    param([string]$Name, [string]$Dest)
    $url = "$GitHubReleases/$Name"
    Write-Success "Downloading $Name..."
    try {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
        $client = New-Object System.Net.WebClient
        $client.DownloadFile($url, $Dest)
        return $true
    } catch {
        Write-Warn "Download failed: $_"
        return $false
    }
}

$ServerBinName = "conch-windows-amd64.exe"
$McpBinName = "conch-mcp-windows-amd64.exe"

if ($BinaryPath) {
    if (-not (Test-Path $BinaryPath)) {
        Write-ErrorExit "Binary not found: $BinaryPath"
    }
    $SrcBin = Resolve-Path $BinaryPath
    Write-Success "Using provided binary: $SrcBin"
} elseif (Download-File $ServerBinName "$RepoDir\conch.exe") {
    $SrcBin = "$RepoDir\conch.exe"
} else {
    $GoBin = Get-Command go -ErrorAction SilentlyContinue
    if ($GoBin -and (Test-Path "$RepoDir\go.mod")) {
        Write-Success "Building from source..."
        Push-Location $RepoDir
        try {
            & go build -o conch.exe .
            if ($LASTEXITCODE -ne 0) { throw "go build failed" }
            $SrcBin = "$RepoDir\conch.exe"
        } finally { Pop-Location }
    } elseif (Test-Path "$RepoDir\conch.exe") {
        $SrcBin = "$RepoDir\conch.exe"
    } elseif (Get-Command conch -ErrorAction SilentlyContinue) {
        $SrcBin = (Get-Command conch).Source
    } else {
        Write-ErrorExit "Failed to download or build binary. Use -BinaryPath to specify a pre-built binary path."
    }
}

# --- Install binary ---
New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
Copy-Item -Force $SrcBin $BinPath
Write-Success "Installed: $BinPath"

# --- Build or locate MCP binary ---
$SrcMcp = $null

if ($McpBinaryPath) {
    if (-not (Test-Path $McpBinaryPath)) {
        Write-Warn "MCP binary not found: $McpBinaryPath. Skipping conch-mcp."
    } else {
        $SrcMcp = Resolve-Path $McpBinaryPath
        Write-Success "Using provided MCP binary: $SrcMcp"
    }
} elseif (Download-File $McpBinName "$RepoDir\conch-mcp.exe") {
    $SrcMcp = "$RepoDir\conch-mcp.exe"
} else {
    $GoBin = Get-Command go -ErrorAction SilentlyContinue
    if ($GoBin -and (Test-Path "$RepoDir\go.mod")) {
        Write-Success "Building conch-mcp from source..."
        Push-Location $RepoDir
        try {
            & go build -o conch-mcp.exe ./cmd/mcp
            if ($LASTEXITCODE -ne 0) { throw "go build mcp failed" }
            $SrcMcp = "$RepoDir\conch-mcp.exe"
        } catch {
            Write-Warn "Failed to build conch-mcp: $_"
        } finally { Pop-Location }
    } elseif (Test-Path "$RepoDir\conch-mcp.exe") {
        $SrcMcp = "$RepoDir\conch-mcp.exe"
        Write-Success "Using prebuilt conch-mcp.exe"
    } elseif (Get-Command conch-mcp -ErrorAction SilentlyContinue) {
        $SrcMcp = (Get-Command conch-mcp).Source
        Write-Success "Using conch-mcp from PATH: $SrcMcp"
    } else {
        Write-Warn "No conch-mcp binary found. Skipping MCP bridge install."
    }
}

if ($SrcMcp) {
    Copy-Item -Force $SrcMcp $McpBinPath
    Write-Success "Installed: $McpBinPath"
}

# --- API Key ---
if (-not $ApiKey) {
    if (Test-Path $EnvFile) {
        $existing = Get-Content $EnvFile -Raw
        if ($existing -match "CONCH_API_KEY=(.+)") {
            $ApiKey = $Matches[1].Trim()
            Write-Success "Using existing API key from $EnvFile"
        }
    }
    if (-not $ApiKey) {
        $bytes = New-Object byte[] 32
        [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
        $ApiKey = [Convert]::ToBase64String($bytes).TrimEnd('=')
    }
}

# --- Write config ---
@"
CONCH_API_KEY=$ApiKey
CONCH_PORT=$Port
CONCH_HOST=$HostAddr
CONCH_TIMEOUT=$TimeoutSec
CONCH_MAX_TIMEOUT=$MaxTimeoutSec
CONCH_ALLOW_NO_AUTH=$($NoAuth.ToString().ToLower())
"@ | Out-File -FilePath $EnvFile -Encoding ASCII

Write-Success "Config: $EnvFile"
Write-Success "API key: $ApiKey"

if ($NoAuth) {
    Write-Warn "Authentication disabled. Do not expose to untrusted networks."
}

# --- Register Windows service via nssm ---
# nssm is needed because conch does not have native Windows SCM support.
if (-not $nssm) {
    Write-ErrorExit "nssm (Non-Sucking Service Manager) is required. Install via: winget install NSSM.NSSM"
}

# Stop and remove existing service (both sc.exe and nssm variants)
$svc = Get-Service -Name $ServiceName -ErrorAction SilentlyContinue
if ($svc) {
    Stop-ServiceWait -Name $ServiceName
    sc.exe delete $ServiceName 2>&1 | Out-Null
    cmd /c "nssm remove $ServiceName confirm >nul 2>&1"
}

# Install with nssm
& $nssm.Source install $ServiceName "$BinPath" 2>&1 | Out-Null
if ($LASTEXITCODE -ne 0) {
    Write-ErrorExit "nssm install failed"
}
& $nssm.Source set $ServiceName AppDirectory "$InstallDir" 2>&1 | Out-Null
& $nssm.Source set $ServiceName Start SERVICE_AUTO_START 2>&1 | Out-Null
& $nssm.Source set $ServiceName ObjectName "NT AUTHORITY\SYSTEM" 2>&1 | Out-Null
& $nssm.Source set $ServiceName DisplayName "Conch Shell Server" 2>&1 | Out-Null

# Set environment variables
$envLines = @(Get-Content $EnvFile)
& $nssm.Source set $ServiceName AppEnvironmentExtra $envLines 2>&1 | Out-Null

Write-Success "Service registered via nssm: $ServiceName (startup: auto)"

# --- Prompts ---
$DoStart = -not $NoStart
if (-not $NoStart -and -not $Yes) {
    $reply = Read-Host "Start service now? [Y/n]"
    if ($reply -match '^[nN]') { $DoStart = $false }
}

if ($DoStart) {
    & $nssm.Source start $ServiceName 2>&1 | Out-Null
    Write-Success "Service started"
} else {
    Write-Success "Service installed (not started). Start with: nssm start $ServiceName"
}

Write-Success ""
Write-Success "Done. Conch is running as a Windows service."
Write-Success "Test: curl.exe -s http://localhost:$Port/health"
Write-Success ""
Write-Success "Manage:"
Write-Success "  Stop:      nssm stop $ServiceName"
Write-Success "  Start:     nssm start $ServiceName"
Write-Success "  Status:    nssm status $ServiceName"
Write-Success "  Uninstall: .\install.ps1 -Uninstall"
