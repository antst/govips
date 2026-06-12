package vips

// Crash-hunt stress harness for the intermittent macOS/Apple Silicon
// SIGSEGV (https://github.com/antst/govips/issues/1, upstream
// davidbyttow/govips#356).
//
// The crash is bursty and machine-state dependent: it appears in
// windows of 2-3 consecutive full-suite runs and then goes dormant for
// dozens of runs of the identical command. This harness condenses the
// crash-correlated workload — heifsave threadpool encodes, find_trim
// (median/rank), webpsave, HEIC decodes, GC/finalizer churn, all under
// goroutine concurrency — so a crash window can be caught by looping
// one test instead of the whole suite:
//
//	GOVIPS_STRESS=1 go test ./vips/ -run TestStress_CrashHunt -v
//
// Knobs:
//
//	GOVIPS_STRESS_DURATION  run length per invocation (Go duration, default 60s)
//	GOVIPS_STRESS_WORKERS   concurrent workers (default GOMAXPROCS)
//
// Suggested hunt loop (zsh), including the MallocNanoZone variant that
// is known to matter for other mixed Go/C runtimes on macOS:
//
//	while :; do GOVIPS_STRESS=1 GOVIPS_STRESS_DURATION=90s go test ./vips/ -run TestStress_CrashHunt || break; done
//	while :; do MallocNanoZone=0 GOVIPS_STRESS=1 GOVIPS_STRESS_DURATION=90s go test ./vips/ -run TestStress_CrashHunt || break; done
//
// On a crash, macOS writes a report to ~/Library/Logs/DiagnosticReports/
// (vips.test-*.ips) — attach it to the issue.

import (
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func stressEnvDuration(name string, def time.Duration) time.Duration {
	if v := os.Getenv(name); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func TestStress_CrashHunt(t *testing.T) {
	if os.Getenv("GOVIPS_STRESS") != "1" {
		t.Skip("set GOVIPS_STRESS=1 to run the crash-hunt stress harness (issue #1)")
	}

	require.NoError(t, Startup(nil))

	duration := stressEnvDuration("GOVIPS_STRESS_DURATION", 60*time.Second)
	workers := runtime.GOMAXPROCS(0)
	if v := os.Getenv("GOVIPS_STRESS_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workers = n
		}
	}

	pngBuf, err := os.ReadFile(resources + "png-24bit.png")
	require.NoError(t, err)
	jpgBuf, err := os.ReadFile(resources + "jpg-24bit.jpg")
	require.NoError(t, err)
	heicBuf, err := os.ReadFile(resources + "heic-24bit.heic")
	require.NoError(t, err)
	trimBuf, err := os.ReadFile(resources + "find_trim.png")
	require.NoError(t, err)

	// Skip the heifsave cases gracefully when the encoder is absent.
	heifSave := func() bool {
		img, err := Black(1, 1)
		if err != nil {
			return false
		}
		defer img.Close()
		_, _, err = img.ExportHeif(NewHeifExportParams())
		return err == nil
	}()
	if !heifSave {
		t.Log("heifsave unavailable; heif-encode cases disabled")
	}

	deadline := time.Now().Add(duration)
	var ops, errs atomic.Int64
	var wg sync.WaitGroup

	t.Logf("crash hunt: %d workers for %s (heifsave=%v)", workers, duration, heifSave)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))

			// maybeClose leaves roughly half of the images to the GC
			// finalizer, mirroring the test suite's churn: concurrent
			// finalizer-driven unrefs are part of the suspected
			// trigger conditions.
			maybeClose := func(img *ImageRef) {
				if rng.Intn(2) == 0 {
					img.Close()
				}
			}

			for time.Now().Before(deadline) {
				ops.Add(1)
				switch rng.Intn(9) {
				case 0: // heifsave threadpool (crash flavor: heif/AVIF tests)
					if !heifSave {
						continue
					}
					img, err := NewImageFromBuffer(pngBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					if _, _, err := img.ExportHeif(NewHeifExportParams()); err != nil {
						errs.Add(1)
					}
					maybeClose(img)
				case 1: // find_trim → median/rank (crash flavor: FindTrim tests)
					img, err := NewImageFromBuffer(trimBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					if _, _, _, _, err := img.FindTrim(0, &Color{R: 255, G: 255, B: 255}); err != nil {
						errs.Add(1)
					}
					maybeClose(img)
				case 2: // webpsave (crash flavor: davidbyttow/govips#356)
					img, err := NewImageFromBuffer(jpgBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					if _, _, err := img.ExportWebp(NewWebpExportParams()); err != nil {
						errs.Add(1)
					}
					maybeClose(img)
				case 3: // resize vector paths + jpegsave
					img, err := NewImageFromBuffer(pngBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					if err := img.Resize(0.5, KernelLanczos3); err != nil {
						errs.Add(1)
					} else if _, _, err := img.ExportJpeg(NewJpegExportParams()); err != nil {
						errs.Add(1)
					}
					maybeClose(img)
				case 4: // libheif decode
					img, err := NewImageFromBuffer(heicBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					maybeClose(img)
				case 5: // thumbnail (sink + shrink-on-load paths)
					img, err := NewThumbnailFromBuffer(jpgBuf, 64, 64, InterestingCentre)
					if err != nil {
						errs.Add(1)
						continue
					}
					maybeClose(img)
				case 6: // GC pressure: drive finalizers concurrently with C work
					runtime.GC()
				case 8: // Join: regression for the input double-unref (#1).
					// Both images are left to finalizers; the second
					// must not be unref'd by the binding.
					a, err := NewImageFromBuffer(pngBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					b, err := NewImageFromBuffer(jpgBuf)
					if err != nil {
						errs.Add(1)
						a.Close()
						continue
					}
					if err := a.Join(b, DirectionHorizontal); err != nil {
						errs.Add(1)
					}
					maybeClose(a)
				case 7: // multi-image ops with finalizer-managed overlays:
					// the root cause of #1 was secondary ImageRefs being
					// collected mid-call (missing KeepAlive)
					img, err := NewImageFromBuffer(pngBuf)
					if err != nil {
						errs.Add(1)
						continue
					}
					overlay, err := NewImageFromBuffer(jpgBuf)
					if err != nil {
						errs.Add(1)
						img.Close()
						continue
					}
					if err := overlay.AddAlpha(); err != nil {
						errs.Add(1)
					} else if err := img.Composite(overlay, BlendModeOver, 0, 0); err != nil {
						errs.Add(1)
					}
					// Deliberately never Close the overlay: its cleanup
					// must be safe to run from the GC finalizer even
					// while other C calls are in flight.
					maybeClose(img)
				}
			}
		}(int64(w) + time.Now().UnixNano())
	}

	wg.Wait()
	runtime.GC()

	t.Logf("crash hunt finished: %d ops, %d soft errors, no crash", ops.Load(), errs.Load())
}
