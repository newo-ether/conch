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

Write-Host "install.ps1 PowerShell 5 regression checks passed."
