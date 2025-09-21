package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

func ExampleScrape() {
	// Request the HTML page.
	res, err := http.Get("https://www.zdf.de/spielfilm-highlights-104")
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Fatalf("status code error: %d %s", res.StatusCode, res.Status)
	}

	// Load the HTML document
	doc, err := goquery.NewDocumentFromReader(res.Body)
	if err != nil {
		log.Fatal(err)
	}

	doc.Find("div[data-testid='teaser-tile']").Each(func(i int, s *goquery.Selection) {
		link := getField(s, "a@href")
		title := getField(s, "h3")

		fmt.Printf("Title %d: %s\n", i, title)
		fmt.Printf("URL %d: %s\n", i, link)
	})
}

func getField(s *goquery.Selection, selector string) string {
	parts := strings.SplitN(selector, "@", 2)
	el := s.Find(parts[0])
	if len(parts) == 2 {
		val, _ := el.Attr(parts[1])
		return val
	}
	return el.Text()
}

func main() {
	ExampleScrape()
}
