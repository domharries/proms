package main

import (
	"embed"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

//go:embed *.tmpl *.css
var tmpl embed.FS

type Day struct {
	Date  time.Time
	Proms []Prom
}

type Prom struct {
	Time, Name, Location, Desc string
	Programme                  []Work
	Performers                 []Performer
	Url                        string
}

type Work struct {
	Composer, Name, Duration string
}

type Performer struct {
	Name, Role string
}

func main() {
	http.HandleFunc("/proms/", promsList)
	http.Handle("/proms/static/",
		http.StripPrefix("/proms/static/", http.FileServer(http.Dir(""))))
	http.ListenAndServe(":1895", nil)
}

var londonLocs = []string{
	"Royal Albert Hall",
	"Battersea Arts Centre",
	"Printworks London",
}

func promsList(w http.ResponseWriter, req *http.Request) {
	year := time.Now().Year()
	url := fmt.Sprintf("https://www.bbc.co.uk/proms/events/by/date/%d", year)
	res, err := http.Get(url)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Fatalf("status code error: %d %s", res.StatusCode, res.Status)
	}

	doc, err := html.Parse(res.Body)
	// f, _ := os.Open("2022.html")
	// doc, err := html.Parse(f)

	days := []Day{}

	for _, dayNode := range cascadia.QueryAll(doc,
		mustParseSel("li[data-id-for-tests='event-summaries-date-section']"),
	) {
		dateStr := textContent(cascadia.Query(dayNode, mustParseSel("h3")))
		date, err := time.Parse("Mon 2 Jan 2006", strings.TrimSpace(dateStr))
		if err != nil {
			log.Fatalf("Could not parse date: %s", dateStr)
		}

		day := Day{Date: date}

		for _, promNode := range cascadia.QueryAll(dayNode,
			mustParseSel("li[data-id-for-tests='event-summary']"),
		) {
			prom := Prom{
				Time:     textBySel(promNode, ".ev-event-calendar__time"),
				Name:     textBySel(promNode, ".ev-event-calendar__name"),
				Location: textBySel(promNode, ".ev-event-calendar__event-location"),
				Desc:     textBySel(promNode, ".ev-event-calendar__event-description"),
			}

			for _, attr := range cascadia.Query(promNode, mustParseSel("a")).Attr {
				if attr.Key == "href" {
					prom.Url = "https://www.bbc.co.uk" + attr.Val
				}
			}

			for _, workNode := range cascadia.QueryAll(promNode,
				mustParseSel(".ev-act-schedule__performance-composer-segments"),
			) {
				prom.Programme = append(prom.Programme, Work{
					Composer: textBySel(workNode, ".ev-act-schedule__performance-composers"),
					Name:     textBySel(workNode, ".ev-act-schedule__performance-work-name"),
					Duration: textBySel(workNode, ".ev-act-schedule__performance-work-duration"),
				})
			}

			for _, perfNode := range cascadia.QueryAll(promNode,
				mustParseSel("div[data-id-for-tests='event-schedule-artists'] "+
					".ev-act-schedule__artist"),
			) {
				prom.Performers = append(prom.Performers, Performer{
					Name: textBySel(perfNode, ".ev-act-schedule__artist-name"),
					Role: textBySel(perfNode, ".ev-act-schedule__artist-role-container"),
				})
			}
			sort.Slice(prom.Performers, func(i, j int) bool {
				return prom.Performers[i].Role < prom.Performers[j].Role
			})

			for _, loc := range londonLocs {
				if prom.Location == loc {
					day.Proms = append(day.Proms, prom)
				}
			}
		}

		days = append(days, day)
	}

	t, _ := template.ParseFiles("proms.html.tmpl")
	err = t.Execute(w, days)
	if err != nil {
		log.Fatal(err)
	}
}

func mustParseSel(s string) cascadia.Sel {
	selector, err := cascadia.Parse(s)
	if err != nil {
		log.Fatalf("Bad selector: %s", s)
	}
	return selector
}

func textContent(node *html.Node) string {
	if node == nil {
		return ""
	}
	text := ""
	for c := node.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.TextNode:
			text += c.Data
		case html.ElementNode:
			text += textContent(c)
		}
	}
	return text
}

func textBySel(node *html.Node, class string) string {
	return textContent(cascadia.Query(node, mustParseSel(class)))
}
