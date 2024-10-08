package main

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
	ics "github.com/arran4/golang-ical"
	"golang.org/x/net/html"
)

type Day struct {
	Date  time.Time
	Proms []Prom
}

type Prom struct {
	Id                   string
	Start, End           time.Time
	Name, Location, Desc string
	Programme            []Work
	Performers           []Performer
	Url                  string
}

type Work struct {
	Composer, Name string
	Duration       int
	Interval       bool
}

type Performer struct {
	Name, Role string
}

var (
	cacheTime, _ = time.ParseDuration("1h")
	cache        []Prom
	cacheUpdated time.Time
)

var lon, _ = time.LoadLocation("Europe/London")

func main() {
	http.HandleFunc("/proms/", promsList)
	http.HandleFunc("/proms/{id}", promIcal)
	http.Handle("/proms/static/",
		http.StripPrefix("/proms/static/", http.FileServer(http.Dir("./static"))))
	http.ListenAndServe("127.0.0.1:1895", nil)
}

var londonLocs = []string{
	"Royal Albert Hall",
	"Battersea Arts Centre",
	"Printworks London",
}

func cachedProms() []Prom {
	if len(cache) == 0 || time.Since(cacheUpdated) > cacheTime {
		cache = refreshPromsList()
		cacheUpdated = time.Now()
	}
	return cache
}

func refreshPromsList() []Prom {
	var bbcList io.Reader
	if localFile := os.Getenv("LOCAL"); localFile == "" {
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
		bbcList = res.Body
	} else {
		bbcList, _ = os.Open(localFile)
	}
	doc, _ := html.Parse(bbcList)

	proms := []Prom{}

	for _, dayNode := range cascadia.QueryAll(doc,
		mustParseSel("li[data-id-for-tests='event-summaries-date-section']"),
	) {
		dateStr := textBySel(dayNode, "h3")

		for _, promNode := range cascadia.QueryAll(dayNode,
			mustParseSel("li[data-id-for-tests='event-summary']"),
		) {
			name := textBySel(promNode, ".ev-event-calendar__name")
			startTime := textBySel(promNode, ".ev-event-calendar__time")
			startStr := fmt.Sprintf("%s %s", dateStr, startTime)
			start, err := time.ParseInLocation("Mon 2 Jan 2006 15:04", startStr, lon)
			if err != nil {
				log.Printf("Malformed date for prom: %s", name)
				continue // skip it
			}
			prom := Prom{
				Start:    start,
				Name:     name,
				Location: textBySel(promNode, ".ev-event-calendar__event-location"),
				Desc:     textBySel(promNode, ".ev-event-calendar__event-description"),
			}

			for _, attr := range cascadia.Query(promNode, mustParseSel("a")).Attr {
				if attr.Key == "href" {
					prom.Id = attr.Val[strings.LastIndex(attr.Val, "/")+1:]
					prom.Url = "https://www.bbc.co.uk" + attr.Val
				}
			}

			for _, progNode := range cascadia.QueryAll(promNode,
				mustParseSel(".ev-act-schedule__performance-composer-segments-list>li"),
			) {
				if interval := cascadia.Query(progNode,
					mustParseSel(".ev-act-schedule__performance-segment-interval"),
				); interval != nil {
					prom.Programme = append(prom.Programme, Work{Interval: true, Duration: 20})
				} else {
					composer := textBySel(progNode, ".ev-act-schedule__performance-composers")
					for _, workNode := range cascadia.QueryAll(progNode,
						mustParseSel(".ev-act-schedule__performance-segment"),
					) {
						durStr := textBySel(workNode, ".ev-act-schedule__performance-work-duration")
						re := regexp.MustCompile(`\d+`)
						duration, _ := strconv.Atoi(re.FindString(durStr))
						prom.Programme = append(prom.Programme, Work{
							Composer: composer,
							Name:     textBySel(workNode, ".ev-act-schedule__performance-work-name"),
							Duration: duration,
						})
					}
				}
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

			prom.End = calculateEnd(&prom)

			proms = append(proms, prom)
		}
	}

	return proms
}

func promsList(w http.ResponseWriter, _ *http.Request) {
	var (
		days []Day
		day  Day
	)

	proms := cachedProms()

	for _, p := range proms {
		t := time.Date(p.Start.Year(), p.Start.Month(), p.Start.Day(), 0, 0, 0, 0, lon)
		if day.Date.IsZero() {
			day.Date = t
		} else if t != day.Date {
			days = append(days, day)
			day = Day{Date: t}
		}
		for _, loc := range londonLocs {
			if p.Location == loc {
				day.Proms = append(day.Proms, p)
			}
		}
	}
	days = append(days, day)

	t := template.New("proms.html.tmpl").Funcs(map[string]any{
		"icaltime": icalTime,
	})
	if _, err := t.ParseFiles("proms.html.tmpl", "ical.txt.tmpl"); err != nil {
		log.Fatal(err)
	}
	if err := t.Execute(w, days); err != nil {
		log.Fatal(err)
	}
}

func promIcal(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("id"), ".ics")
	var p *Prom
	if p = promById(cachedProms(), id); p == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	cal := ics.NewCalendar()
	event := cal.AddEvent(id + "@h5s.org")
	event.SetSummary(p.Name)
	event.SetStartAt(p.Start)
	event.SetEndAt(p.End)
	event.SetLocation(p.Location)
	event.SetURL(p.Url)

	var desc bytes.Buffer
	t, err := template.ParseFiles("ical.txt.tmpl")
	if err != nil {
		log.Fatal(err)
	}
	err = t.Execute(&desc, p)
	event.SetDescription(desc.String())

	// reminder 1 day before
	preAlarm := event.AddAlarm()
	preAlarm.SetAction(ics.ActionAudio)
	preAlarm.SetTrigger("-P1D")

	// ticket reminder at 10:25
	tktAlarm := event.AddAlarm()
	tktAlarm.SetAction(ics.ActionAudio)
	tktTime := time.Date(p.Start.Year(), p.Start.Month(), p.Start.Day(), 10, 25, 0, 0, lon)
	tktAlarm.SetTrigger(icalTime(tktTime))

	w.Header().Add("Content-Type", "text/calendar")
	cal.SerializeTo(w)
}

func icalTime(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

func promById(proms []Prom, id string) *Prom {
	for _, v := range proms {
		if v.Id == id {
			return &v
		}
	}
	return nil
}

func calculateEnd(p *Prom) time.Time {
	dur := 20 // for interval
	for _, w := range p.Programme {
		dur += w.Duration
	}
	d, _ := time.ParseDuration(fmt.Sprintf("%dm", dur))
	return p.Start.Add(d)
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
	return strings.TrimSpace(textContent(cascadia.Query(node, mustParseSel(class))))
}
