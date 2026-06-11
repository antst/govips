# Quickstart: Streaming I/O

## Stream-Load from io.Reader

```go
package main

import (
    "log"
    "os"

    "github.com/davidbyttow/govips/v2/vips"
)

func main() {
    vips.Startup(nil)
    defer vips.Shutdown()

    // Open a file as io.Reader (also implements io.Seeker)
    f, err := os.Open("input.heic")
    if err != nil {
        log.Fatal(err)
    }
    defer f.Close()

    // Load via streaming — no full-file buffer in Go memory
    image, err := vips.LoadImageFromReader(f, nil)
    if err != nil {
        log.Fatal(err)
    }
    defer image.Close()

    log.Printf("Loaded %dx%d %s image via streaming",
        image.Width(), image.Height(), vips.ImageTypes[image.Format()])
}
```

## Stream-Save to io.Writer

```go
    // After loading and processing an image...
    out, err := os.Create("output.jpg")
    if err != nil {
        log.Fatal(err)
    }
    defer out.Close()

    // Save via streaming — encoded bytes written directly to file
    err = image.SaveToWriter(out, vips.ImageTypeJPEG, &vips.ExportParams{
        Quality: 85,
    })
    if err != nil {
        log.Fatal(err)
    }
```

## End-to-End Streaming Pipeline

```go
func convertHEICtoJPEG(input io.Reader, output io.Writer) error {
    vips.Startup(nil)

    // Stream-load (reader is released after decode)
    image, err := vips.LoadImageFromReader(input, nil)
    if err != nil {
        return fmt.Errorf("load: %w", err)
    }
    defer image.Close()

    // Process
    if err := image.AutoRotate(); err != nil {
        return fmt.Errorf("rotate: %w", err)
    }

    // Stream-save (writer receives chunks as they're encoded)
    if err := image.SaveToWriter(output, vips.ImageTypeJPEG, nil); err != nil {
        return fmt.Errorf("save: %w", err)
    }

    return nil
}
```

## Controlling Pipe Buffer Limit

For non-seekable readers (e.g., `http.Request.Body`), libvips
buffers header data up to ~1 GB by default. To reduce this:

```go
    vips.Startup(nil)
    vips.SetPipeReadLimit(100 * 1024 * 1024) // 100 MB limit
```

## Validation Checklist

- [ ] `go build ./...` compiles without errors
- [ ] `make test` passes all tests
- [ ] Stream-load a JPEG via `os.File` → correct dimensions
- [ ] Stream-load a HEIC via `os.File` (seekable) → correct format
- [ ] Stream-load via `io.Pipe` reader (non-seekable) → succeeds
- [ ] Stream-save to `bytes.Buffer` → output matches `Export*`
- [ ] End-to-end: HEIC reader → JPEG writer → valid output
- [ ] Concurrent streaming loads from multiple goroutines → no races
- [ ] `AssertNoLeaks` passes after all streaming operations
