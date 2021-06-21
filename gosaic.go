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
	"os"
	"path/filepath"
	"sync"

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
}

type Tile struct {
	Filename string
	Tiny     image.Image
}

type HasAt interface {
	At(x, y int) color.Color
	ColorModel() color.Model
	Bounds() image.Rectangle
}

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

	count := 0
	for i := 0; i < 8; i++ {
		go func(id int) {
			wg.Add(1)
			for path := range imgPathChan {
				count++
				// log.Printf("[%d] %s\n", id, path)
				img, err := vips.NewImageFromFile(path)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				err = img.ToColorSpace(vips.InterpretationSRGB)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				err = img.SmartCrop(g.config.CompareSize, g.config.CompareSize, vips.InterestingCentre)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				tinyImg, err := img.ToImage(vips.NewDefaultPNGExportParams())
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				tile := Tile{
					Filename: path,
					Tiny:     tinyImg,
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

func (g *Gosaic) Build() {
	rows := g.SeedImage.Bounds().Size().X / g.config.TileSize
	cols := g.SeedImage.Bounds().Size().Y / g.config.TileSize

	bar := pb.StartNew(rows * cols)

	//cur := g.tiles.Front()

	for x := 0; x < rows; x++ {
		for y := 0; y < cols; y++ {
			rect := image.Rect(x*g.config.TileSize, y*g.config.TileSize, (x+1)*g.config.TileSize, (y+1)*g.config.TileSize)
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
			err = imgRef.Thumbnail(g.config.CompareSize, g.config.CompareSize, vips.InterestingCentre)
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

			count := 0
			tileRect := image.Rect(0, 0, g.config.CompareSize, g.config.CompareSize)

			for cur := g.Tiles.Front(); cur != nil; cur = cur.Next() {
				count++

				tile := cur.Value.(Tile)
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
					minDist = dist
					minTile = tile
					minTileElem = cur
				}
			}

			if minTile.Filename == "" {
				log.Println("minTile is empty!")
			} else {
				g.Tiles.Remove(minTileElem)
				imgRef, err := vips.NewImageFromFile(minTile.Filename)
				if err != nil {
					log.Println(err)
					continue
				}
				err = imgRef.ToColorSpace(vips.InterpretationSRGB)
				if err != nil {
					log.Println(err)
					continue
				}
				err = imgRef.SmartCrop(g.config.TileSize, g.config.TileSize, vips.InterestingCentre)
				if err != nil {
					log.Println(err)
					continue
				}
				tileImg, err := imgRef.ToImage(vips.NewDefaultPNGExportParams())
				draw.Draw(g.SeedImage, rect, tileImg, image.ZP, draw.Over)
				g.SaveAsJPEG(g.SeedImage, g.config.OutputImage)
			}

			bar.Increment()
		}
	}

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
