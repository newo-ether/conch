$ErrorActionPreference = 'Stop'
$installer = Join-Path (Split-Path -Parent $PSScriptRoot) 'scripts/install.ps1'
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$errors)
if ($errors.Count) { throw 'Installer parse failed' }
foreach ($name in @('Protect-SecretFile', 'Protect-ServiceSecrets', 'Write-AtomicConfig')) {
    $fn = $ast.FindAll({ param($node)
        $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq $name
    }, $true) | Select-Object -First 1
    if (-not $fn) { throw "Missing helper: $name" }
    . ([scriptblock]::Create($fn.Extent.Text))
}
function Assert-PrivateAcl($acl) {
    if (-not $acl.AreAccessRulesProtected) { throw 'Secret ACL still inherits' }
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    if ($rules.Count -ne 2) { throw 'Expected exactly two allowed principals' }
    foreach ($rule in $rules) {
        if ($rule.IdentityReference.Value -notin @('S-1-5-18', 'S-1-5-32-544') -or
            $rule.AccessControlType -ne 'Allow') { throw 'Unexpected principal' }
    }
}
$fixture = Join-Path $env:TEMP ('conch-secret-acl-' + [guid]::NewGuid().ToString('N'))
$keyName = 'Software\ConchSecurityFixture\' + [guid]::NewGuid().ToString('N')
$keyPath = 'HKCU:\' + $keyName
New-Item -ItemType Directory -Path $fixture | Out-Null
try {
    $target = Join-Path $fixture 'env.txt'
    foreach ($content in @('isolated first value', 'isolated replacement value')) {
        Write-AtomicConfig $content $target
        if ([IO.File]::ReadAllText($target) -cne $content) { throw 'Config content changed' }
        Assert-PrivateAcl (Get-Acl -LiteralPath $target)
        if (@(Get-ChildItem -LiteralPath $fixture -File).Count -ne 1) { throw 'Secret temporary file retained' }
    }
    Protect-ServiceSecrets $keyPath
    Protect-ServiceSecrets $keyPath
    $key = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey($keyName, $false)
    try { Assert-PrivateAcl ($key.GetAccessControl([Security.AccessControl.AccessControlSections]::Access)) }
    finally { $key.Dispose() }
    Write-Output 'PASS: fresh/replaced secret files, private registry, idempotent ACL, temporary cleanup'
} finally {
    # Remove only the exact random fixture created above; no computed recursive filesystem deletion.
    if (Test-Path -LiteralPath (Join-Path $fixture 'env.txt')) {
        Remove-Item -LiteralPath (Join-Path $fixture 'env.txt') -Force
    }
    [IO.Directory]::Delete($fixture, $false)
    [Microsoft.Win32.Registry]::CurrentUser.DeleteSubKeyTree($keyName, $false)
}
