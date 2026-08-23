package main

import (
	"fmt"
	"M365Copilot2ApiX/backend/internal/infra/proxypool/geoip"
)

func main() {
	db := geoip.Get()
	fmt.Println("available:", db.IsAvailable())
	fmt.Println("country 8.8.8.8:", db.LookupCountry("8.8.8.8"))
	fmt.Println("country 114.114.114.114:", db.LookupCountry("114.114.114.114"))
}
