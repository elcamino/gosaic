package gosaic

import (
	"bytes"
	"container/list"
	"context"
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/davidbyttow/govips/v2/vips"
	redis "github.com/go-redis/redis/v8"
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
	RedisAddr   string
	RedisLabel  string
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
	rdb           *redis.Client
}

func (g *Gosaic) diff(a, b uint32) int32 {
	if a > b {
		return int32(a - b)
	}
	return int32(b - a)
}

func (g *Gosaic) loadTilesFromRedis() error {
	var cursor uint64
	tRedis := time.Duration(0)

	keyPattern := fmt.Sprintf("%s:%d:*.jpg", g.config.RedisLabel, g.config.CompareSize)
	keys := []string{}
	cmd := g.rdb.Scan(context.Background(), cursor, keyPattern, 1000)
	iter := cmd.Iterator()
	for iter.Next(context.Background()) {
		keys = append(keys, iter.Val())
	}

	var bar ProgressIndicator
	if g.config.ProgressBar {
		bar = pb.StartNew(len(keys))
	} else {
		bar = &ProgressCounter{count: 0, max: uint64(len(keys))}
	}

	for _, k := range keys {
		bar.Increment()
		tStart := time.Now()

		keyParts := strings.Split(k, ":")
		avg, err := strconv.Atoi(keyParts[2])
		if err != nil {
			log.Println(err)
			continue
		}

		data, err := g.rdb.Get(context.Background(), k).Bytes()
		if err != nil {
			log.Println(err)
			continue
		}

		buf := bytes.NewBuffer(data)
		img, err := jpeg.Decode(buf)
		if err != nil {
			log.Println(err)
			continue
		}

		// tile = g.BuildTile()
		tile, err := g.buildTile(img, k, avg)
		if err != nil {
			log.Println(err)
			continue
		}
		g.Tiles.PushBack(tile)

		tRedis += time.Now().Sub(tStart)
	}

	bar.Finish()
	return nil
}

func (g *Gosaic) buildTile(img image.Image, label string, avg int) (Tile, error) {
	var err error

	defer func() {
		if r := recover(); r != nil {
			log.Println(r)
			err = errors.New("failed to cast image to RGBA")
		}
	}()

	b := img.Bounds()
	m := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(m, m.Bounds(), img, b.Min, draw.Src)

	tile := Tile{
		Filename: label,
		Average:  float64(avg),
		Tiny:     m,
	}

	return tile, err
}

func (g *Gosaic) loadTilesFromDisk() error {
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

				tile, err := g.loadTileFromDisk(path, g.config.CompareSize)
				if err != nil {
					log.Printf("%s: %s\n", path, err)
					continue
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

func (g *Gosaic) loadTileFromRedis(key string, size int) (Tile, error) {
	tile := Tile{Filename: key}
	//tStart := time.Now()

	keyParts := strings.Split(key, ":")
	keyParts[1] = fmt.Sprintf("%d", size)
	avg, err := strconv.Atoi(keyParts[2])
	if err != nil {
		return tile, err
	}

	keyParts[2] = "*"
	keyPattern := strings.Join(keyParts, ":")
	var cursor uint64
	resp := g.rdb.Scan(context.Background(), cursor, keyPattern, 100)
	iter := resp.Iterator()
	var imgKey string
	if iter.Next(context.Background()) {
		imgKey = iter.Val()
	}
	if err != nil {
		log.Println(err)
		return tile, err
	}
	data, err := g.rdb.Get(context.Background(), imgKey).Bytes()
	if err != nil {
		return tile, err
	}

	buf := bytes.NewBuffer(data)
	img, err := jpeg.Decode(buf)
	if err != nil {
		return tile, nil
	}

	b := img.Bounds()
	m := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(m, m.Bounds(), img, b.Min, draw.Src)

	tile.Tiny = m
	tile.Average = float64(avg)

	return tile, nil
}

func (g *Gosaic) loadTileFromDisk(filename string, size int) (Tile, error) {
	imgRef, err := vips.NewImageFromFile(filename)
	if err != nil {
		return Tile{}, err
	}

	// remove a white frame around the picture
	left, top, width, height, err := imgRef.FindTrim(40, &vips.Color{R: 255, G: 255, B: 255})
	if err != nil {
		return Tile{}, err
	}

	if width < imgRef.Width() || height < imgRef.Height() {
		err = imgRef.ExtractArea(left, top, width, height)
		if err != nil {
			return Tile{}, err
		}
	}

	err = imgRef.ToColorSpace(vips.InterpretationSRGB)
	if err != nil {
		return Tile{}, err
	}

	avg, err := imgRef.Average()
	if err != nil {
		return Tile{}, err
	}

	if g.config.SmartCrop {
		err = imgRef.SmartCrop(size, size, vips.InterestingAttention)
	} else {
		err = imgRef.Thumbnail(size, size, vips.InterestingAttention)
	}
	if err != nil {
		return Tile{}, err
	}

	img, err := imgRef.ToImage(vips.NewDefaultPNGExportParams())
	return Tile{Tiny: img, Average: avg, Filename: filename}, err
}

func (g *Gosaic) Build() {
	tBuildStart := time.Now()
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
					if tile.Tiny == nil {
						log.Printf("%s has empty image data\n", tile.Filename)
						continue
					}

					if math.Abs(tile.Average-avg) > g.config.CompareDist {
						continue
					}

					tileImg := tile.Tiny
					dist, err := g.Difference(
						compareImg.(*image.RGBA).SubImage(tileRect),
						tileImg.(*image.RGBA),
					)
					if err != nil {
						log.Println(err)
						continue
					}

					tileMutex.Lock()
					comparisons++
					tCompare += time.Now().Sub(tStart)

					if dist < minDist {
						minTile = tile
						minDist = dist
						minTileElem = elem
					}
					tileMutex.Unlock()
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

			var tile Tile
			if g.rdb != nil {
				tile, err = g.loadTileFromRedis(minTile.Filename, g.config.TileSize)
				if err != nil {
					log.Println(err)
					continue
				}
			} else {
				tile, err = g.loadTileFromDisk(minTile.Filename, g.config.TileSize)
			}
			if err != nil {
				log.Println(err)
				continue
			}
			draw.Draw(g.SeedImage, rect, tile.Tiny, image.ZP, draw.Over)
		}
		tLoad += time.Now().Sub(tStart)

		bar.Increment()
	}

	bar.Finish()

	log.Printf("Comparisons: %d\n", comparisons)
	log.Printf("Rect time: %s\n", tRect)
	log.Printf("Compare time: %s\n", tCompare)
	log.Printf("Load time: %s\n", tLoad)
	log.Printf("Total time: %s\n", time.Now().Sub(tBuildStart))
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

	img.Resize(scaleFactor, vips.KernelAuto)

	// Create the mosaic
	g := Gosaic{
		config:        config,
		seedVIPSImage: img,
		Tiles:         list.New(),
		scaleFactor:   scaleFactor,
	}

	if config.RedisAddr != "" {
		g.rdb = redis.NewClient(&redis.Options{
			Addr:     config.RedisAddr,
			Password: "", // no password set
			DB:       0,  // use default DB
		})

		resp := g.rdb.Ping(context.Background())
		if resp.Err() != nil {
			return nil, err
		}
	}

	seed, err := img.ToImage(vips.NewDefaultPNGExportParams())
	if err != nil {
		return nil, err
	}

	g.SeedImage = seed.(*image.RGBA)
	if g.config.RedisAddr != "" && g.config.RedisLabel != "" {
		g.loadTilesFromRedis()
	} else {
		g.loadTilesFromDisk()
	}

	return &g, nil
}
