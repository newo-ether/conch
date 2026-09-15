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

foreach ($required in @(
    "Select-InstallMode",
    "Resolve-InstallMode",
    "Start-ServiceWait",
    "Invoke-Nssm",
    "Test-SamePath",
    "Get-ProcessesAtExactPath",
    "Stop-ProcessesAtExactPath",
    "Stop-UserTaskProcess",
    "New-UserLauncherContent"
)) {
    if (-not $functions.ContainsKey($required)) {
        throw "Missing installer helper: $required"
    }
}

$installerText = [IO.File]::ReadAllText($installerPath, [Text.Encoding]::ASCII)
if ($installerText.Contains("`r")) {
    throw "install.ps1 must use repository-normalized LF line endings"
}
if ($installerText -match '&\s+\$nssmExe\s+start') {
    throw "Installer must not use NSSM start output as the service readiness signal"
}
if ($installerText -notmatch 'Start-ServiceWait\s+-Name\s+\$ServiceName') {
    throw "Installer does not start the registered service through Start-ServiceWait"
}
if ($installerText -notmatch '\$Mode\s*=\s*Resolve-InstallMode') {
    throw "Installer does not resolve an omitted mode before choosing paths"
}

. ([scriptblock]::Create($functions["Select-InstallMode"].Extent.Text))

$modeCases = @(
    @{
        Name = "new install defaults to user"
        Args = @{
            RequestedMode = ""; ForUninstall = $false; HasCustomPrefix = $false
            CustomPrefixExists = $false; SystemPresent = $false; UserPresent = $false
        }
        Expected = "user"
    },
    @{
        Name = "existing system install is retained"
        Args = @{
            RequestedMode = ""; ForUninstall = $false; HasCustomPrefix = $false
            CustomPrefixExists = $false; SystemPresent = $true; UserPresent = $false
        }
        Expected = "system"
    },
    @{
        Name = "existing user install is retained"
        Args = @{
            RequestedMode = ""; ForUninstall = $false; HasCustomPrefix = $false
            CustomPrefixExists = $false; SystemPresent = $false; UserPresent = $true
        }
        Expected = "user"
    },
    @{
        Name = "explicit system selection wins"
        Args = @{
            RequestedMode = "system"; ForUninstall = $false; HasCustomPrefix = $false
            CustomPrefixExists = $false; SystemPresent = $false; UserPresent = $false
        }
        Expected = "system"
    },
    @{
        Name = "empty uninstall is a user-mode no-op"
        Args = @{
            RequestedMode = ""; ForUninstall = $true; HasCustomPrefix = $false
            CustomPrefixExists = $false; SystemPresent = $false; UserPresent = $false
        }
        Expected = "user"
    }
)
foreach ($case in $modeCases) {
    $caseArgs = $case.Args
    $selected = Select-InstallMode @caseArgs
    if ($selected -ne $case.Expected) {
        throw "$($case.Name): expected $($case.Expected), got $selected"
    }
}

$ambiguousCases = @(
    @{
        Name = "both modes"
        Args = @{
            RequestedMode = ""; ForUninstall = $false; HasCustomPrefix = $false
            CustomPrefixExists = $false; SystemPresent = $true; UserPresent = $true
        }
    },
    @{
        Name = "unidentified custom prefix"
        Args = @{
            RequestedMode = ""; ForUninstall = $false; HasCustomPrefix = $true
            CustomPrefixExists = $true; SystemPresent = $false; UserPresent = $false
        }
    }
)
foreach ($case in $ambiguousCases) {
    $caseArgs = $case.Args
    $threw = $false
    try {
        [void](Select-InstallMode @caseArgs)
    } catch {
        $threw = $true
    }
    if (-not $threw) {
        throw "$($case.Name): installer guessed a mode instead of requiring an explicit choice"
    }
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
    "Restore previous conch.exe and registration",
    "Restore previous scheduled task definition",
    "Restore previous user launcher",
    "Remove newly created user launcher",
    "Restore previous user task running state",
    "Refusing an implicit mode migration",
    "Refusing an implicit service or prefix migration",
    "Refusing an implicit prefix migration",
    "Remove newly installed conch.exe",
    "Restore previous service running state",
    "ValidateRange(1, 65535)",
    "TimeoutSec must not exceed MaxTimeoutSec",
    "Default: 1800."
)) {
    if ($installerText -notmatch [Regex]::Escape($marker)) {
        throw "Missing installer hardening marker: $marker"
    }
}

if (-not $installerText.Contains('[int]   $MaxTimeoutSec = 1800,')) {
    throw "PowerShell installer MaxTimeoutSec default is not 1800"
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

$releaseWorkflowPath = Join-Path $repoRoot ".github\workflows\release.yml"
$releaseWorkflowText = [IO.File]::ReadAllText($releaseWorkflowPath)
foreach ($marker in @(
    "verify-windows:",
    "needs: verify-windows",
    "--draft",
    "--draft=false",
    "refusing to replace published assets"
)) {
    if (-not $releaseWorkflowText.Contains($marker)) {
        throw "Release workflow is missing immutable cross-platform gate: $marker"
    }
}
if ($releaseWorkflowText.Contains("--clobber")) {
    throw "Release workflow can still replace published assets"
}

$findNssmText = $functions["Find-Nssm"].Extent.Text
if ($findNssmText -notmatch '\$InstallDir\\nssm\.exe') {
    throw "Find-Nssm does not prefer the installer's own nssm.exe"
}
$promptText = $functions["Prompt-User"].Extent.Text
if ($promptText -notmatch 'CONCH_YES') {
    throw "Prompt-User does not honor CONCH_YES"
}
if ($promptText -notmatch 'IsInputRedirected') {
    throw "Prompt-User does not default on redirected stdin"
}
$hashText = $functions["Get-ExpectedReleaseHash"].Extent.Text
if ($hashText -notmatch '\[switch\]\$Refresh') {
    throw "Get-ExpectedReleaseHash lacks the Refresh switch"
}
if ($hashText -notmatch 'Remove-Item -LiteralPath \$ChecksumManifest') {
    throw "Get-ExpectedReleaseHash Refresh does not remove the stale manifest"
}
$downloadText = $functions["Download-File"].Extent.Text
if ($downloadText -notmatch 'foreach \(\$refresh') {
    throw "Download-File does not retry with a refreshed checksum manifest"
}
if ($installerText -notmatch 'held by an existing Conch process') {
    throw "Installer still prompts when the port is held by an existing Conch process"
}

# User mode must keep the scheduled-task action alive for the lifetime of
# conch.exe, own one exact PID/path, and restore overwritten task definitions.
$launcherText = $functions["New-UserLauncherContent"].Extent.Text
foreach ($marker in @(
    "Start-Process",
    "WaitForExit",
    "RedirectStandardOutput",
    "RedirectStandardError",
    "WriteAllText(`$pidFile",
    "[Environment]::Exit(`$exitCode)"
)) {
    if ($launcherText -notmatch [Regex]::Escape($marker)) {
        throw "User launcher is missing lifecycle marker: $marker"
    }
}
if ($launcherText -match 'WScript|cmd\.exe\s+/c|,\s*False') {
    throw "User launcher still detaches the Conch process"
}
$stopUserText = $functions["Stop-UserTaskProcess"].Extent.Text
if ($stopUserText -match 'Get-Process\s+-Name') {
    throw "User task stop path must not kill processes globally by name"
}
if ($installerText -match 'Get-Process\s+-Name') {
    throw "Installer must not kill system or user processes globally by name"
}
if ($installerText.IndexOf('$InstallDir = if ($Prefix)') -gt
    $installerText.IndexOf('$nssm = Find-Nssm')) {
    throw "InstallDir is not resolved before Find-Nssm runs"
}
$serviceUpdateIndex = $installerText.IndexOf('Updating existing service registration in place')
$serviceCreateIndex = $installerText.IndexOf('Creating service...', $serviceUpdateIndex)
$identityIndex = $installerText.IndexOf('"ObjectName"', $serviceUpdateIndex)
if ($serviceUpdateIndex -lt 0 -or $serviceCreateIndex -lt 0 -or
    $identityIndex -lt $serviceCreateIndex) {
    throw "Existing system-service upgrades can still rewrite the service identity"
}
if ([Regex]::Matches($installerText, '"ObjectName"').Count -ne 1) {
    throw "System-service identity should be configured only during first creation"
}
foreach ($marker in @(
    'Export-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName',
    'Register-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -Xml $taskBackupXml',
    '-MultipleInstances IgnoreNew',
    'Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName',
    '$existingSvc = if ($Mode -eq "system")',
    '$existingTask = if ($Mode -eq "user")',
    'Uninstalling Conch ($Mode mode)'
)) {
    if (-not $installerText.Contains($marker)) {
        throw "Missing user-mode isolation marker: $marker"
    }
}

# Exercise the exact-path PID stop helper without creating a real scheduled task.
$tempProcessRoot = Join-Path $env:TEMP ("conch-user-process-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tempProcessRoot | Out-Null
$fakeConch = Join-Path $tempProcessRoot "conch.exe"
$pidFile = Join-Path $tempProcessRoot "conch.pid"
$unrelated = $null
$owned = $null
try {
    Copy-Item -LiteralPath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -Destination $fakeConch
    . ([scriptblock]::Create($functions["Test-SamePath"].Extent.Text))
    . ([scriptblock]::Create($functions["Get-ProcessesAtExactPath"].Extent.Text))
    . ([scriptblock]::Create($functions["Stop-ProcessesAtExactPath"].Extent.Text))
    . ([scriptblock]::Create($functions["Stop-UserTaskProcess"].Extent.Text))
    function Write-Warn { param([Parameter(ValueFromRemainingArguments = $true)]$Message) }
    function Stop-ScheduledTask { param($TaskPath, $TaskName, $ErrorAction) }

    $owned = Start-Process -FilePath $fakeConch -ArgumentList @(
        "-NoLogo", "-NoProfile", "-Command", "Start-Sleep -Seconds 30"
    ) -PassThru
    [IO.File]::WriteAllText($pidFile, [string]$owned.Id)
    Stop-UserTaskProcess -Name "Conch-Test" -ProcessIdFile $pidFile -ExpectedBinary $fakeConch
    $owned.Refresh()
    if (-not $owned.HasExited) {
        throw "Exact-path user process was not stopped"
    }

    $unrelated = Start-Process -FilePath "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe" -ArgumentList @(
        "-NoLogo", "-NoProfile", "-Command", "Start-Sleep -Seconds 30"
    ) -PassThru
    [IO.File]::WriteAllText($pidFile, [string]$unrelated.Id)
    Stop-UserTaskProcess -Name "Conch-Test" -ProcessIdFile $pidFile -ExpectedBinary $fakeConch
    $unrelated.Refresh()
    if ($unrelated.HasExited) {
        throw "Stale PID handling terminated an unrelated process"
    }
} finally {
    if ($owned -and -not $owned.HasExited) { Stop-Process -Id $owned.Id -Force -ErrorAction SilentlyContinue }
    if ($unrelated -and -not $unrelated.HasExited) { Stop-Process -Id $unrelated.Id -Force -ErrorAction SilentlyContinue }
    Remove-Item -LiteralPath $tempProcessRoot -Recurse -Force -ErrorAction SilentlyContinue
}

# Exercise the generated launcher end to end with a disposable executable.
# This proves PID ownership, synchronous waiting, separate logs and exit-code
# propagation without registering a real Scheduled Task.
$launcherRoot = Join-Path $env:TEMP ("conch-user-launcher-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $launcherRoot | Out-Null
try {
    $sourcePath = Join-Path $launcherRoot "main.go"
    $launcherBinary = Join-Path $launcherRoot "conch.exe"
    $launcherConfig = Join-Path $launcherRoot "env.txt"
    $launcherScript = Join-Path $launcherRoot "launch.ps1"
    $launcherPidFile = Join-Path $launcherRoot "conch.pid"
    $launcherStdout = Join-Path $launcherRoot "stdout.log"
    $launcherStderr = Join-Path $launcherRoot "stderr.log"
    $source = @'
package main
import (
    "fmt"
    "os"
    "time"
)
func main() {
    fmt.Fprintln(os.Stdout, "stdout-marker")
    fmt.Fprintln(os.Stderr, "stderr-marker")
    time.Sleep(1500 * time.Millisecond)
    os.Exit(7)
}
'@
    [IO.File]::WriteAllText($sourcePath, $source, [Text.Encoding]::ASCII)
    & go build -o $launcherBinary $sourcePath
    if ($LASTEXITCODE -ne 0) {
        throw "Could not build the disposable launcher test executable"
    }
    [IO.File]::WriteAllText($launcherConfig, "CONCH_LAUNCHER_TEST=owned`n", [Text.Encoding]::ASCII)
    . ([scriptblock]::Create($functions["New-UserLauncherContent"].Extent.Text))
    $launcherArguments = @{
        InstallRoot = $launcherRoot
        ConfigPath = $launcherConfig
        ServerPath = $launcherBinary
        ProcessIdFile = $launcherPidFile
        StdoutPath = $launcherStdout
        StderrPath = $launcherStderr
    }
    $generatedLauncher = New-UserLauncherContent @launcherArguments
    [IO.File]::WriteAllText($launcherScript, $generatedLauncher, [Text.Encoding]::ASCII)

    $processArguments = @{
        FilePath = "$env:SystemRoot\System32\WindowsPowerShell\v1.0\powershell.exe"
        ArgumentList = @("-NoLogo", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", ('"{0}"' -f $launcherScript))
        PassThru = $true
    }
    $launcherProcess = Start-Process @processArguments
    for ($attempt = 0; $attempt -lt 50 -and -not (Test-Path -LiteralPath $launcherPidFile); $attempt++) {
        [Threading.Thread]::Sleep(100)
    }
    if (-not (Test-Path -LiteralPath $launcherPidFile)) {
        throw "Generated launcher did not publish an owned PID"
    }
    $launcherChildPid = [int]([IO.File]::ReadAllText($launcherPidFile).Trim())
    $launcherChild = Get-Process -Id $launcherChildPid -ErrorAction Stop
    if (-not (Test-SamePath -Left $launcherChild.Path -Right $launcherBinary)) {
        throw "Generated launcher PID did not own the expected executable"
    }
    if (-not $launcherProcess.WaitForExit(10000)) {
        throw "Generated launcher did not wait for its child"
    }
    $launcherProcess.Refresh()
    if ($launcherProcess.ExitCode -ne 7) {
        throw "Generated launcher returned $($launcherProcess.ExitCode), expected child exit code 7"
    }
    if (Test-Path -LiteralPath $launcherPidFile) {
        throw "Generated launcher left a stale PID file"
    }
    if ([IO.File]::ReadAllText($launcherStdout) -notmatch "stdout-marker") {
        throw "Generated launcher did not capture stdout"
    }
    if ([IO.File]::ReadAllText($launcherStderr) -notmatch "stderr-marker") {
        throw "Generated launcher did not capture stderr separately"
    }
} finally {
    Get-ProcessesAtExactPath -Paths @((Join-Path $launcherRoot "conch.exe")) |
        Stop-Process -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $launcherRoot -Recurse -Force -ErrorAction SilentlyContinue
}
$env:CONCH_YES = "1"
try {
    . ([scriptblock]::Create($functions["Prompt-User"].Extent.Text))
    if (-not (Prompt-User "probe" -Default "Y")) {
        throw "Prompt-User ignored CONCH_YES for a yes-default prompt"
    }
    if (Prompt-User "probe" -Default "N") {
        throw "Prompt-User broke no-default handling under CONCH_YES"
    }
} finally {
    Remove-Item Env:\CONCH_YES -ErrorAction SilentlyContinue
}

Write-Host "install.ps1 PowerShell 5 regression checks passed."
