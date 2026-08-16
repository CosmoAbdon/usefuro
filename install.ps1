# Furo installer for Windows — downloads the latest release and adds it to PATH.
#
#   client:  irm https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.ps1 | iex
#   server:  $env:FURO_INSTALL_SERVER=1; irm https://raw.githubusercontent.com/CosmoAbdon/usefuro/main/install.ps1 | iex
#
# Later, update with:  furo update
$ErrorActionPreference = "Stop"

$repo = "CosmoAbdon/usefuro"
$bin = if ($env:FURO_INSTALL_SERVER) { "furo-server" } else { "furo" }

$arch = $env:PROCESSOR_ARCHITECTURE.ToLower()
if ($arch -ne "amd64") {
  Write-Error "unsupported architecture: $arch (releases ship windows/amd64 only)"
}

$tag = (Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest").tag_name
$ver = $tag.TrimStart("v")
$asset = "${bin}_${ver}_windows_${arch}.zip"
$base = "https://github.com/$repo/releases/download/$tag"

$tmp = Join-Path $env:TEMP "furo-install-$([guid]::NewGuid().ToString('N').Substring(0,8))"
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  Write-Host "downloading $asset ..."
  Invoke-WebRequest -Uri "$base/$asset" -OutFile "$tmp\$asset"
  Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile "$tmp\checksums.txt"

  $want = (Select-String -Path "$tmp\checksums.txt" -Pattern ([regex]::Escape($asset))).Line.Split(" ")[0]
  $got = (Get-FileHash -Algorithm SHA256 "$tmp\$asset").Hash.ToLower()
  if ($want -ne $got) { Write-Error "checksum mismatch for $asset" }

  Expand-Archive -Path "$tmp\$asset" -DestinationPath $tmp -Force

  $dest = Join-Path $env:LOCALAPPDATA "Programs\furo"
  New-Item -ItemType Directory -Path $dest -Force | Out-Null
  Move-Item -Path "$tmp\$bin.exe" -Destination "$dest\$bin.exe" -Force

  $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
  if ($userPath -notlike "*$dest*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$dest", "User")
    Write-Host "added $dest to your user PATH (open a new terminal to pick it up)"
  }

  Write-Host "installed $bin $tag to $dest\$bin.exe"
  if ($bin -eq "furo") {
    Write-Host "next: furo login <token> --server <host>:7835   then   furo http 3000"
  } else {
    Write-Host "next: furo-server init   then   furo-server user add <name>; furo-server serve"
  }
}
finally {
  Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
