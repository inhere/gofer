# gofer worker installer — served by the gofer server at {{SERVER}}.
#
# It installs the gofer binary of this server's version, then asks the server to admit
# this machine as a worker: the script prints a pairing code, a person approves it in the
# console (Runners), and the worker is registered as a logon task.
#
# This script carries no credential. Everything it downloads is verified against the
# sha256 the server publishes for it; a missing or wrong checksum stops the install.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$Server = '{{SERVER}}'

function Fail([string]$Message) {
	Write-Host "gofer install: $Message"
	exit 1
}

switch -Regex ($env:PROCESSOR_ARCHITECTURE) {
	'AMD64' { $arch = 'amd64' }
	'ARM64' { $arch = 'arm64' }
	default { Fail "unsupported architecture $env:PROCESSOR_ARCHITECTURE" }
}

$manifestUrl = "$Server/install/manifest?os=windows&arch=$arch"
try {
	$manifest = Invoke-RestMethod -Uri $manifestUrl -UseBasicParsing
} catch {
	Fail "cannot read ${manifestUrl}: $($_.Exception.Message)"
}
if (-not $manifest.url) { Fail 'the manifest has no download url' }
if (-not $manifest.name) { Fail 'the manifest has no file name' }
if (-not $manifest.sha256) {
	Fail "the manifest has no sha256 for windows/$arch: refusing to install an unverified download"
}

$tmp = Join-Path $env:TEMP ('gofer-install-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
	$file = Join-Path $tmp $manifest.name
	try {
		Invoke-WebRequest -Uri $manifest.url -OutFile $file -UseBasicParsing
	} catch {
		Fail "download $($manifest.url) failed: $($_.Exception.Message)"
	}

	# The published checksum is the binary's, so an archive is unpacked before hashing.
	if ($manifest.name -like '*.zip') {
		$unpacked = Join-Path $tmp 'unpacked'
		Expand-Archive -Path $file -DestinationPath $unpacked -Force
		$bin = Get-ChildItem -Path $unpacked -Filter 'gofer.exe' -Recurse -File | Select-Object -First 1
	} else {
		$bin = Get-Item -Path $file
	}
	if (-not $bin) { Fail "the download $($manifest.name) contains no gofer.exe" }

	$got = (Get-FileHash -Path $bin.FullName -Algorithm SHA256).Hash.ToLower()
	$want = $manifest.sha256.ToLower()
	if ($got -ne $want) {
		Fail "sha256 mismatch for $($manifest.name): got $got, want $want — the download is corrupt or the source serves another binary"
	}

	$dir = Join-Path $env:LOCALAPPDATA 'gofer\bin'
	New-Item -ItemType Directory -Path $dir -Force | Out-Null
	$target = Join-Path $dir 'gofer.exe'
	Copy-Item -Path $bin.FullName -Destination $target -Force
	Write-Host "installed $target"

	& $target worker join --server $Server --yes
	if ($LASTEXITCODE -ne 0) {
		Fail "worker join failed (nothing was registered; rerun: $target worker join --server $Server)"
	}

	& $target worker service register --start
	if ($LASTEXITCODE -ne 0) {
		Fail "registered the worker, but the logon task could not be set up: run $target worker service register --start"
	}
} finally {
	Remove-Item -Path $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

if (($env:PATH -split ';') -notcontains (Join-Path $env:LOCALAPPDATA 'gofer\bin')) {
	Write-Host "note: $(Join-Path $env:LOCALAPPDATA 'gofer\bin') is not in PATH — add it to run gofer without the full path"
}
