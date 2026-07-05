// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	size := 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// 배경
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r := uint8(30 + x*20/size)
			g := uint8(30 + y*20/size)
			b := uint8(50 + (x+y)*15/size)
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}

	// 중앙 블록
	cx, cy := size/2, size/2
	for y := cy - 60; y <= cy+60; y++ {
		for x := cx - 80; x <= cx+80; x++ {
			if x >= 0 && x < size && y >= 0 && y < size {
				img.Set(x, y, color.RGBA{0, 150, 220, 255})
			}
		}
	}

	f, err := os.Create(os.Args[1])
	if err != nil {
		panic(err)
	}
	defer f.Close()
	png.Encode(f, img)
}
