param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^v[0-9]+\.[0-9]+\.[0-9]+$')]
    [string]$Version
)

$ErrorActionPreference = "Stop"
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$dirty = @(& git -C $RepoRoot status --porcelain --untracked-files=all)
if ($LASTEXITCODE -ne 0) {
    throw "Unable to inspect the release worktree"
}
if ($dirty.Count -gt 0) {
    throw "Release builds require a clean worktree"
}
$Revision = (& git -C $RepoRoot rev-parse HEAD).Trim()
$BuildTime = (& git -C $RepoRoot show -s --format=%cI HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or -not $Revision -or -not $BuildTime) {
    throw "Unable to read deterministic build metadata from git"
}

$DistRoot = Join-Path $RepoRoot "dist"
$OutputDir = Join-Path $DistRoot $Version
$resolvedDist = [IO.Path]::GetFullPath($DistRoot)
$resolvedOutput = [IO.Path]::GetFullPath($OutputDir)
if (-not $resolvedOutput.StartsWith($resolvedDist + [IO.Path]::DirectorySeparatorChar)) {
    throw "Refusing to clean output outside dist: $resolvedOutput"
}
if (Test-Path -LiteralPath $resolvedOutput) {
    Remove-Item -LiteralPath $resolvedOutput -Recurse -Force
}
New-Item -ItemType Directory -Path $resolvedOutput -Force | Out-Null

$ldflags = @(
    "-s",
    "-w",
    "-X github.com/newo-ether/conch/buildinfo.Version=$Version",
    "-X github.com/newo-ether/conch/buildinfo.Revision=$Revision",
    "-X github.com/newo-ether/conch/buildinfo.BuildTime=$BuildTime"
) -join " "

$targets = @(
    @{ Name = "conch-linux-arm64";             OS = "linux";   Arch = "arm64"; Package = "." },
    @{ Name = "conch-linux-amd64";             OS = "linux";   Arch = "amd64"; Package = "." },
    @{ Name = "conch-windows-amd64.exe";       OS = "windows"; Arch = "amd64"; Package = "." },
    @{ Name = "conch-mcp-linux-arm64";         OS = "linux";   Arch = "arm64"; Package = "./cmd/mcp" },
    @{ Name = "conch-mcp-linux-amd64";         OS = "linux";   Arch = "amd64"; Package = "./cmd/mcp" },
    @{ Name = "conch-mcp-windows-amd64.exe";   OS = "windows"; Arch = "amd64"; Package = "./cmd/mcp" }
)

$oldGOOS = $env:GOOS
$oldGOARCH = $env:GOARCH
$oldCGO = $env:CGO_ENABLED
try {
    Push-Location $RepoRoot
    foreach ($target in $targets) {
        $env:GOOS = $target.OS
        $env:GOARCH = $target.Arch
        $env:CGO_ENABLED = "0"
        $output = Join-Path $resolvedOutput $target.Name
        & go build -trimpath -buildvcs=false -ldflags $ldflags -o $output $target.Package
        if ($LASTEXITCODE -ne 0) {
            throw "Build failed: $($target.Name)"
        }
        if ((Get-Item -LiteralPath $output).Length -lt 1MB) {
            throw "Build output is unexpectedly small: $($target.Name)"
        }
    }
} finally {
    Pop-Location
    $env:GOOS = $oldGOOS
    $env:GOARCH = $oldGOARCH
    $env:CGO_ENABLED = $oldCGO
}

$checksumLines = foreach ($target in $targets) {
    $path = Join-Path $resolvedOutput $target.Name
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $path).Hash.ToLowerInvariant()
    "$hash  $($target.Name)"
}
$checksumPath = Join-Path $resolvedOutput "checksums.txt"
$checksumContent = [string]::Join("`n", $checksumLines) + "`n"
[IO.File]::WriteAllText(
    $checksumPath,
    $checksumContent,
    [Text.UTF8Encoding]::new($false)
)

$windowsServer = Join-Path $resolvedOutput "conch-windows-amd64.exe"
$windowsMcp = Join-Path $resolvedOutput "conch-mcp-windows-amd64.exe"
foreach ($binary in @($windowsServer, $windowsMcp)) {
    $reported = & $binary --version
    if ($LASTEXITCODE -ne 0 -or $reported -notmatch [Regex]::Escape($Version)) {
        throw "Version smoke test failed: $binary reported '$reported'"
    }
}

Write-Host "Release assets ready: $resolvedOutput"
Write-Host "Revision: $Revision"
Write-Host "Build time: $BuildTime"
Write-Host "Checksums: $checksumPath"
