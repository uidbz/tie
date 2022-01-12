package main

import (
	"fmt"
	"image"
	"image/draw"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2/canvas"
	"github.com/disintegration/gift"
	"github.com/disintegration/imaging"
	"github.com/nfnt/resize"
	"github.com/rwcarlsen/goexif/exif"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

type ImgView struct {
	widget.BaseWidget
	focus        func(fyne.Focusable)
	content      *canvas.Image
	origImage    *canvas.Image
	zoom         int
	dirPath      string
	dirContent   []fs.DirEntry
	currentIndex int
}

func NewImgView(path string, focusFunc func(fyne.Focusable)) *ImgView {
	img := ReadImage(path)
	orig := canvas.NewImageFromImage(CloneToRGBA(img.Image))
	img.ScaleMode = canvas.ImageScaleFastest
	orig.ScaleMode = canvas.ImageScaleFastest

	dirPath, dir, index := ReadDir(path)
	iv := &ImgView{
		content:      img,
		origImage:    orig,
		focus:        focusFunc,
		dirPath:      dirPath,
		dirContent:   dir,
		currentIndex: index,
	}
	iv.ExtendBaseWidget(iv)

	return iv
}

func (iv *ImgView) SetCurrentImage(path string) {
	fmt.Println("Current img", path)
	func() {
		GetFileInfo(path)
		tieView.Refresh()
	}()
	iv.content = ReadImage(path)

	refreshImg()
}

func ReadDir(imgPath string) (string, []fs.DirEntry, int) {
	path := filepath.Dir(imgPath)
	dirContent, _ := os.ReadDir(path)
	for i, x := range dirContent {
		if x.Name() == filepath.Base(imgPath) {
			return path, dirContent, i
		}
	}

	return path, dirContent, 0
}

type ImgViewRenderer struct {
	iv *ImgView
}

func (iv *ImgView) CreateRenderer() fyne.WidgetRenderer {
	r := &ImgViewRenderer{iv}

	return r
}

func (r *ImgViewRenderer) Destroy() {}

func (r *ImgViewRenderer) Layout(size fyne.Size) {
	r.iv.content.Resize(size)
}

// MinSize returns the minimum size of the widget that is rendered by this renderer.
func (r *ImgViewRenderer) MinSize() fyne.Size {
	return fyne.NewSize(100, 100)
}

// Objects returns all objects that should be drawn.
func (r *ImgViewRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.iv.content}
}

func (r *ImgViewRenderer) Refresh() {
	fmt.Println("refresh")
}
func (e *ImgView) Tapped(ev *fyne.PointEvent) {
	fmt.Println("mouseIn", ev)
	e.focus(e)
}

func (e *ImgView) FocusGained() {
	fmt.Println("Focus gained")
}
func (e *ImgView) FocusLost() {
	fmt.Println("Focus lost")
}
func (e *ImgView) TypedKey(key *fyne.KeyEvent) {
	fmt.Println("KeyEvent", key)
	switch key.Name {
	case "Space":
		e.NextImage()
	case "Backspace":
		e.PrevImage()
	case "Right":
		e.NextImage()
	case "Left":
		e.PrevImage()

	}
}

func (e *ImgView) Scrolled(ev *fyne.ScrollEvent) {
	fmt.Println("Scroll")

	src := e.origImage.Image

	zoom := int(math.Max(float64(src.Bounds().Dx()), float64(src.Bounds().Dy())) * 0.02)

	fmt.Println("Zoom", zoom)
	// Zoom out
	if ev.Scrolled.DY < 0 && e.zoom > 0 {
		e.zoom -= zoom
	}

	// fmt.Println("x",
	// fmt.Println("y", src.Bounds().Dy())

	my_width := src.Bounds().Dx() - e.zoom
	my_height := src.Bounds().Dy() - e.zoom

	// Zoom in
	if ev.Scrolled.DY > 0 && e.zoom < my_width {
		e.zoom += zoom
	}

	g := gift.New(
		gift.Crop(image.Rect(e.zoom, e.zoom, my_width, my_height)),
	)
	dst := image.NewRGBA(g.Bounds(src.Bounds()))
	g.Draw(dst, src)

	// show new image
	e.content.Image = dst
	e.content.Refresh()
	// e.Refresh()
}
func IsImage(entry fs.DirEntry) bool {
	if entry.IsDir() {
		return false
	}

	switch filepath.Ext(entry.Name()) {
	case ".jpg":
		return true
	case ".png":
		return true
	case ".gif":
		return true
	default:
		return false
	}
}
func (e *ImgView) NextImage() {
	if e.currentIndex < len(e.dirContent)-1 {
		e.currentIndex++
		if !IsImage(e.dirContent[e.currentIndex]) {
			e.NextImage()
			return
		}
		path := filepath.Join(e.dirPath, e.dirContent[e.currentIndex].Name())
		e.SetCurrentImage(path)
	}
}

func (e *ImgView) PrevImage() {
	if e.currentIndex > 0 {
		e.currentIndex--
		if !IsImage(e.dirContent[e.currentIndex]) {
			e.PrevImage()
			return
		}
		path := filepath.Join(e.dirPath, e.dirContent[e.currentIndex].Name())
		e.SetCurrentImage(path)
	}
}

func (e *ImgView) TypedRune(r rune) {
	fmt.Println("rune", r)
}

func Decode(reader io.ReadSeeker) (image.Image, string, error) {
	img, fmt, err := image.Decode(reader)
	if err != nil {
		return img, fmt, err
	}
	reader.Seek(0, io.SeekStart)
	orientation := getOrientation(reader)
	switch orientation {
	case "1":
	case "2":
		img = imaging.FlipH(img)
	case "3":
		img = imaging.Rotate180(img)
	case "4":
		img = imaging.Rotate180(imaging.FlipH(img))
	case "5":
		img = imaging.Rotate270(imaging.FlipV(img))
	case "6":
		img = imaging.Rotate270(img)
	case "7":
		img = imaging.Rotate90(imaging.FlipV(img))
	case "8":
		img = imaging.Rotate90(img)
	}

	return img, fmt, err
}

func getOrientation(reader io.Reader) string {
	x, err := exif.Decode(reader)
	if err != nil {
		return "1"
	}
	if x != nil {
		orient, err := x.Get(exif.Orientation)
		if err != nil {
			return "1"
		}
		if orient != nil {
			return orient.String()
		}
	}

	return "1"
}

func ScaleImage(img image.Image, w uint, h uint) image.Image {
	return resize.Thumbnail(w, h, img,
		resize.Lanczos3)
}

func ReadImage(path string) *canvas.Image {
	img, err := os.Open(path)
	if err != nil {
		// name = err.Error()
		return nil
	}
	img2, _, err2 := Decode(img)
	if err2 != nil {
		fmt.Println("Error 3:", err2)
		return nil
	}
	img3 := canvas.NewImageFromImage(img2)
	img3.FillMode = canvas.ImageFillContain

	return img3
}

// func clonePix(b []uint8) []byte {
// 	c := make([]uint8, len(b))
// 	copy(c, b)
// 	return c
// }

func CloneToRGBA(src image.Image) draw.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)
	return dst
}

// func CloneImage(src image.Image) draw.Image {
// 	switch s := src.(type) {
// 	case *image.Alpha:
// 		clone := src.(type)
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.Alpha16:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.Gray:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.Gray16:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.NRGBA:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.NRGBA64:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.RGBA:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	case *image.RGBA64:
// 		clone := *s
// 		clone.Pix = clonePix(s.Pix)
// 		return &clone
// 	}
// 	return nil
// }
func MaxInt(x, y int) int {
	if x > y {
		return x
	}
	return y
}
