package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/color"
	_ "image/gif" // pictures a preview is made from
	"image/jpeg"
	"image/png"
	"io"
	"log"
	pathpkg "path"
	"strings"
)

// Picture previews (0.97.1): uploading a JPEG, PNG or GIF picture that is
// large (longer side over previewSide pixels, or over previewMinBytes) also
// stores a smaller copy beside it, "<name>.preview.jpg" (".preview.png" when
// the picture has see-through parts), for documents to show instead of the
// full-size file:
//
//	![Site photo](assets/site.preview.jpg)
//
// The copy is turned the way the photo's Exif orientation says, so it shows
// the right way up anywhere, and keeps no metadata (no Exif, so no GPS). It
// is an ordinary virtual file, written in its own commit after the
// original's; uploading a new version of the picture makes it again. A
// preview that can't be made (a picture too large to decode, a broken one,
// a file already at that name that isn't a virtual file) leaves the upload
// as it is: the original is stored either way.

const (
	previewSide     = 1280
	previewMinBytes = 512 << 10
	previewQuality  = 82
	// previewMaxPixels bounds the memory a decode takes (4 bytes a pixel).
	previewMaxPixels = 50_000_000
	previewMarker    = ".preview."
)

// previewTypes are the pictures the standard library decodes.
var previewTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/gif": true}

// isPreviewPath reports a picture's preview, which gets no preview itself.
func isPreviewPath(p string) bool {
	return strings.Contains(strings.ToLower(pathpkg.Base(p)), previewMarker)
}

// previewPath is where path's preview goes, with the extension it is saved as.
func previewPath(path, ext string) string {
	base := strings.TrimSuffix(path, pathpkg.Ext(path))
	return base + previewMarker + ext
}

// makePreview reads a picture and returns its preview's bytes and type
// ("image/jpeg" or "image/png"), or ok false when it needs none or can't
// have one.
func makePreview(r io.ReadSeeker, size int64) (body []byte, media string, ok bool) {
	cfg, format, err := image.DecodeConfig(r)
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > previewMaxPixels {
		return nil, "", false
	}
	if max(cfg.Width, cfg.Height) <= previewSide && size <= previewMinBytes {
		return nil, "", false // small already: documents show the original
	}
	orientation := 0
	if format == "jpeg" {
		if _, err := r.Seek(0, io.SeekStart); err != nil {
			return nil, "", false
		}
		orientation = jpegOrientation(r)
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, "", false
	}
	src, _, err := image.Decode(r)
	if err != nil {
		return nil, "", false
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if long := max(w, h); long > previewSide {
		w, h = max(1, w*previewSide/long), max(1, h*previewSide/long)
	}
	small, opaque := shrink(src, w, h)
	out := orient(small, orientation)
	var buf bytes.Buffer
	if opaque {
		err, media = jpeg.Encode(&buf, out, &jpeg.Options{Quality: previewQuality}), "image/jpeg"
	} else {
		err, media = png.Encode(&buf, out), "image/png"
	}
	if err != nil {
		return nil, "", false
	}
	return buf.Bytes(), media, true
}

// jpegOrientation finds a JPEG's Exif orientation (1 to 8), or 0.
func jpegOrientation(r io.Reader) int {
	head := make([]byte, 128<<10) // Exif comes before the picture data
	n, _ := io.ReadFull(r, head)
	b := head[:n]
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 0
	}
	for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
		marker := b[i+1]
		if marker == 0xFF {
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			return 0
		}
		end := i + 2 + int(binary.BigEndian.Uint16(b[i+2:i+4]))
		if end > len(b) || end <= i+4 {
			return 0
		}
		if marker == 0xE1 {
			if o := exifOrientation(b[i+4 : end]); o >= 1 && o <= 8 {
				return o
			}
		}
		i = end
	}
	return 0
}

// shrink scales src down to w×h by averaging the pixels each one covers,
// and reports whether every pixel is opaque.
func shrink(src image.Image, w, h int) (*image.RGBA, bool) {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	// Each target column's first source column, and one past its last.
	cols := make([]int, w+1)
	for x := 0; x <= w; x++ {
		cols[x] = x * sw / w
	}
	sums := make([]uint64, 4*w)
	opaque := true
	pixel := pixelReader(src)
	for ty := 0; ty < h; ty++ {
		y0, y1 := ty*sh/h, (ty+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		clear(sums)
		for y := y0; y < y1; y++ {
			for tx := 0; tx < w; tx++ {
				x0, x1 := cols[tx], cols[tx+1]
				if x1 <= x0 {
					x1 = x0 + 1
				}
				s := sums[4*tx : 4*tx+4]
				for x := x0; x < x1; x++ {
					r, g, bl, a := pixel(b.Min.X+x, b.Min.Y+y)
					s[0] += uint64(r)
					s[1] += uint64(g)
					s[2] += uint64(bl)
					s[3] += uint64(a)
				}
			}
		}
		for tx := 0; tx < w; tx++ {
			x0, x1 := cols[tx], cols[tx+1]
			if x1 <= x0 {
				x1 = x0 + 1
			}
			n := uint64((x1 - x0) * (y1 - y0))
			s := sums[4*tx : 4*tx+4]
			c := color.RGBA{uint8((s[0] + n/2) / n), uint8((s[1] + n/2) / n), uint8((s[2] + n/2) / n), uint8((s[3] + n/2) / n)}
			if c.A != 0xFF {
				opaque = false
			}
			dst.SetRGBA(tx, ty, c)
		}
	}
	return dst, opaque
}

// pixelReader reads 8-bit premultiplied colour, quickly for the kinds of
// picture decoders give.
func pixelReader(src image.Image) func(x, y int) (r, g, b, a uint8) {
	switch m := src.(type) {
	case *image.YCbCr:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			yi, ci := m.YOffset(x, y), m.COffset(x, y)
			r, g, b := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			return r, g, b, 0xFF
		}
	case *image.Gray:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			v := m.Pix[m.PixOffset(x, y)]
			return v, v, v, 0xFF
		}
	case *image.RGBA:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			p := m.Pix[m.PixOffset(x, y):]
			return p[0], p[1], p[2], p[3]
		}
	case *image.NRGBA:
		return func(x, y int) (uint8, uint8, uint8, uint8) {
			p := m.Pix[m.PixOffset(x, y):]
			a := uint16(p[3])
			return uint8(uint16(p[0]) * a / 255), uint8(uint16(p[1]) * a / 255), uint8(uint16(p[2]) * a / 255), p[3]
		}
	}
	return func(x, y int) (uint8, uint8, uint8, uint8) {
		r, g, b, a := src.At(x, y).RGBA()
		return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)
	}
}

// orient turns and flips m as Exif orientation o says to show it.
func orient(m *image.RGBA, o int) *image.RGBA {
	if o < 2 || o > 8 {
		return m
	}
	w, h := m.Bounds().Dx(), m.Bounds().Dy()
	ow, oh := w, h
	if o >= 5 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var nx, ny int
			switch o {
			case 2: // mirrored
				nx, ny = w-1-x, y
			case 3: // upside down
				nx, ny = w-1-x, h-1-y
			case 4: // upside down, mirrored
				nx, ny = x, h-1-y
			case 5: // transposed
				nx, ny = y, x
			case 6: // turned a quarter clockwise to show
				nx, ny = h-1-y, x
			case 7: // transverse
				nx, ny = h-1-y, w-1-x
			case 8: // turned a quarter anticlockwise to show
				nx, ny = y, w-1-x
			}
			out.SetRGBA(nx, ny, m.RGBAAt(x, y))
		}
	}
	return out
}

// storePreview makes and stores the preview of a picture just uploaded to
// path (state is the commit the upload made). It logs what goes wrong and
// returns the preview's path, or "".
func (s *Service) storePreview(ctx context.Context, r *repository, space, path, state string, info VirtualInfo, u *spooledUpload) string {
	if !previewTypes[info.Type] || isPreviewPath(path) {
		return ""
	}
	if _, err := u.file.Seek(0, io.SeekStart); err != nil {
		return ""
	}
	body, media, ok := makePreview(u.file, u.size)
	if !ok {
		return ""
	}
	ext := "jpg"
	if media == "image/png" {
		ext = "png"
	}
	target := previewPath(path, ext)
	if !validPath(target) {
		return ""
	}
	// Never replace a file someone wrote there: only an earlier preview.
	if _, files, err := r.snapshot(ctx, state); err != nil {
		return ""
	} else if f, there := files[target]; there && f.Virtual == nil {
		return ""
	}
	if _, err := s.putVirtual(ctx, r, space, target, state, body, media, "", ""); err != nil {
		log.Printf("picture preview %s: %v", target, err)
		return ""
	}
	return target
}
