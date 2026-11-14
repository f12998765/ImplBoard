package main

import (
	"log"

	"github.com/egoist/mygo"

	"implboard/internal/boot"
)

func main() {
	homeURL, err := boot.Start("")
	if err != nil {
		log.Fatal(err)
	}

	mygo.App.WhenReady(func() {
		mygo.NewWindow(mygo.WindowOptions{
			Title:     "实施看板",
			URL:       homeURL + "/",
			Width:     1360,
			Height:    900,
			MinWidth:  980,
			MinHeight: 640,
		})
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
