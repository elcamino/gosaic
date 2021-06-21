package main

import (
	"flag"
	"fmt"
	"image"
	"log"

	"github.com/davidbyttow/govips/v2/vips"
	"github.com/elcamino/gosaic"
)

func main() {
	var (
		seed        = flag.String("seed", "master.jpg", "the seed image")
		tilesGlob   = flag.String("tiles", "tiles/*", "glob for all tiles")
		tileSize    = flag.Int("tilesize", 100, "size of each tile")
		outputSize  = flag.Int("outputsize", 2000, "size of the output file")
		output      = flag.String("output", "mosaic.jpg", "the mosaic output file")
		comparesize = flag.Int("comparesize", 20, "the size to which to scale pictures before comparing them for their distance")
	)

	flag.Parse()

	g, err := gosaic.New(gosaic.Config{
		SeedImage:   *seed,
		TilesGlob:   *tilesGlob,
		TileSize:    *tileSize,
		OutputSize:  *outputSize,
		OutputImage: *output,
		CompareSize: *comparesize,
	})
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