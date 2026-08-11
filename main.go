package main

import (
	"context"
	"flag"
	"log"

	emqxcloudprovider "github.com/emqx/terraform-provider-emqxcloud/internal/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with debugger support")
	flag.Parse()

	err := providerserver.Serve(
		context.Background(),
		emqxcloudprovider.New(version),
		providerserver.ServeOpts{
			Address:         "registry.terraform.io/emqx/emqxcloud",
			Debug:           debug,
			ProtocolVersion: 6,
		},
	)
	if err != nil {
		log.Fatal(err)
	}
}
