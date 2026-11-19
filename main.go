package main

import (
	"log"

	"gravitybox-mygo/internal/ui"
)

func main() {
	if err := ui.Run(); err != nil {
		log.Fatal(err)
	}
}
