package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image/jpeg"
	"log"
	"path/filepath"
	"time"

	"github.com/cheggaaa/pb/v3"
	"github.com/davidbyttow/govips/v2/vips"
	redis "github.com/go-redis/redis/v8"
)

func main() {
	var tileGlob = flag.String("tileglob", "", "import all images that match this glob pattern")
	var label = flag.String("label", "gosaic", "save the tiles using this label")
	var tileSize = flag.Int("tilesize", 100, "crop and scale the tiles to this size")
	var redisAddr = flag.String("redisaddr", "localhost:6379", "import the images into this redis instance")

	flag.Parse()

	vips.LoggingSettings(func(messageDomain string, messageLevel vips.LogLevel, message string) {
		log.Println(message)
	}, vips.LogLevelError)

	rdb := redis.NewClient(&redis.Options{
		Addr: *redisAddr,
	})

	images, err := filepath.Glob(*tileGlob)
	if err != nil {
		log.Fatal(err)
	}

	bar := pb.StartNew(len(images))

	tLoad := time.Duration(0)

	for _, filename := range images {
		bar.Increment()
		// log.Println(filename)

		tStart := time.Now()
		img, err := vips.NewImageFromFile(filename)
		if err != nil {
			log.Printf("%s: %s\n", filename, err)
			continue
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

		err = img.Thumbnail(*tileSize, *tileSize, vips.InterestingCentre)
		if err != nil {
			log.Printf("%s: %s\n", filename, err)
			continue
		}

		avg, err := img.Average()
		if err != nil {
			log.Printf("%s: %s\n", filename, err)
			continue
		}

		image, err := img.ToImage(vips.NewDefaultPNGExportParams())
		if err != nil {
			log.Printf("%s: %s\n", filename, err)
			continue
		}

		buf := bytes.NewBuffer([]byte{})
		err = jpeg.Encode(buf, image, &jpeg.Options{Quality: 90})
		if err != nil {
			log.Printf("%s: %s\n", filename, err)
			continue
		}
		tLoad += time.Now().Sub(tStart)

		k := fmt.Sprintf("%s:%d:%d:%s", *label, *tileSize, int(avg), filepath.Base(filename))

		res := rdb.Set(context.Background(), k, buf.Bytes(), 0)
		if res.Err() != nil {
			log.Printf("%s: %s\n", filename, res.Err())
		}
	}

	bar.Finish()

	fmt.Printf("load time: %s\n", tLoad)
}
