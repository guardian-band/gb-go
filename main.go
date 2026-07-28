package main

import (
	"log"
	"net/http"

	"github.com/guardian-band/gb-go/api"
)

func main() {
	server := api.New()

	log.Fatal(http.ListenAndServe(":3000", server.Router()))
}
