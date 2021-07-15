package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image/jpeg"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/davidbyttow/govips/v2/vips"
	redis "github.com/go-redis/redis/v8"
)

type Importer struct {
	Label    string
	Tilesize int
	Redis    *redis.Client
	Time     time.Duration
	Workers  int
	Total    int
	Current  int
	wg       sync.WaitGroup
	mutex    sync.Mutex
}

func NewImporter(label string, tilesize int, redisAddr string, workers int) (*Importer, error) {
	i := Importer{
		Label:    label,
		Tilesize: tilesize,
		Time:     0,
		Redis:    redis.NewClient(&redis.Options{Addr: redisAddr}),
		Workers:  workers,
		Current:  0,
		mutex:    sync.Mutex{},
		wg:       sync.WaitGroup{},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res := i.Redis.Ping(ctx)
	if res.Err() != nil {
		return nil, res.Err()
	}

	return &i, nil
}

func (i *Importer) Worker(filenameChan chan string) {
	for fn := range filenameChan {
		i.Import(fn)
	}
}

func (i *Importer) AddToTime(d time.Duration) {
	i.mutex.Lock()
	defer i.mutex.Unlock()
	i.Time += d
}

func (i *Importer) Progress() {
	i.mutex.Lock()
	defer i.mutex.Unlock()

	i.Current++

	if i.Current%100 == 0 {
		log.Printf("%d/%d (%.2f%%)\n", i.Current, i.Total, float64(i.Current*100)/float64(i.Total))
	}
}

func (i *Importer) Run(glob string) error {
	images, err := filepath.Glob(glob)
	if err != nil {
		return err
	}

	i.mutex.Lock()
	i.Total = len(images)
	i.mutex.Unlock()

	fnameChan := make(chan string)
	for x := 0; x < i.Workers; x++ {
		go i.Worker(fnameChan)
	}

	for _, filename := range images {
		i.Progress()
		fnameChan <- filename
	}
	close(fnameChan)
	i.wg.Wait()
	return nil
}

func (i *Importer) Import(filename string) {
	tStart := time.Now()
	img, err := vips.NewImageFromFile(filename)
	if err != nil {
		log.Printf("%s: %s\n", filename, err)
		return
	}

	// remove a white frame around the picture
	left, top, width, height, err := img.FindTrim(40, &vips.Color{R: 255, G: 255, B: 255})
	if err != nil {
		log.Printf("%s: %s\n", filename, err)
	}

	if width < img.Width() || height < img.Height() {
		err = img.ExtractArea(left, top, width, height)
		if err != nil {
			log.Printf("%s: %s\n", filename, err)
		}
	}

	err = img.Thumbnail(i.Tilesize, i.Tilesize, vips.InterestingCentre)
	if err != nil {
		log.Printf("%s: %s\n", filename, err)
		return
	}

	avg, err := img.Average()
	if err != nil {
		log.Printf("%s: %s\n", filename, err)
		return
	}

	image, err := img.ToImage(vips.NewDefaultPNGExportParams())
	if err != nil {
		log.Printf("%s: %s\n", filename, err)
		return
	}

	buf := bytes.NewBuffer([]byte{})
	err = jpeg.Encode(buf, image, &jpeg.Options{Quality: 90})
	if err != nil {
		log.Printf("%s: %s\n", filename, err)
		return
	}

	i.AddToTime(time.Now().Sub(tStart))

	k := fmt.Sprintf("%s:%d:%d:%s", i.Label, i.Tilesize, int(avg), filepath.Base(filename))

	res := i.Redis.Set(context.Background(), k, buf.Bytes(), 0)
	if res.Err() != nil {
		log.Printf("%s: %s\n", filename, res.Err())
	}

}

func main() {
	var tileGlob = flag.String("tileglob", "", "import all images that match this glob pattern")
	var label = flag.String("label", "gosaic", "save the tiles using this label")
	var tileSize = flag.Int("tilesize", 100, "crop and scale the tiles to this size")
	var redisAddr = flag.String("redisaddr", "localhost:6379", "import the images into this redis instance")
	var workers = flag.Int("workers", 8, "the number of parallel import workers")

	flag.Parse()

	vips.LoggingSettings(func(messageDomain string, messageLevel vips.LogLevel, message string) {
		log.Println(message)
	}, vips.LogLevelError)

	imp, err := NewImporter(*label, *tileSize, *redisAddr, *workers)
	if err != nil {
		log.Fatal(err)
	}

	err = imp.Run(*tileGlob)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("load time: %s\n", imp.Time)
}
