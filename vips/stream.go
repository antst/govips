package vips

// Streaming I/O via VipsSourceCustom/VipsTargetCustom.
//
// libvips worker threads pull/push bytes through C trampolines (stream.c)
// that dispatch to the exported Go callbacks below via integer handles in
// a global registry. Holding a registry reference also keeps the Go
// reader/writer alive while the C side may still call back into it.

// #include "stream.h"
import "C"

import (
	"errors"
	"fmt"
	"io"
	"math"
	"runtime"
	"sync"
	"unsafe"
)

// sourceEntry is the registry entry for one streaming load. The mutex
// serializes callback invocations so callers never need thread-safe
// readers, even though libvips may call from multiple worker threads.
type sourceEntry struct {
	reader  io.Reader
	seeker  io.Seeker // non-nil only when reader implements io.Seeker
	mu      sync.Mutex
	lastErr error
}

// targetEntry is the registry entry for one streaming save.
type targetEntry struct {
	writer  io.Writer
	mu      sync.Mutex
	lastErr error
}

func (e *sourceEntry) takeErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

func (e *targetEntry) takeErr() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastErr
}

// streamCallbacks maps integer handles to active source/target entries.
// The registry mutex only guards the maps; per-entry mutexes guard the
// actual I/O so the global lock is never held during a Read/Write.
var streamCallbacks = struct {
	sync.Mutex
	sources    map[int]*sourceEntry
	targets    map[int]*targetEntry
	nextHandle int
}{
	sources: make(map[int]*sourceEntry),
	targets: make(map[int]*targetEntry),
}

// allocStreamHandle returns the next free handle. Handles cross the CGo
// boundary as C int (via GLib's GINT_TO_POINTER), so they must stay
// within int32 range: wrap instead of overflowing, and skip any handle
// that is still registered after a wrap. The caller must hold the
// streamCallbacks lock.
func allocStreamHandle() int {
	for {
		streamCallbacks.nextHandle++
		if streamCallbacks.nextHandle > math.MaxInt32 {
			streamCallbacks.nextHandle = 1
		}
		h := streamCallbacks.nextHandle
		if _, live := streamCallbacks.sources[h]; live {
			continue
		}
		if _, live := streamCallbacks.targets[h]; live {
			continue
		}
		return h
	}
}

func registerSource(r io.Reader) (int, *sourceEntry) {
	entry := &sourceEntry{reader: r}
	if s, ok := r.(io.Seeker); ok {
		entry.seeker = s
	}

	streamCallbacks.Lock()
	defer streamCallbacks.Unlock()
	handle := allocStreamHandle()
	streamCallbacks.sources[handle] = entry
	return handle, entry
}

func deregisterSource(handle int) {
	streamCallbacks.Lock()
	defer streamCallbacks.Unlock()
	delete(streamCallbacks.sources, handle)
}

func lookupSource(handle int) *sourceEntry {
	streamCallbacks.Lock()
	defer streamCallbacks.Unlock()
	return streamCallbacks.sources[handle]
}

func registerTarget(w io.Writer) (int, *targetEntry) {
	entry := &targetEntry{writer: w}

	streamCallbacks.Lock()
	defer streamCallbacks.Unlock()
	handle := allocStreamHandle()
	streamCallbacks.targets[handle] = entry
	return handle, entry
}

func deregisterTarget(handle int) {
	streamCallbacks.Lock()
	defer streamCallbacks.Unlock()
	delete(streamCallbacks.targets, handle)
}

func lookupTarget(handle int) *targetEntry {
	streamCallbacks.Lock()
	defer streamCallbacks.Unlock()
	return streamCallbacks.targets[handle]
}

//export goSourceReadCb
func goSourceReadCb(handle C.int, buffer unsafe.Pointer, length C.gint64) C.gint64 {
	if length <= 0 {
		return 0
	}
	buf := unsafe.Slice((*byte)(buffer), int(length))
	return C.gint64(sourceRead(int(handle), buf))
}

//export goSourceSeekCb
func goSourceSeekCb(handle C.int, offset C.gint64, whence C.int) C.gint64 {
	return C.gint64(sourceSeek(int(handle), int64(offset), int(whence)))
}

//export goTargetWriteCb
func goTargetWriteCb(handle C.int, data unsafe.Pointer, length C.gint64) C.gint64 {
	if length <= 0 {
		return 0
	}
	buf := unsafe.Slice((*byte)(data), int(length))
	return C.gint64(targetWrite(int(handle), buf))
}

//export goTargetEndCb
func goTargetEndCb(handle C.int) C.int {
	return C.int(targetEnd(int(handle)))
}

func sourceRead(handle int, buf []byte) int64 {
	entry := lookupSource(handle)
	if entry == nil {
		return -1
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	for {
		n, err := entry.reader.Read(buf)
		if n > 0 {
			// A non-EOF error alongside n>0 will surface on the next call.
			return int64(n)
		}
		if errors.Is(err, io.EOF) {
			return 0
		}
		if err != nil {
			entry.lastErr = err
			return -1
		}
		// (0, nil) is allowed by the io.Reader contract; retry rather
		// than returning 0, which libvips would treat as EOF.
	}
}

func sourceSeek(handle int, offset int64, whence int) int64 {
	entry := lookupSource(handle)
	if entry == nil || entry.seeker == nil {
		return -1
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	// libvips whence values are SEEK_SET/SEEK_CUR/SEEK_END, which match
	// io.SeekStart/io.SeekCurrent/io.SeekEnd.
	pos, err := entry.seeker.Seek(offset, whence)
	if err != nil {
		entry.lastErr = err
		return -1
	}
	return pos
}

func targetWrite(handle int, buf []byte) int64 {
	entry := lookupTarget(handle)
	if entry == nil {
		return -1
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	n, err := entry.writer.Write(buf)
	if err != nil {
		entry.lastErr = err
		return -1
	}
	return int64(n)
}

func targetEnd(handle int) int {
	entry := lookupTarget(handle)
	if entry == nil {
		return -1
	}

	entry.mu.Lock()
	defer entry.mu.Unlock()

	if entry.lastErr != nil {
		return -1
	}
	return 0
}

// LoadImageFromReader loads an image from the given io.Reader using
// libvips streaming (VipsSourceCustom). If r also implements io.Seeker,
// seek is exposed to libvips for efficient random-access loading.
// Otherwise libvips uses sequential mode with automatic header buffering
// (see SetPipeReadLimit).
//
// The reader is consumed during this call and is not retained after
// LoadImageFromReader returns. Callers may close their reader immediately
// after this function returns. The returned ImageRef has no buffer
// backing: the compressed input is never held in Go memory.
//
// params may be nil for default import settings.
func LoadImageFromReader(r io.Reader, params *ImportParams) (*ImageRef, error) {
	if r == nil {
		return nil, errors.New("reader is nil")
	}
	if err := startupIfNeeded(); err != nil {
		return nil, err
	}
	if params == nil {
		params = NewImportParams()
	}

	incOpCounter("load_source")

	handle, entry := registerSource(r)
	defer deregisterSource(handle)

	source := C.create_source_custom(C.int(handle), C.int(boolToInt(entry.seeker != nil)))
	if source == nil {
		return nil, handleVipsError()
	}
	// The source is released here, immediately after decode, so upstream
	// resources (file handles, HTTP connections) are freed early.
	defer C.clear_source(&source)

	loadParams := createImportParams(ImageTypeUnknown, params)

	if code := C.load_from_source(source, &loadParams); code != 0 {
		return nil, wrapStreamError("streaming load", handleImageError(loadParams.outputImage), entry.takeErr())
	}

	format := ImageType(loadParams.inputFormat)
	ref := newImageRef(loadParams.outputImage, format, format, nil)

	govipsLog("govips", LogLevelDebug, fmt.Sprintf("created imageRef %p from reader", ref))
	return ref, nil
}

// SaveToWriter encodes the image in the specified format and writes the
// encoded bytes directly to w using libvips streaming (VipsTargetCustom).
// The writer receives chunks of encoded data as they are produced and is
// not retained after SaveToWriter returns.
//
// Supported formats: ImageTypeJPEG, ImageTypePNG, ImageTypeWEBP,
// ImageTypeHEIF, ImageTypeTIFF, ImageTypeGIF. Output is byte-identical to
// the corresponding Export* method with equivalent parameters.
//
// TIFF is encoded in memory and written in a single chunk, because the
// TIFF container requires seekable output; all other formats stream
// encoded chunks to w as they are produced.
//
// params may be nil for the format's default export settings; the format
// argument takes precedence over params.Format.
func (r *ImageRef) SaveToWriter(w io.Writer, format ImageType, params *ExportParams) error {
	if w == nil {
		return errors.New("writer is nil")
	}

	r.lock.Lock()
	defer r.lock.Unlock()
	defer runtime.KeepAlive(r)

	if r.image == nil {
		return errors.New("attempt to save a closed ImageRef")
	}

	saveParams, cleanup, err := streamSaveParams(r.image, format, params)
	if err != nil {
		return err
	}
	defer cleanup()

	incOpCounter("save_" + ImageTypes[format] + "_target")

	if format == ImageTypeTIFF {
		// libtiff requires a seekable, readable output stream (it
		// rewrites IFD offsets after encoding), which a plain io.Writer
		// cannot provide. Encode through the buffer path and emit a
		// single write; the bytes are identical to ExportTiff.
		buf, err := vipsSaveToBuffer(saveParams)
		if err != nil {
			return err
		}
		if _, err := w.Write(buf); err != nil {
			return fmt.Errorf("streaming save: writer error: %w", err)
		}
		return nil
	}

	handle, entry := registerTarget(w)
	defer deregisterTarget(handle)

	target := C.create_target_custom(C.int(handle))
	if target == nil {
		return handleVipsError()
	}
	defer C.clear_target(&target)

	var code C.int
	switch format {
	case ImageTypeJPEG:
		code = C.save_jpeg_to_target(&saveParams, target)
	case ImageTypePNG:
		code = C.save_png_to_target(&saveParams, target)
	case ImageTypeWEBP:
		code = C.save_webp_to_target(&saveParams, target)
	case ImageTypeHEIF:
		code = C.save_heif_to_target(&saveParams, target)
	// ImageTypeTIFF is handled by the buffer-path early return above.
	case ImageTypeGIF:
		code = C.save_gif_to_target(&saveParams, target)
	}

	if code != 0 {
		return wrapStreamError("streaming save", handleVipsError(), entry.takeErr())
	}
	if ioErr := entry.takeErr(); ioErr != nil {
		return fmt.Errorf("streaming save: writer error: %w", ioErr)
	}
	return nil
}

// streamSaveParams builds the C save parameters for SaveToWriter using
// the same ExportParams mapping as (*ImageRef).Export and the same
// C-struct population as the Export* buffer savers, so streaming output
// stays byte-identical to the buffer path. The returned cleanup must be
// called after the save completes.
func streamSaveParams(in *C.VipsImage, format ImageType, params *ExportParams) (C.struct_SaveParams, func(), error) {
	noop := func() {}

	switch format {
	case ImageTypeJPEG:
		jp := NewJpegExportParams()
		if params != nil {
			jp = &JpegExportParams{
				Quality:            params.Quality,
				StripMetadata:      params.StripMetadata,
				Interlace:          params.Interlaced,
				OptimizeCoding:     params.OptimizeCoding,
				SubsampleMode:      params.SubsampleMode,
				TrellisQuant:       params.TrellisQuant,
				OvershootDeringing: params.OvershootDeringing,
				OptimizeScans:      params.OptimizeScans,
				QuantTable:         params.QuantTable,
			}
		}
		return newSaveParamsJPEG(in, *jp), noop, nil
	case ImageTypePNG:
		pp := NewPngExportParams()
		if params != nil {
			pp = &PngExportParams{
				StripMetadata: params.StripMetadata,
				Compression:   params.Compression,
				Interlace:     params.Interlaced,
			}
		}
		return newSaveParamsPNG(in, *pp), noop, nil
	case ImageTypeWEBP:
		wp := NewWebpExportParams()
		if params != nil {
			wp = &WebpExportParams{
				StripMetadata:   params.StripMetadata,
				Quality:         params.Quality,
				Lossless:        params.Lossless,
				ReductionEffort: params.Effort,
			}
		}
		p, cleanup := newSaveParamsWebP(in, *wp)
		return p, cleanup, nil
	case ImageTypeHEIF:
		hp := NewHeifExportParams()
		if params != nil {
			hp = &HeifExportParams{
				Quality:  params.Quality,
				Lossless: params.Lossless,
			}
		}
		return newSaveParamsHEIF(in, *hp), noop, nil
	case ImageTypeTIFF:
		tp := NewTiffExportParams()
		if params != nil {
			compression := TiffCompressionLzw
			if params.Lossless {
				compression = TiffCompressionNone
			}
			tp = &TiffExportParams{
				StripMetadata: params.StripMetadata,
				Quality:       params.Quality,
				Compression:   compression,
			}
		}
		return newSaveParamsTIFF(in, *tp), noop, nil
	case ImageTypeGIF:
		gp := NewGifExportParams()
		if params != nil {
			gp = &GifExportParams{
				Quality: params.Quality,
			}
		}
		return newSaveParamsGIF(in, *gp), noop, nil
	default:
		return C.struct_SaveParams{}, noop, fmt.Errorf("streaming save does not support format %q", ImageTypes[format])
	}
}

// wrapStreamError combines the libvips error with the original Go
// reader/writer error, when one was stored during a callback.
func wrapStreamError(op string, vipsErr, ioErr error) error {
	if ioErr != nil {
		return fmt.Errorf("%s: %w (caused by: %w)", op, vipsErr, ioErr)
	}
	return vipsErr
}
