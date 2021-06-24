package gosaic

import (
	"bytes"
	"container/list"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/davidbyttow/govips/v2/vips"
)

type Config struct {
	SeedImage   string
	OutputImage string
	OutputSize  int
	TileSize    int
	TilesGlob   string
	CompareSize int
	CompareDist float64
	Unique      bool
	SmartCrop   bool
	ProgressBar bool
}

type Tile struct {
	Filename string
	Tiny     image.Image
	Average  float64
}

type HasAt interface {
	At(x, y int) color.Color
	ColorModel() color.Model
	Bounds() image.Rectangle
}

type ProgressIndicator interface {
	Increment() *pb.ProgressBar
	Finish() *pb.ProgressBar
}

type ProgressCounter struct {
	count uint64
	max   uint64
}

func (c *ProgressCounter) Increment() *pb.ProgressBar {
	atomic.AddUint64(&c.count, 1)
	cur := atomic.LoadUint64(&c.count)
	max := atomic.LoadUint64(&c.max)
	fmt.Printf("%d/%d (%.2f%%)\n", cur, max, 100.0*float64(cur)/float64(max))
	return nil
}

func (c *ProgressCounter) Finish() *pb.ProgressBar { return nil }

type Gosaic struct {
	seedVIPSImage *vips.ImageRef
	SeedImage     *image.RGBA
	Tiles         *list.List
	config        Config
	scaleFactor   float64
}

func (g *Gosaic) diff(a, b uint32) int32 {
	if a > b {
		return int32(a - b)
	}
	return int32(b - a)
}

func (g *Gosaic) loadTiles() error {
	tileChan := make(chan Tile)
	imgPathChan := make(chan string)
	wg := sync.WaitGroup{}
	wg2 := sync.WaitGroup{}

	tilePaths, err := filepath.Glob(g.config.TilesGlob)
	if err != nil {
		return err
	}

	go func() {
		wg2.Add(1)
		for tile := range tileChan {
			g.Tiles.PushBack(tile)
		}
		wg2.Done()
	}()

	log.Println("Loading Tiles")
	var bar ProgressIndicator

	if g.config.ProgressBar {
		bar = pb.StartNew(len(tilePaths))
	} else {
		bar = &ProgressCounter{count: 0, max: uint64(len(tilePaths))}
	}

	count := 0
	for i := 0; i < 16; i++ {
		go func(id int) {
			wg.Add(1)
			for path := range imgPathChan {
				count++
				bar.Increment()
				// log.Printf("[%d] %s\n", id, path)

				tinyImg, avg, err := g.loadTile(path, g.config.CompareSize)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				tile := Tile{
					Filename: path,
					Tiny:     tinyImg,
					Average:  avg,
				}

				tileChan <- tile
			}
			wg.Done()
		}(i)
	}

	for _, path := range tilePaths {
		imgPathChan <- path
	}
	close(imgPathChan)
	wg.Wait()

	close(tileChan)
	wg2.Wait()

	bar.Finish()

	return nil
}

func (g *Gosaic) Difference(img1, img2 HasAt) (float64, error) {
	if img1.ColorModel() != img2.ColorModel() {
		return 0.0, errors.New("different color models")
	}

	b := img1.Bounds()
	c := img2.Bounds()
	if b.Dx() != c.Dx() || b.Dy() != c.Dy() {
		return 0.0, fmt.Errorf("bounds are not identical: %v vs. %v", b, c)
	}

	var sum int64
	for x := 0; x < b.Dx(); x++ {
		for y := 0; y < b.Dy(); y++ {
			x1 := x + b.Min.X
			y1 := y + b.Min.Y
			x2 := x + c.Min.X
			y2 := y + c.Min.Y
			r1, g1, b1, _ := img1.At(x1, y1).RGBA()
			r2, g2, b2, _ := img2.At(x2, y2).RGBA()

			sum += int64(g.diff(r1, r2))
			sum += int64(g.diff(g1, g2))
			sum += int64(g.diff(b1, b2))
		}
	}

	nPixels := b.Dx() * b.Dy()

	dist := float64(sum) / (float64(nPixels) * 0xffff * 3)
	return dist, nil
}

func (g *Gosaic) SaveAsJPEG(img image.Image, filename string) error {
	fh, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("%s: %s\n", filename, err)
	}
	defer fh.Close()

	err = jpeg.Encode(fh, img, &jpeg.Options{Quality: 85})
	if err != nil {
		return err
	}

	return nil
}

func (g *Gosaic) loadTile(filename string, size int) (image.Image, float64, error) {
	imgRef, err := vips.NewImageFromFile(filename)
	if err != nil {
		return nil, 0, err
	}

	// remove a white frame around the picture
	left, top, width, height, err := imgRef.FindTrim(40, &vips.Color{R: 255, G: 255, B: 255})
	if err != nil {
		return nil, 0, err
	}

	if width < imgRef.Width() || height < imgRef.Height() { // &&
		// (float64(width)/float64(img.Width()) > 0.8 && float64(height)/float64(img.Height()) > 0.8) {
		err = imgRef.ExtractArea(left, top, width, height)
		if err != nil {
			return nil, 0, err
		}
	}

	err = imgRef.ToColorSpace(vips.InterpretationSRGB)
	if err != nil {
		return nil, 0, err
	}

	avg, err := imgRef.Average()
	if err != nil {
		return nil, 0, err
	}

	//fmt.Println(avg)

	if g.config.SmartCrop {
		err = imgRef.SmartCrop(size, size, vips.InterestingAttention)
	} else {
		err = imgRef.Thumbnail(size, size, vips.InterestingAttention)
	}
	if err != nil {
		return nil, 0, err
	}

	img, err := imgRef.ToImage(vips.NewDefaultPNGExportParams())
	return img, avg, err
}

func (g *Gosaic) Build() {
	rows := g.SeedImage.Bounds().Size().X/g.config.TileSize + 1
	cols := g.SeedImage.Bounds().Size().Y/g.config.TileSize + 1

	log.Println("Building Mosaic")
	var bar ProgressIndicator
	if g.config.ProgressBar {
		bar = pb.StartNew(rows * cols)
	} else {
		bar = &ProgressCounter{count: 0, max: uint64(rows * cols)}
	}

	rects := make([]image.Rectangle, 0)
	for x := 0; x < rows; x++ {
		for y := 0; y < cols; y++ {
			rects = append(rects, image.Rect(x*g.config.TileSize, y*g.config.TileSize, (x+1)*g.config.TileSize, (y+1)*g.config.TileSize))
		}
	}

	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(rects), func(i, j int) { rects[i], rects[j] = rects[j], rects[i] })

	comparisons := 0

	var tRect, tCompare, tLoad time.Duration

	for _, rect := range rects {
		tRectStart := time.Now()
		// rect := image.Rect(x*g.config.TileSize, y*g.config.TileSize, (x+1)*g.config.TileSize, (y+1)*g.config.TileSize)
		subImg := g.SeedImage.SubImage(rect)

		buf := bytes.NewBuffer([]byte{})
		err := png.Encode(buf, subImg)
		if err != nil {
			log.Println(err)
			continue
		}
		imgRef, err := vips.NewImageFromReader(buf)
		if err != nil {
			log.Println(err)
			continue
		}

		err = imgRef.Thumbnail(g.config.CompareSize, g.config.CompareSize, vips.InterestingAttention)
		if err != nil {
			log.Println(err)
			continue
		}

		avg, err := imgRef.Average()
		if err != nil {
			log.Println(err)
			continue
		}

		compareImg, err := imgRef.ToImage(vips.NewDefaultPNGExportParams())
		if err != nil {
			log.Println(err)
			continue
		}

		minDist := 1.0
		var minTile Tile
		var minTileElem *list.Element

		tileRect := image.Rect(0, 0, g.config.CompareSize, g.config.CompareSize)

		tileChan := make(chan *list.Element)
		tileWG := sync.WaitGroup{}
		tileMutex := sync.Mutex{}
		tRect += time.Now().Sub(tRectStart)

		for i := 0; i < 100; i++ {
			tileWG.Add(1)
			go func(id int) {
				for elem := range tileChan {
					tStart := time.Now()
					tile := elem.Value.(Tile)

					if math.Abs(tile.Average-avg) > g.config.CompareDist {
						continue
					}

					// log.Printf("[%d] %d - %s\n", id, comparisons, tile.Filename)

					tileImg := tile.Tiny
					dist, err := g.Difference(
						compareImg.(*image.RGBA).SubImage(tileRect),
						tileImg.(*image.RGBA),
					)
					if err != nil {
						log.Println(err)
						continue
					}

					if dist < minDist {
						tileMutex.Lock()
						minTile = tile
						minDist = dist
						minTileElem = elem
						comparisons++
						tCompare += time.Now().Sub(tStart)
						tileMutex.Unlock()
					}
				}
				tileWG.Done()
			}(i)
		}

		for cur := g.Tiles.Front(); cur != nil; cur = cur.Next() {
			tileChan <- cur
		}
		close(tileChan)

		tileWG.Wait()

		tStart := time.Now()
		if minTile.Filename == "" {
			log.Println("minTile is empty!")
		} else {
			if g.config.Unique {
				g.Tiles.Remove(minTileElem)
			}

			tileImg, _, err := g.loadTile(minTile.Filename, g.config.TileSize)
			if err != nil {
				log.Println(err)
				continue
			}
			draw.Draw(g.SeedImage, rect, tileImg, image.ZP, draw.Over)
			//g.SaveAsJPEG(g.SeedImage, g.config.OutputImage)
		}
		tLoad += time.Now().Sub(tStart)

		bar.Increment()
	}

	bar.Finish()

	log.Printf("Comparisons: %d\n", comparisons)
	log.Printf("Rect time: %s\n", tRect)
	log.Printf("Compare time: %s\n", tCompare)
	log.Printf("Load time: %s\n", tLoad)
	err := g.SaveAsJPEG(g.SeedImage, g.config.OutputImage)
	if err != nil {
		log.Fatal(err)
	}

}

func New(config Config) (*Gosaic, error) {
	vips.LoggingSettings(func(messageDomain string, messageLevel vips.LogLevel, message string) {
		log.Println(message)
	}, vips.LogLevelError)

	// Load the master image and scale it to the output size
	img, err := vips.NewImageFromFile(config.SeedImage)
	if err != nil {
		return nil, err
	}

	scaleFactorX := float64(config.OutputSize) / float64(img.Width())
	scaleFactorY := float64(config.OutputSize) / float64(img.Height())

	scaleFactor := scaleFactorX
	if scaleFactor < scaleFactorY {
		scaleFactor = scaleFactorY
	}

	fmt.Printf("master scale factor: %f\n", scaleFactor)
	//os.Exit(1)

	img.Resize(scaleFactor, vips.KernelAuto)

	// Create the mosaic
	g := Gosaic{
		config:        config,
		seedVIPSImage: img,
		Tiles:         list.New(),
		scaleFactor:   scaleFactor,
	}

	seed, err := img.ToImage(vips.NewDefaultPNGExportParams())
	if err != nil {
		return nil, err
	}

	g.SeedImage = seed.(*image.RGBA)

	g.loadTiles()

	/*
		exportOpts := vips.NewJpegExportParams()
		exportOpts.Quality = 80
		bytes, _, err := img.ExportJpeg(exportOpts)

		fh, err := os.Create(config.OutputImage)
		if err != nil {
			return nil, err
		}
		fh.Write(bytes)
		fh.Close()
	*/

	return &g, nil
}
