$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$installerPath = Join-Path $repoRoot "scripts\install.ps1"

$bytes = [IO.File]::ReadAllBytes($installerPath)
$nonAsciiOffset = -1
for ($i = 0; $i -lt $bytes.Length; $i++) {
    if ($bytes[$i] -gt 127) {
        $nonAsciiOffset = $i
        break
    }
}
if ($nonAsciiOffset -ge 0) {
    throw "install.ps1 contains a non-ASCII byte at offset $nonAsciiOffset"
}

$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile(
    $installerPath,
    [ref]$tokens,
    [ref]$parseErrors
)
if ($parseErrors.Count -gt 0) {
    $messages = $parseErrors | ForEach-Object { $_.Message }
    throw "PowerShell parser errors: $($messages -join '; ')"
}

$functions = @{}
$ast.FindAll(
    {
        param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst]
    },
    $true
) | ForEach-Object {
    $functions[$_.Name] = $_
}

foreach ($required in @("Start-ServiceWait", "Invoke-Nssm")) {
    if (-not $functions.ContainsKey($required)) {
        throw "Missing installer helper: $required"
    }
}

$installerText = [IO.File]::ReadAllText($installerPath, [Text.Encoding]::ASCII)
if ($installerText -match '&\s+\$nssmExe\s+start') {
    throw "Installer must not use NSSM start output as the service readiness signal"
}
if ($installerText -notmatch 'Start-ServiceWait\s+-Name\s+\$ServiceName') {
    throw "Installer does not start the registered service through Start-ServiceWait"
}

$script:mockStatuses = @("Stopped", "StartPending", "Running")
$script:mockStatusIndex = 0
$script:mockStartCalls = 0

function Get-Service {
    param([string]$Name, [object]$ErrorAction)
    $index = [Math]::Min($script:mockStatusIndex, $script:mockStatuses.Count - 1)
    $script:mockStatusIndex++
    [pscustomobject]@{ Status = $script:mockStatuses[$index] }
}

function Start-Service {
    param([string]$Name, [object]$ErrorAction)
    $script:mockStartCalls++
    throw "mock START_PENDING native race"
}

function Start-Sleep {
    param([int]$Seconds)
}

. ([scriptblock]::Create($functions["Start-ServiceWait"].Extent.Text))

$started = Start-ServiceWait -Name "Conch" -TimeoutSec 3
if (-not $started) {
    throw "Start-ServiceWait rejected a valid StartPending -> Running transition"
}
if ($script:mockStartCalls -ne 1) {
    throw "Expected exactly one service start attempt, got $script:mockStartCalls"
}

# Release downloads must be pinned, checksummed, and must not terminate a
# running MCP process during an in-place path swap.
foreach ($required in @("Get-ExpectedReleaseHash", "Copy-IfDifferent")) {
    if (-not $functions.ContainsKey($required)) {
        throw "Missing installer hardening helper: $required"
    }
}
$copyText = $functions["Copy-IfDifferent"].Extent.Text
if ($copyText -match "Stop-Process") {
    throw "Copy-IfDifferent must not terminate a running MCP process"
}
if ($installerText -notmatch '(?s)Installation failed.*?\bthrow\s*\r?\n\}') {
    throw "Installer catch path does not propagate a nonzero failure"
}
foreach ($marker in @(
    "checksums.txt",
    "Verified SHA-256",
    "Preserving existing configuration and durable job settings",
    "Restore previous configuration",
    "Restore previous conch.exe and service registration",
    "Restore previous service running state",
    "ValidateRange(1, 65535)",
    "TimeoutSec must not exceed MaxTimeoutSec"
)) {
    if ($installerText -notmatch [Regex]::Escape($marker)) {
        throw "Missing installer hardening marker: $marker"
    }
}

$tempManifest = Join-Path $env:TEMP ("conch-checksums-" + [Guid]::NewGuid().ToString("N") + ".txt")
try {
    $expectedHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    [IO.File]::WriteAllText(
        $tempManifest,
        "$expectedHash  conch-windows-amd64.exe`n",
        [Text.Encoding]::ASCII
    )
    $ChecksumManifest = $tempManifest
    . ([scriptblock]::Create($functions["Get-ExpectedReleaseHash"].Extent.Text))
    $parsedHash = Get-ExpectedReleaseHash "conch-windows-amd64.exe"
    if ($parsedHash -ne $expectedHash) {
        throw "Checksum parser returned '$parsedHash'"
    }
} finally {
    Remove-Item -Force -LiteralPath $tempManifest -ErrorAction SilentlyContinue
}

$releaseBuilderPath = Join-Path $repoRoot "scripts\build-release.ps1"
$releaseTokens = $null
$releaseParseErrors = $null
[Management.Automation.Language.Parser]::ParseFile(
    $releaseBuilderPath,
    [ref]$releaseTokens,
    [ref]$releaseParseErrors
) | Out-Null
if ($releaseParseErrors.Count -gt 0) {
    throw "build-release.ps1 parser errors: $($releaseParseErrors -join '; ')"
}
$releaseBuilderText = [IO.File]::ReadAllText($releaseBuilderPath)
if (-not $releaseBuilderText.Contains('[Array]::Sort($checksumNames, [StringComparer]::Ordinal)')) {
    throw "Release checksum manifest is not sorted with ordinal semantics"
}
if (-not $releaseBuilderText.Contains('$checksumContent = [string]::Join("`n", $checksumLines) + "`n"')) {
    throw "Release checksum manifest is not normalized to LF"
}

Write-Host "install.ps1 PowerShell 5 regression checks passed."
