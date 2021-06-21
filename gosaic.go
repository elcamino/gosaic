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
	Tile *image.RGBA
	Tiny *image.RGBA
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
				log.Printf("[%d] %s\n", id, path)
				img, err := vips.NewImageFromFile(path)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				err = img.SmartCrop(g.config.TileSize, g.config.TileSize, vips.InterestingAttention)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				t1, err := img.ToImage(vips.NewDefaultPNGExportParams())
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}
				// g.SaveAsJPEG(tile, fmt.Sprintf("test/tile_%04d.jpg", count))

				err = img.Thumbnail(g.config.CompareSize, g.config.CompareSize, vips.InterestingCentre)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				t2, err := img.ToImage(vips.NewDefaultPNGExportParams())
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
				}

				tile := Tile{
					Tile: t1.(*image.RGBA),
					Tiny: t2.(*image.RGBA),
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

	count = 0
	for cur := g.Tiles.Front(); cur != nil; cur = cur.Next() {
		count++
		img := cur.Value.(Tile)
		g.SaveAsJPEG(img.Tile, fmt.Sprintf("test/tile_%d.jpg", count))
	}

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

			/*
				if x == 20 && y == 20 {
					fmt.Printf("%d/%d img1 %d/%d/%d - %d/%d img2 %d/%d/%d\n", x1, y1, r1/255, g1/255, b1/255, x2, y2, r2/255, g2/255, b2/255)
				}
			*/
			sum += int64(g.diff(r1, r2))
			sum += int64(g.diff(g1, g2))
			sum += int64(g.diff(b1, b2))
		}
	}

	nPixels := b.Dx() * b.Dy()

	dist := float64(sum) / (float64(nPixels) * 0xffff * 3)
	// fmt.Printf("dist: %.3f %v / %v - %d\n", dist, b, c, sum)
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

	//cur := g.tiles.Front()

	for x := 0; x < rows; x++ {
		for y := 0; y < cols; y++ {
			fmt.Printf("%d/%d\n", x, y)
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
			var minTile image.Image
			var minTileElem *list.Element

			//g.SaveAsJPEG(subImg, fmt.Sprintf("test/tile_%d_%d.jpg", x, y))

			/*
				tile := cur.Value.(*image.RGBA)
				cur = cur.Next()
				minTile = tile
			*/

			count := 0
			tileRect := image.Rect(0, 0, g.config.CompareSize, g.config.CompareSize)

			for cur := g.Tiles.Front(); cur != nil; cur = cur.Next() {
				count++

				tile := cur.Value.(Tile)
				tileImg := tile.Tiny.SubImage(tileRect)
				dist, err := g.Difference(compareImg.(*image.RGBA).SubImage(tileRect), tileImg.(*image.RGBA))
				//log.Printf("%d/%d [%d] diff: %f\n", x, y, count, dist)
				if err != nil {
					log.Println(err)
					continue
				}

				if dist < minDist {
					minDist = dist
					minTile = tile.Tile
					minTileElem = cur
				}
			}

			if minTile == nil {
				log.Println("minTile is nil!")
			} else {
				g.Tiles.Remove(minTileElem)
				draw.Draw(g.SeedImage, rect, minTile, image.ZP, draw.Over)
				g.SaveAsJPEG(g.SeedImage, g.config.OutputImage)
			}

			//draw.Draw(g.SeedImage)
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
