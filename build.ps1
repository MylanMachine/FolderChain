# Builds dist\FolderChain.exe (single file, no install needed).
# Requires Go 1.27+. Run from the project folder:  .\build.ps1
$ErrorActionPreference = 'Stop'
$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) { $env:Path = "$env:LOCALAPPDATA\Programs\go\bin;$env:Path" }
$env:Path = "$env:USERPROFILE\go\bin;$env:Path"

Set-Location $PSScriptRoot
go run ./tools/genicon
if (-not (Get-Command go-winres -ErrorAction SilentlyContinue)) { go install github.com/tc-hib/go-winres@latest }
go-winres make --arch amd64
go test ./internal/... -count=1
New-Item -ItemType Directory -Force dist | Out-Null
go build -trimpath -ldflags "-H windowsgui -s -w" -o dist\FolderChain.exe .
Get-Item dist\FolderChain.exe | Select-Object Name, Length, LastWriteTime
