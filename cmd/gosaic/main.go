package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path"
	"runtime"
	"runtime/pprof"
	"strings"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/elcamino/gosaic"
	"github.com/sirupsen/logrus"
	log "github.com/sirupsen/logrus"
)

var (
	seed        = flag.String("seed", "", "the seed image")
	tilesGlob   = flag.String("tiles", "", "glob for all tiles")
	tileSize    = flag.Int("tilesize", 100, "size of each tile")
	outputSize  = flag.Int("outputsize", 2000, "size of the output file")
	output      = flag.String("output", "mosaic.jpg", "the mosaic output file")
	comparesize = flag.Int("comparesize", 50, "the size to which to scale pictures before comparing them for their distance")
	comparedist = flag.Int("comparedist", 30, "only compare image whose average color is this far apart")
	unique      = flag.Bool("unique", true, "use each tile only once")
	cpuprofile  = flag.String("cpuprofile", "", "profile the CPU usage to this file")
	smartcrop   = flag.Bool("smartcrop", false, "perform smart cropping of the tiles")
	progressbar = flag.Bool("progressbar", false, "show a progress bar when loading tiles and building the mosaic")
	redisAddr   = flag.String("redisaddr", "127.0.0.1:6379", "use the tile cache at this redis address")
	redisLabel  = flag.String("redislabel", "interesting", "load cached tiles with this label")
	httpAddr    = flag.String("http-address", "", "run the REST API server at this address")
	apiKey      = flag.String("api-key", "", "the API key with which to authenticate requests")
	loglevel    = flag.String("loglevel", "error", "the loglevel")
)

type lineNumberHook struct {
	skip int
}

func (hook *lineNumberHook) Levels() []log.Level {
	return log.AllLevels
}

func (hook *lineNumberHook) Fire(entry *log.Entry) error {
	// determine the call stack skip level for logrus to print the calling file/function/line number
	if hook.skip == -1 {
		i := 0
		for {
			pc, file, _, ok := runtime.Caller(i)

			if !ok {
				hook.skip = -2
				break
			}

			fname := runtime.FuncForPC(pc).Name()
			if !strings.Contains(file, "sirupsen/logrus") && !strings.Contains(fname, "lineNumberHook") {
				hook.skip = i
				break
			}

			i++
		}
	}

	// don't try to add the file/func/line number info if the skip level couldn't be determined
	if hook.skip < 0 {
		return nil
	}

	// add the file, func name and line number in each log entry
	if pc, file, line, ok := runtime.Caller(hook.skip); ok {
		funcName := runtime.FuncForPC(pc).Name()

		entry.Data["src"] = fmt.Sprintf("%s:%v:%s", path.Base(file), line, path.Base(funcName))
	}

	return nil
}

func runServer() error {
	srv, err := gosaic.NewServer(*httpAddr, *redisAddr)
	if err != nil {
		return err
	}
	return srv.Run()
}

func main() {

	flag.Parse()

	level, err := logrus.ParseLevel(*loglevel)
	if err != nil {
		log.Fatal(err)
	}
	log.SetLevel(level)
	log.AddHook(&lineNumberHook{skip: -1})

	if *httpAddr != "" {
		runServer()
		return
	}

	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}

	config := gosaic.Config{
		SeedImage:   *seed,
		TilesGlob:   *tilesGlob,
		TileSize:    *tileSize,
		OutputSize:  *outputSize,
		OutputImage: *output,
		CompareSize: *comparesize,
		CompareDist: float64(*comparedist),
		Unique:      *unique,
		SmartCrop:   *smartcrop,
		ProgressBar: *progressbar,
		RedisAddr:   *redisAddr,
		RedisLabel:  *redisLabel,
	}

	g, err := gosaic.New(config)
	if err != nil {
		log.Fatal(err)
	}

	g.Build()
}

func main2() {
	g, err := gosaic.New(gosaic.Config{
		SeedImage:   "master.jpg",
		TilesGlob:   "./losangeles/*.jpg",
		TileSize:    100,
		OutputSize:  2000,
		OutputImage: "losangeles-mosaic.jpg",
	})
	if err != nil {
		log.Fatal(err)
	}

	img1Path := "master.jpg"
	img1, err := vips.NewImageFromFile(img1Path)
	if err != nil {
		log.Fatalf("%s: %s", img1Path, err)
	}
	//img1.SmartCrop(100, 100, vips.InterestingCentre)

	rect := image.Rect(0, 0, 100, 100)
	iimg1, err := img1.ToImage(vips.NewDefaultPNGExportParams())
	if err != nil {
		log.Fatal(err)
	}

	extr1 := iimg1.(*image.RGBA).SubImage(rect)

	for cur := g.Tiles.Front(); cur != nil; cur = cur.Next() {
		tile := cur.Value.(*image.RGBA)
		extr0 := tile.SubImage(rect)

		similarity, err := g.Difference(extr0, extr1)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s difference to master.png: %f\n", img1Path, similarity)
	}

}
