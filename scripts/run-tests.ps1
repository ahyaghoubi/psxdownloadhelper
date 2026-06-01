# Build + execute Go test binaries from a known workspace path.
#
# Some Windows installs (Defender Application Control / WDAC) block test
# binaries that `go test` silently emits into obscured temp paths. Building
# them explicitly into ./.testbin/ sidesteps that policy. We also build
# every binary first, then pause briefly, then execute — that gives WDAC
# enough time to finish its reputation lookup before launch.
#
# Usage:
#   powershell -File scripts/run-tests.ps1                                   # all packages
#   powershell -File scripts/run-tests.ps1 ./internal/proxy/...              # subset
#   powershell -File scripts/run-tests.ps1 -- -test.v ./internal/proxy/...   # forward flags
#
# Arguments after a literal `--` are forwarded verbatim to each test binary.

# We intentionally do NOT set $ErrorActionPreference = 'Stop'. Native command
# stderr in PowerShell is delivered as ErrorRecord objects; with 'Stop' that
# would turn every slog/stderr line into a terminating exception.
$ErrorActionPreference = 'Continue'

$packages = @()
$passthrough = @()
$mode = 'packages'
foreach ($a in $args) {
    if ($mode -eq 'packages' -and $a -eq '--') {
        $mode = 'passthrough'
        continue
    }
    if ($mode -eq 'packages') { $packages += $a } else { $passthrough += $a }
}
if ($packages.Count -eq 0) { $packages = @('./...') }
if ($passthrough.Count -eq 0) { $passthrough = @('-test.timeout=120s') }

$repo = (Resolve-Path "$PSScriptRoot/..").Path
$binDir = Join-Path $repo '.testbin'
if (-not (Test-Path $binDir)) { New-Item -ItemType Directory -Path $binDir | Out-Null }

$pkgList = & go list @packages
if ($LASTEXITCODE -ne 0 -or -not $pkgList) {
    Write-Host "no packages matched: $packages" -ForegroundColor Red
    exit 1
}

# Build phase: compile every test binary up front. Capture each package's
# source directory so we can run the binary with the correct cwd (tests
# that read testdata/* assume the package dir).
$jobs = @()
foreach ($pkg in $pkgList) {
    $testFiles = & go list -f '{{.TestGoFiles}}{{.XTestGoFiles}}' $pkg
    if ($testFiles -eq '[][]') { continue }
    $name = ($pkg -split '/')[-1]
    $exe  = Join-Path $binDir "$name.test.exe"
    $dir  = & go list -f '{{.Dir}}' $pkg

    Write-Host "==> Building $pkg" -ForegroundColor Cyan
    Remove-Item -Force $exe -ErrorAction SilentlyContinue
    & go test -c -o $exe $pkg
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $exe)) {
        Write-Host "    build failed: $pkg" -ForegroundColor Red
        $jobs += [pscustomobject]@{ Pkg=$pkg; Exe=$exe; Dir=$dir; BuildOK=$false }
        continue
    }
    $jobs += [pscustomobject]@{ Pkg=$pkg; Exe=$exe; Dir=$dir; BuildOK=$true }
}

# Pre-warm Defender / WDAC. Computing SHA-256 on each freshly-built
# binary forces a synchronous AV scan, which is much more reliable than
# waiting for the async reputation lookup that fires on first exec.
if ($jobs.Count -gt 0) {
    Write-Host "==> Built $($jobs.Count) test binaries; pre-warming Defender (hashing + Unblock-File)" -ForegroundColor DarkGray
    foreach ($j in $jobs) {
        if (-not $j.BuildOK) { continue }
        try {
            Unblock-File -LiteralPath $j.Exe -ErrorAction SilentlyContinue
            Get-FileHash -Algorithm SHA256 -LiteralPath $j.Exe | Out-Null
        } catch {}
    }
    Start-Sleep -Seconds 3
}

$failed = @()
foreach ($j in $jobs) {
    if (-not $j.BuildOK) {
        $failed += $j.Pkg
        continue
    }

    Write-Host "==> Running  $($j.Pkg)" -ForegroundColor Cyan
    $attempt = 0
    $maxAttempts = 6
    $rc = -1
    while ($attempt -lt $maxAttempts) {
        $attempt++
        $launchErr = $null
        Push-Location $j.Dir
        try {
            # Merge stderr to stdout so all test output is visible.
            # We deliberately don't pipe through ForEach-Object because that
            # converts ErrorRecord (stderr) entries in ways that mask exit
            # codes from native commands.
            & $j.Exe @passthrough *>&1 | Out-Host
            $rc = $LASTEXITCODE
        } catch {
            $rc = -1
            $launchErr = $_.Exception.Message
        } finally {
            Pop-Location
        }

        if ($rc -eq 0) { break }

        # Distinguish "WDAC blocked the launch" from a real test failure.
        # PowerShell surfaces the WDAC block either as an exception (caught
        # above) or as exit code -1073741502 (0xC0000428). Anything else
        # we treat as an actual test failure and stop retrying.
        $blocked = $false
        if ($launchErr) {
            if ($launchErr -like '*Application Control*' -or
                $launchErr -like '*failed to run*' -or
                $launchErr -like '*blocked*') { $blocked = $true }
        }
        if ($rc -eq -1073741502) { $blocked = $true }

        if (-not $blocked) { break }
        if ($attempt -ge $maxAttempts) { break }
        Write-Host "    (Defender blocked launch; sleeping 10s, attempt $attempt/$maxAttempts)" -ForegroundColor Yellow
        Start-Sleep -Seconds 10
    }

    # If WDAC kept blocking our prebuilt binary, fall back to plain
    # `go test` — Defender's blocklist is hash-based and the binary that
    # `go test` produces internally has a different hash from ours.
    if ($rc -ne 0 -and $blocked) {
        Write-Host "    Defender kept blocking $($j.Exe); falling back to 'go test $($j.Pkg)'" -ForegroundColor Yellow
        & go test -timeout 120s $j.Pkg
        $rc = $LASTEXITCODE
    }

    if ($rc -ne 0) { $failed += $j.Pkg }
}

if ($failed.Count -gt 0) {
    Write-Host ""
    Write-Host "FAIL ($($failed.Count) package(s)):" -ForegroundColor Red
    foreach ($p in $failed) { Write-Host "  $p" -ForegroundColor Red }
    exit 1
}

Write-Host ""
Write-Host "PASS (all $($jobs.Count) package(s) with tests)" -ForegroundColor Green
