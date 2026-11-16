# Runs the Windows test binaries that ci/windows-build.sh made, on Windows: the py, aio,
# asgi (inline and with Options.LoopThread), wsgi and workers tests, aio's and asgi's with
# the binary wheels in DIR\run\wheels. PowerShell, so that it
# also runs in a Windows Server Core container, which has no bash.
#
#   powershell -File ci/windows-run.ps1 DIR\run
param([Parameter(Mandatory = $true)][string]$Run)
$ErrorActionPreference = "Stop"
# aio's and asgi's tests load the binary wheels ci/windows-build.sh put in DIR\run\wheels
# (internal/testwheels). Without them those tests would skip, so a missing directory fails.
$wheels = Join-Path (Resolve-Path $Run) "wheels"
if (-not (Test-Path (Join-Path $wheels "*.whl"))) {
    Write-Host "== no wheels in $wheels (ci/windows-build.sh puts them there)"
    exit 1
}
$env:PYTHAGO_TEST_WHEELS = $wheels
$failed = @()
foreach ($t in @("py", "aio", "asgi", "asgi:1", "wsgi", "workers")) {
    $pkg, $loop = $t.Split(":")
    if (-not $loop) { $loop = "0" }
    $env:PYTHAGO_ASGI_LOOP_THREAD = $loop
    Write-Host "== $pkg tests$(if ($loop -eq '1') { ', loop thread (Options.LoopThread)' })"
    Push-Location $Run
    try {
        # One token: PowerShell splits a bare -test.timeout at the dot ("flag provided but
        # not defined: -test").
        & ".\$pkg.test.exe" "-test.timeout=15m"
        if ($LASTEXITCODE -ne 0) { $failed += $t }
    } finally {
        Pop-Location
    }
}
if ($failed.Count -gt 0) {
    Write-Host "== failed: $($failed -join ', ')"
    exit 1
}
Write-Host "== windows tests passed"
