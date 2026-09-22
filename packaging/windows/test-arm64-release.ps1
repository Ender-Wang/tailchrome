param(
  [Parameter(Mandatory = $true)]
  [string]$HelperPath,

  [Parameter(Mandatory = $true)]
  [string]$InstallerTestPath,

  [string]$ExpectedVersion = "",
  [string]$ExpectedSha256 = "",
  [string]$ReleaseTag = "",
  [string]$SourceSha = "",
  [string]$EvidencePath = ""
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Read-ExactBytes {
  param(
    [Parameter(Mandatory = $true)]
    [System.IO.Stream]$Stream,

    [Parameter(Mandatory = $true)]
    [int]$Length,

    [Parameter(Mandatory = $true)]
    [string]$Label
  )

  $buffer = New-Object byte[] $Length
  $offset = 0
  while ($offset -lt $Length) {
    $readTask = $Stream.ReadAsync($buffer, $offset, $Length - $offset)
    if (-not $readTask.Wait(15000)) {
      throw "Timed out reading $Label from the ARM64 helper."
    }
    $read = $readTask.Result
    if ($read -le 0) {
      throw "The ARM64 helper closed before returning $Label."
    }
    $offset += $read
  }
  return $buffer
}

function Read-NativeMessage {
  param(
    [Parameter(Mandatory = $true)]
    [System.IO.Stream]$Stream,

    [Parameter(Mandatory = $true)]
    [string]$Label
  )

  $prefix = Read-ExactBytes -Stream $Stream -Length 4 -Label "$Label length"
  $length = [BitConverter]::ToUInt32($prefix, 0)
  if ($length -eq 0 -or $length -gt 1048576) {
    throw "The ARM64 helper returned an invalid $Label length: $length."
  }
  $payload = Read-ExactBytes -Stream $Stream -Length ([int]$length) -Label "$Label payload"
  return [Text.Encoding]::UTF8.GetString($payload)
}

function Invoke-CheckedNativeVersion {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Path
  )

  $output = @(& $Path version 2>&1)
  $exitCode = $LASTEXITCODE
  if ($exitCode -ne 0) {
    throw "The final ARM64 helper version command exited with $exitCode."
  }
  return ($output -join "`n").Trim()
}

function Invoke-NativeMessagingHandshake {
  param(
    [Parameter(Mandatory = $true)]
    [string]$Path,

    [Parameter(Mandatory = $true)]
    [string]$Version
  )

  $startInfo = New-Object System.Diagnostics.ProcessStartInfo
  $startInfo.FileName = $Path
  $startInfo.UseShellExecute = $false
  $startInfo.RedirectStandardInput = $true
  $startInfo.RedirectStandardOutput = $true
  $process = New-Object System.Diagnostics.Process
  $process.StartInfo = $startInfo
  if (-not $process.Start()) {
    throw "The final ARM64 helper could not be started for native messaging."
  }
  $handshakeComplete = $false
  try {
    $payload = [Text.Encoding]::UTF8.GetBytes('{"cmd":"ping"}')
    $frame = New-Object byte[] (4 + $payload.Length)
    [Buffer]::BlockCopy([BitConverter]::GetBytes([uint32]$payload.Length), 0, $frame, 0, 4)
    [Buffer]::BlockCopy($payload, 0, $frame, 4, $payload.Length)
    $process.StandardInput.BaseStream.Write($frame, 0, $frame.Length)
    $process.StandardInput.BaseStream.Flush()
    $process.StandardInput.Close()

    $running = Read-NativeMessage -Stream $process.StandardOutput.BaseStream -Label "procRunning reply" |
      ConvertFrom-Json
    if ($running.cmd -ne "procRunning" -or
        $null -eq $running.procRunning -or
        $running.procRunning.version -ne $Version -or
        [int]$running.procRunning.port -le 0) {
      throw "The final ARM64 helper returned an invalid procRunning reply."
    }
    $pong = Read-NativeMessage -Stream $process.StandardOutput.BaseStream -Label "ping reply" |
      ConvertFrom-Json
    if ($pong.cmd -ne "pong") {
      throw "The final ARM64 helper did not complete the framed ping handshake."
    }
    if (-not $process.WaitForExit(15000)) {
      $process.Kill()
      throw "The final ARM64 helper did not exit after the framed handshake."
    }
    if ($process.ExitCode -ne 0) {
      throw "The final ARM64 native messaging process exited with $($process.ExitCode)."
    }
    $handshakeComplete = $true
  } finally {
    if (-not $handshakeComplete) {
      if (-not $process.HasExited) {
        $process.Kill()
        if (-not $process.WaitForExit(5000)) {
          throw "The final ARM64 helper remained live after a failed handshake and could not be reaped."
        }
      } else {
        $process.WaitForExit()
      }
    }
    $process.Dispose()
  }
}

function Invoke-StockInstallerFixture {
  param(
    [Parameter(Mandatory = $true)]
    [string]$PowerShellPath,

    [Parameter(Mandatory = $true)]
    [string]$Label
  )

  if (-not (Test-Path -LiteralPath $PowerShellPath -PathType Leaf)) {
    throw "The required stock Windows PowerShell 5.1 shell is missing: $PowerShellPath"
  }
  $startInfo = New-Object System.Diagnostics.ProcessStartInfo
  $startInfo.FileName = $PowerShellPath
  $startInfo.Arguments = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$InstallerTestPath`""
  $startInfo.UseShellExecute = $false
  $startInfo.EnvironmentVariables["TAILCHROME_EXPECTED_NATIVE_ARCHITECTURE"] = "arm64"
  $process = [System.Diagnostics.Process]::Start($startInfo)
  if ($null -eq $process) {
    throw "Could not start the stock PowerShell 5.1 $Label installer fixture."
  }
  try {
    if (-not $process.WaitForExit(120000)) {
      $process.Kill()
      if (-not $process.WaitForExit(5000)) {
        throw "The stock PowerShell 5.1 $Label installer fixture remained live after timeout."
      }
      throw "The stock PowerShell 5.1 $Label installer fixture timed out."
    }
    if ($process.ExitCode -ne 0) {
      throw "The stock PowerShell 5.1 $Label installer fixture exited with $($process.ExitCode)."
    }
  } finally {
    if (-not $process.HasExited) {
      $process.Kill()
      if (-not $process.WaitForExit(5000)) {
        throw "The stock PowerShell 5.1 $Label installer fixture could not be reaped."
      }
    }
    $process.Dispose()
  }
}

if ($env:PROCESSOR_ARCHITECTURE -cne "ARM64" -or $env:RUNNER_ARCH -cne "ARM64") {
  throw "The ARM64 smoke gate requires a native ARM64 runner; observed PROCESSOR_ARCHITECTURE=$env:PROCESSOR_ARCHITECTURE RUNNER_ARCH=$env:RUNNER_ARCH."
}
$helper = (Resolve-Path -LiteralPath $HelperPath -ErrorAction Stop).Path
$InstallerTestPath = (Resolve-Path -LiteralPath $InstallerTestPath -ErrorAction Stop).Path
$initialHash = (Get-FileHash -LiteralPath $helper -Algorithm SHA256).Hash.ToLowerInvariant()
if (-not [string]::IsNullOrWhiteSpace($ExpectedSha256) -and $initialHash -cne $ExpectedSha256.ToLowerInvariant()) {
  throw "The final ARM64 helper hash does not match the package output."
}
$helperVersion = Invoke-CheckedNativeVersion -Path $helper
if (-not [string]::IsNullOrWhiteSpace($ExpectedVersion) -and $helperVersion -cne $ExpectedVersion) {
  throw "The final ARM64 helper version '$helperVersion' does not equal '$ExpectedVersion'."
}
if (-not [string]::IsNullOrWhiteSpace($ExpectedVersion) -and
    -not [string]::IsNullOrWhiteSpace($ReleaseTag) -and
    $ExpectedVersion -cne $ReleaseTag) {
  throw "The smoke version contract is inconsistent: ExpectedVersion must equal ReleaseTag."
}

Invoke-NativeMessagingHandshake -Path $helper -Version $helperVersion
$afterHandshakeHash = (Get-FileHash -LiteralPath $helper -Algorithm SHA256).Hash.ToLowerInvariant()
if ($afterHandshakeHash -cne $initialHash) {
  throw "The final ARM64 helper changed during native messaging smoke."
}

$systemPowerShell = Join-Path $env:SystemRoot "System32\WindowsPowerShell\v1.0\powershell.exe"
$emulatedPowerShell = Join-Path $env:SystemRoot "SysWOW64\WindowsPowerShell\v1.0\powershell.exe"
Invoke-StockInstallerFixture -PowerShellPath $systemPowerShell -Label "native ARM64"
Invoke-StockInstallerFixture -PowerShellPath $emulatedPowerShell -Label "emulated-shell ARM64 selection"

if (-not [string]::IsNullOrWhiteSpace($EvidencePath)) {
  $evidence = [ordered]@{
    schemaVersion = 1
    result = "clean"
    runnerLabel = "windows-11-arm"
    runnerOS = $env:RUNNER_OS
    runnerArchitecture = $env:RUNNER_ARCH
    processorArchitecture = $env:PROCESSOR_ARCHITECTURE
    sourceSha = $SourceSha
    releaseTag = $ReleaseTag
    helperVersion = $helperVersion
    rawExe = @{ name = "tailscale-browser-ext-windows-arm64.exe"; sha256 = $afterHandshakeHash }
    nativeMessagingHandshake = "passed"
    installerTests = "passed"
    installerShells = @("powershell.exe-5.1-native-arm64", "powershell.exe-5.1-emulated-shell")
  }
  $evidence | ConvertTo-Json -Depth 5 | Out-File -LiteralPath $EvidencePath -Encoding utf8
}
