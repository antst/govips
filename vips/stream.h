// Streaming I/O via VipsSourceCustom/VipsTargetCustom.
// C declarations for the callback bridge between libvips worker threads
// and Go io.Reader/io.Writer instances registered in stream.go.

#ifndef STREAM_H
#define STREAM_H

#include "foreign.h"

// Exported Go callbacks, defined in stream.go via //export.
// Called by the static C trampolines in stream.c. The handle identifies
// an entry in the Go-side callback registry. The buffer/data pointers are
// owned by libvips and are only valid for the duration of the call; Go
// must not retain them.

// Reads up to length bytes into buffer. Returns bytes read, 0 on EOF,
// -1 on error.
extern gint64 goSourceReadCb(int handle, void *buffer, gint64 length);

// Seeks the underlying reader. Returns the new absolute offset, or -1 on
// error or if the reader does not support seeking.
extern gint64 goSourceSeekCb(int handle, gint64 offset, int whence);

// Writes length bytes from data. Returns bytes written, -1 on error.
extern gint64 goTargetWriteCb(int handle, void *data, gint64 length);

// Signals end of output. Returns 0 on success, -1 on error.
extern int goTargetEndCb(int handle);

// Creates a VipsSourceCustom with the "read" trampoline connected, and the
// "seek" trampoline too when has_seek is non-zero. The handle is stored as
// signal user_data. Returns a new reference owned by the caller; release
// with clear_source. Returns NULL on failure.
VipsSourceCustom *create_source_custom(int handle, int has_seek);

// Creates a VipsTargetCustom with the "write" and "end" trampolines
// connected. The handle is stored as signal user_data. Returns a new
// reference owned by the caller; release with clear_target. Returns NULL
// on failure.
VipsTargetCustom *create_target_custom(int handle);

// Detects the image format by sniffing the source, then loads it.
// Borrows source (does not take ownership). On success returns 0, sets
// params->inputFormat to the detected format, and stores a new VipsImage
// reference in params->outputImage owned by the caller. On failure
// returns non-zero with the error in the vips error buffer.
int load_from_source(VipsSourceCustom *source, LoadParams *params);

// Encode params->inputImage to the target. Both the image and the target
// are borrowed (the caller retains ownership). Return 0 on success,
// non-zero on failure with the error in the vips error buffer.
int save_jpeg_to_target(SaveParams *params, VipsTargetCustom *target);
int save_png_to_target(SaveParams *params, VipsTargetCustom *target);
int save_webp_to_target(SaveParams *params, VipsTargetCustom *target);
int save_heif_to_target(SaveParams *params, VipsTargetCustom *target);
int save_tiff_to_target(SaveParams *params, VipsTargetCustom *target);
int save_gif_to_target(SaveParams *params, VipsTargetCustom *target);

// Unrefs *source / *target and sets it to NULL. NULL-safe.
void clear_source(VipsSourceCustom **source);
void clear_target(VipsTargetCustom **target);

#endif  // STREAM_H
