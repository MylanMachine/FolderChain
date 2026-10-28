# Builds dist\FolderChain.exe (single file, no install needed).
# Requires Go 1.27+. Run from the project folder:  .\build.ps1
# If signing\metadata.json exists, the exe is also code-signed with Azure
# Artifact Signing (see SIGNING.md). Pass -RequireSigning to fail instead of
# producing an unsigned exe. Pass -Assets to regenerate the icons and exe
# resources (only needed after changing tools\genicon or winres\winres.json).
param([switch]$RequireSigning, [switch]$Assets, [switch]$SkipTests)
$ErrorActionPreference = 'Stop'
$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) { $env:Path = "$env:LOCALAPPDATA\Programs\go\bin;$env:Path" }
$env:Path = "$env:USERPROFILE\go\bin;$env:Path"
Set-Location $PSScriptRoot

# Smart App Control blocks every unsigned program, including the helper
# tools and test binaries Go compiles during a build.
$sac = (Get-ItemProperty 'HKLM:\SYSTEM\CurrentControlSet\Control\CI\Policy' -ErrorAction SilentlyContinue).VerifiedAndReputablePolicyState -eq 1

if ($Assets -or -not (Test-Path 'rsrc_windows_amd64.syso')) {
    if ($sac) { throw 'Smart App Control blocks the icon/resource generators on this PC; build on a PC without it, or use the committed assets.' }
    go run ./tools/genicon
    if (-not (Get-Command go-winres -ErrorAction SilentlyContinue)) { go install github.com/tc-hib/go-winres@latest }
    go-winres make --arch amd64
}
if ($SkipTests) {
    Write-Warning 'Tests skipped.'
} elseif ($sac) {
    Write-Warning 'Tests skipped: Smart App Control blocks unsigned test programs on this PC. Run them on a PC without it (or in GitHub Actions).'
} else {
    go test ./internal/... -count=1
    if ($LASTEXITCODE) { throw 'tests failed' }
}
New-Item -ItemType Directory -Force dist | Out-Null
$exe = Join-Path $PSScriptRoot 'dist\FolderChain.exe'
go build -trimpath -ldflags "-H windowsgui -s -w" -o $exe .
if ($LASTEXITCODE) { throw 'build failed' }

# ---- code signing ----
function Find-SignTool {
    $sdk = Get-ChildItem 'C:\Program Files (x86)\Windows Kits\10\bin' -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match '^10\.' } | Sort-Object { [version]$_.Name } -Descending
    foreach ($d in $sdk) {
        $p = Join-Path $d.FullName 'x64\signtool.exe'
        if (Test-Path $p) { return $p }
    }
    $c = Get-Command signtool -ErrorAction SilentlyContinue
    if ($c) { return $c.Source }
}

function Find-Dlib {
    if ($env:FC_SIGN_DLIB -and (Test-Path $env:FC_SIGN_DLIB)) { return $env:FC_SIGN_DLIB }
    $roots = @("$env:ProgramFiles", "${env:ProgramFiles(x86)}", "$env:LOCALAPPDATA", (Join-Path $PSScriptRoot 'signing\tools'))
    foreach ($r in $roots) {
        if (-not $r -or -not (Test-Path $r)) { continue }
        $hit = Get-ChildItem $r -Recurse -Filter Azure.CodeSigning.Dlib.dll -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match '\\x64\\' } | Select-Object -First 1
        if ($hit) { return $hit.FullName }
    }
}

$metadata = Join-Path $PSScriptRoot 'signing\metadata.json'
if (Test-Path $metadata) {
    $signtool = Find-SignTool
    $dlib = Find-Dlib
    if (-not $signtool) { throw 'signtool.exe not found. Install the Artifact Signing Client Tools (see SIGNING.md).' }
    if (-not $dlib) { throw 'Azure.CodeSigning.Dlib.dll not found. Install the Artifact Signing Client Tools (see SIGNING.md), or set FC_SIGN_DLIB.' }
    Write-Host "Signing with $signtool"
    & $signtool sign /v /fd SHA256 /tr 'http://timestamp.acs.microsoft.com' /td SHA256 /d 'FolderChain' /du 'https://github.com/MylanMachine/FolderChain' /dlib $dlib /dmdf $metadata $exe
    if ($LASTEXITCODE) { throw 'signing failed (run "az login" first; see SIGNING.md troubleshooting)' }
    & $signtool verify /pa /q $exe
    if ($LASTEXITCODE) { throw 'signature did not verify' }
    Write-Host 'Signed and verified.' -ForegroundColor Green
} elseif ($RequireSigning) {
    throw 'signing\metadata.json is missing; see SIGNING.md'
} else {
    Write-Warning 'Not signed: signing\metadata.json not found. Smart App Control will block this exe (see SIGNING.md).'
}

Get-Item $exe | Select-Object Name, Length, LastWriteTime
(Get-FileHash $exe -Algorithm SHA256).Hash.ToLower()
