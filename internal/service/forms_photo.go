package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	_ "image/jpeg" // DecodeConfig for photos
	_ "image/png"
	pathpkg "path"
	"strconv"
	"strings"
)

// Photo fields (Core 0.95.19): {{photo name=site_photo max=3}} asks for up to
// max photos (1 to 10, default 1); required asks for at least one.
//
// The answer is a list. Each item is a photo as a data URL
// ("data:image/jpeg;base64,…", what Slate sends after scaling it down), or
// the path of a picture already in the form's space (what an agent usually
// sends). The server keeps JPEG and PNG only, takes out everything that can
// say where or on what a photo was taken (Exif and XMP, including GPS, and
// PNG text chunks; a JPEG keeps only its orientation), stores each one as a
// virtual file in an assets/ folder beside the filed copy, in the same
// commit, and the answer becomes the list of those files' paths, relative
// to the copy, which shows them as pictures.

const (
	maxPhotosPerField = 10
	maxFormPhotos     = 20
	maxPhotoBytes     = 10 << 20
	maxFormPhotoBytes = 40 << 20
	maxPhotoSide      = 20000
	maxPhotoPixels    = 100_000_000
	// maxSubmitBodyBytes is a submission's request body: the answers and
	// their photos, as base64.
	maxSubmitBodyBytes = maxFormAnswerBytes + 4096 + maxFormPhotoBytes/3*4 + 64<<10
)

// photoUpload is one photo in an answer: its cleaned bytes, or, until the
// submission reads it, the space path it is to come from.
type photoUpload struct {
	from  string
	media string
	data  []byte
}

// formPhoto is a photo stored for a filed copy: its path in the target space
// and the virtual file's content.
type formPhoto struct {
	path string
	info VirtualInfo
}

// readPhotoAnswer reads a photo field's answer: a list of data URLs and
// space paths. It returns what is wrong, or "".
func readPhotoAnswer(f formField, value json.RawMessage, present bool) ([]photoUpload, string) {
	var items []string
	if present && string(value) != "null" && json.Unmarshal(value, &items) != nil {
		return nil, "Send the photos as a list of data URLs (data:image/jpeg;base64,…) or paths of pictures in this space."
	}
	limit := photoLimit(f)
	switch {
	case len(items) == 0 && f.Required:
		return nil, "Add a photo."
	case len(items) > limit:
		if limit == 1 {
			return nil, "Add one photo."
		}
		return nil, fmt.Sprintf("Add at most %d photos.", limit)
	}
	photos := []photoUpload{}
	for i, item := range items {
		n := ""
		if len(items) > 1 {
			n = " " + strconv.Itoa(i+1)
		}
		if !strings.HasPrefix(item, "data:") {
			if !validPath(item) || !isPicturePath(item) {
				return nil, "Photo" + n + " is neither a data URL nor the path of a picture (.jpg or .png) in this space."
			}
			photos = append(photos, photoUpload{from: item})
			continue
		}
		data, message := decodePhotoURL(item)
		if message != "" {
			return nil, "Photo" + n + ": " + message
		}
		media, clean, message := cleanPhoto(data)
		if message != "" {
			return nil, "Photo" + n + ": " + message
		}
		photos = append(photos, photoUpload{media: media, data: clean})
	}
	return photos, ""
}

func photoLimit(f formField) int {
	if v := intAttr(f.attrs["max"]); v > 0 {
		return v
	}
	return 1
}

func isPicturePath(p string) bool {
	switch strings.ToLower(pathpkg.Ext(p)) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

// decodePhotoURL reads a base64 data URL's bytes.
func decodePhotoURL(s string) ([]byte, string) {
	head, payload, ok := strings.Cut(strings.TrimPrefix(s, "data:"), ",")
	if !ok || !strings.HasSuffix(strings.ToLower(head), ";base64") {
		return nil, "send it as a base64 data URL, such as data:image/jpeg;base64,…"
	}
	if base64.StdEncoding.DecodedLen(len(payload)) > maxPhotoBytes+3 {
		return nil, fmt.Sprintf("a photo can be at most %d MB.", maxPhotoBytes>>20)
	}
	payload = strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	b, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
	}
	if err != nil {
		return nil, "its base64 can't be read."
	}
	return b, ""
}

// cleanPhoto checks that b is a JPEG or PNG picture of a sensible size and
// returns it without its metadata.
func cleanPhoto(b []byte) (string, []byte, string) {
	if len(b) > maxPhotoBytes {
		return "", nil, fmt.Sprintf("a photo can be at most %d MB.", maxPhotoBytes>>20)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "jpeg" && format != "png" {
		return "", nil, "send a JPEG or PNG picture."
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxPhotoSide || cfg.Height > maxPhotoSide || cfg.Width*cfg.Height > maxPhotoPixels {
		return "", nil, "the picture is too large; send it at most 20000 pixels a side."
	}
	var clean []byte
	media := "image/jpeg"
	if format == "png" {
		media = "image/png"
		clean, err = stripPNG(b)
	} else {
		clean, err = stripJPEG(b)
	}
	if err != nil {
		return "", nil, "the picture can't be read."
	}
	return media, clean, ""
}

// stripJPEG drops a JPEG's application segments that carry metadata (Exif,
// XMP, IPTC and the like) and comments, keeping JFIF, ICC colour profiles
// and Adobe's colour transform, and an Exif segment of only the original
// orientation, so a photo still shows the right way up.
func stripJPEG(b []byte) ([]byte, error) {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return nil, fmt.Errorf("not a JPEG")
	}
	out := bytes.NewBuffer(make([]byte, 0, len(b)))
	out.Write(b[:2])
	orientation := 0
	i := 2
	for {
		for i < len(b) && b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0xFF {
			i++ // fill bytes
		}
		if i+4 > len(b) || b[i] != 0xFF {
			return nil, fmt.Errorf("broken JPEG")
		}
		marker := b[i+1]
		if marker == 0xD9 {
			out.Write(b[i : i+2])
			return out.Bytes(), nil
		}
		length := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		end := i + 2 + length
		if length < 2 || end > len(b) {
			return nil, fmt.Errorf("broken JPEG")
		}
		segment := b[i:end]
		payload := b[i+4 : end]
		keep := true
		switch {
		case marker == 0xE1:
			if o := exifOrientation(payload); o > 1 && o <= 8 {
				orientation = o
			}
			keep = false
		case marker == 0xE2:
			keep = bytes.HasPrefix(payload, []byte("ICC_PROFILE\x00"))
		case marker == 0xFE, marker >= 0xE3 && marker <= 0xEF && marker != 0xEE:
			keep = false
		}
		if marker == 0xDA {
			if orientation > 1 {
				out.Write(orientationSegment(orientation))
			}
			// The scan and everything after it are picture data.
			out.Write(b[i:])
			return out.Bytes(), nil
		}
		if keep {
			out.Write(segment)
		}
		i = end
	}
}

// exifOrientation reads the Orientation tag from an APP1 Exif payload, or 0.
func exifOrientation(p []byte) int {
	if len(p) < 14 || string(p[:6]) != "Exif\x00\x00" {
		return 0
	}
	t := p[6:]
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 0
	}
	ifd := int(order.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 0
	}
	n := int(order.Uint16(t[ifd : ifd+2]))
	for k := 0; k < n; k++ {
		e := ifd + 2 + 12*k
		if e+12 > len(t) {
			return 0
		}
		if order.Uint16(t[e:e+2]) == 0x0112 {
			return int(order.Uint16(t[e+8 : e+10]))
		}
	}
	return 0
}

// orientationSegment is an APP1 Exif segment holding only an orientation.
func orientationSegment(o int) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, // header, IFD at 8
		0, 1, // one entry
		0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, byte(o), 0, 0, // Orientation, SHORT, 1
		0, 0, 0, 0} // no next IFD
	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(seg, payload...)
}

// pngKeep are the PNG chunks a cleaned picture keeps: the picture and how
// its colours show; text, time and Exif chunks are dropped.
var pngKeep = map[string]bool{"IHDR": true, "PLTE": true, "IDAT": true, "IEND": true, "tRNS": true, "gAMA": true, "cHRM": true, "sRGB": true, "iCCP": true, "sBIT": true, "pHYs": true, "bKGD": true}

func stripPNG(b []byte) ([]byte, error) {
	const sig = "\x89PNG\r\n\x1a\n"
	if len(b) < len(sig) || string(b[:len(sig)]) != sig {
		return nil, fmt.Errorf("not a PNG")
	}
	out := bytes.NewBuffer(make([]byte, 0, len(b)))
	out.WriteString(sig)
	i := len(sig)
	for i+12 <= len(b) {
		length := int(binary.BigEndian.Uint32(b[i : i+4]))
		end := i + 12 + length
		if length < 0 || end > len(b) {
			return nil, fmt.Errorf("broken PNG")
		}
		kind := string(b[i+4 : i+8])
		if crc32.ChecksumIEEE(b[i+4:i+8+length]) != binary.BigEndian.Uint32(b[i+8+length:end]) {
			return nil, fmt.Errorf("broken PNG")
		}
		if pngKeep[kind] {
			out.Write(b[i:end])
		}
		i = end
		if kind == "IEND" {
			return out.Bytes(), nil
		}
	}
	return nil, fmt.Errorf("broken PNG")
}

// photoAnswers are the answers' photo lists, by field.
func photoAnswers(t formTemplate, answers formAnswers) map[string][]photoUpload {
	out := map[string][]photoUpload{}
	for _, f := range t.Fields {
		if list, ok := answers[f.Name].([]photoUpload); ok {
			out[f.Name] = list
		}
	}
	return out
}

// readSpacePhotos fills in photos given as paths of pictures in the form's
// space, and checks the size of them all. Problems are by field.
func (s *Service) readSpacePhotos(ctx context.Context, repo *repository, files map[string]record, t formTemplate, answers formAnswers) []FieldError {
	var problems []FieldError
	total := 0
	for _, f := range t.Fields {
		list, ok := answers[f.Name].([]photoUpload)
		if !ok {
			continue
		}
		for i := range list {
			p := &list[i]
			if p.from != "" {
				rec, ok := files[p.from]
				if !ok || rec.Virtual == nil || !rec.Virtual.image() {
					problems = append(problems, FieldError{f.Name, p.from + " is not a picture in this space."})
					break
				}
				if rec.Virtual.Size > maxPhotoBytes {
					problems = append(problems, FieldError{f.Name, fmt.Sprintf("%s is over %d MB.", p.from, maxPhotoBytes>>20)})
					break
				}
				b, err := s.virtualContent(ctx, repo, *rec.Virtual)
				if err != nil {
					problems = append(problems, FieldError{f.Name, p.from + " could not be read; try again later."})
					break
				}
				media, clean, message := cleanPhoto(b)
				if message != "" {
					problems = append(problems, FieldError{f.Name, p.from + ": " + message})
					break
				}
				p.media, p.data = media, clean
			}
			total += len(p.data)
		}
	}
	if len(problems) == 0 && total > maxFormPhotoBytes {
		for _, f := range t.Fields {
			if _, ok := answers[f.Name].([]photoUpload); ok {
				problems = append(problems, FieldError{f.Name, fmt.Sprintf("The photos come to more than %d MB; send them smaller.", maxFormPhotoBytes>>20)})
				break
			}
		}
	}
	return problems
}

// storeFormPhotos stores a submission's photos as virtual files of the
// target space, under folder/assets/, named by the submission and field,
// and puts their paths, relative to the copy, in the answers.
func (s *Service) storeFormPhotos(ctx context.Context, target submitTarget, t formTemplate, answers formAnswers, id string) ([]formPhoto, error) {
	var out []formPhoto
	for _, f := range t.Fields {
		list, ok := answers[f.Name].([]photoUpload)
		if !ok {
			continue
		}
		paths := []string{}
		for i, p := range list {
			ext := ".jpg"
			if p.media == "image/png" {
				ext = ".png"
			}
			rel := "assets/" + id[:12] + "-" + strings.ToLower(f.Name) + "-" + strconv.Itoa(i+1) + ext
			info, err := s.storeVirtual(ctx, target.repo, target.folder+rel, p.data, p.media, "")
			if err != nil {
				return nil, err
			}
			out = append(out, formPhoto{path: target.folder + rel, info: info})
			paths = append(paths, rel)
		}
		answers[f.Name] = paths
	}
	return out, nil
}

// photoPaths is a photo answer's list of paths.
func photoPaths(v any) []string {
	switch a := v.(type) {
	case []string:
		return a
	case []any:
		var out []string
		for _, item := range a {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// photoMarkdown shows a photo answer's pictures on a line of their own.
func photoMarkdown(f formField, paths []string) string {
	label := f.Label
	if label == "" {
		label = f.Name
	}
	var b strings.Builder
	for i, p := range paths {
		if i > 0 {
			b.WriteString(" ")
		}
		alt := label
		if len(paths) > 1 {
			alt += " " + strconv.Itoa(i+1)
		}
		b.WriteString("![" + escapeInline(alt) + "](" + p + ")")
	}
	return b.String() + "\n"
}
