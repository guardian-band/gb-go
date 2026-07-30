package main

import (
	"context"
	"log"
	"net/http"

	"github.com/guardian-band/gb-go/api"
)

func main() {
	server, err := api.New(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	defer server.Close()

	log.Fatal(http.ListenAndServe(":3000", server.Router()))
}
