package research

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tools"
	webtool "github.com/anemos-labs/sleipnir/internal/tools/web"
)

// Reader is what the research tool reads the web through: web_fetch's reader (internal/tools/web.Reader), so its guard, its
// permission rules and its page cache apply, and a page an agent just read is checked without being fetched again.
type Reader interface {
	Read(ctx context.Context, env *tools.Env, url string) (*webtool.Page, error)
	Search(ctx context.Context, env *tools.Env, query string, max int) ([]webtool.SearchResult, bool, error)
	Backend() string
}

// Hit is one search result, with the index it came from.
type Hit struct {
	In      string
	Title   string
	URL     string
	Snippet string
}

// Indexes are the places the search action looks in. "web" is the configured web search (a key: BRAVE_API_KEY, TAVILY_API_KEY
// or SEARXNG_URL); the others are public indexes that need no key, read through the same fetcher as web_fetch.
var Indexes = []struct{ ID, What string }{
	{"web", "the configured web search engine"},
	{"wikipedia", "Wikipedia articles"},
	{"github", "GitHub repositories, by stars"},
	{"hn", "Hacker News stories and discussions"},
	{"stackoverflow", "Stack Overflow questions"},
	{"papers", "scholarly works (Crossref)"},
	{"npm", "npm packages"},
	{"crates", "Rust crates"},
}

// indexURL is the API address of a keyless index for a query.
func indexURL(in, q string, n int) string {
	e := url.QueryEscape(q)
	switch in {
	case "wikipedia":
		return "https://en.wikipedia.org/w/api.php?action=query&list=search&format=json&srlimit=" + strconv.Itoa(n) + "&srsearch=" + e
	case "github":
		return "https://api.github.com/search/repositories?sort=stars&per_page=" + strconv.Itoa(n) + "&q=" + e
	case "hn":
		return "https://hn.algolia.com/api/v1/search?tags=story&hitsPerPage=" + strconv.Itoa(n) + "&query=" + e
	case "stackoverflow":
		return "https://api.stackexchange.com/2.3/search/advanced?order=desc&sort=relevance&site=stackoverflow&pagesize=" + strconv.Itoa(n) + "&q=" + e
	case "papers":
		return "https://api.crossref.org/works?select=title,URL,container-title,issued,author&rows=" + strconv.Itoa(n) + "&query=" + e
	case "npm":
		return "https://registry.npmjs.org/-/v1/search?size=" + strconv.Itoa(n) + "&text=" + e
	case "crates":
		return "https://crates.io/api/v1/crates?per_page=" + strconv.Itoa(n) + "&q=" + e
	}
	return ""
}

var reHTMLTag = regexp.MustCompile(`<[^>]*>`)

// plain makes an index's snippet one line of text.
func plain(s string, n int) string {
	return clipText(strings.Join(strings.Fields(html.UnescapeString(reHTMLTag.ReplaceAllString(s, ""))), " "), n)
}

// parseIndex reads an index's JSON answer.
func parseIndex(in, body string) ([]Hit, error) {
	var hits []Hit
	switch in {
	case "wikipedia":
		var v struct {
			Query struct {
				Search []struct{ Title, Snippet string } `json:"search"`
			} `json:"query"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Query.Search {
			hits = append(hits, Hit{Title: s.Title, URL: "https://en.wikipedia.org/wiki/" + url.PathEscape(strings.ReplaceAll(s.Title, " ", "_")), Snippet: plain(s.Snippet, 240)})
		}
	case "github":
		var v struct {
			Items []struct {
				FullName    string `json:"full_name"`
				HTMLURL     string `json:"html_url"`
				Description string `json:"description"`
				Stars       int    `json:"stargazers_count"`
				Language    string `json:"language"`
				License     *struct {
					SPDX string `json:"spdx_id"`
				} `json:"license"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Items {
			meta := fmt.Sprintf("★%d", s.Stars)
			if s.Language != "" {
				meta += " · " + s.Language
			}
			if s.License != nil && s.License.SPDX != "" && s.License.SPDX != "NOASSERTION" {
				meta += " · " + s.License.SPDX
			}
			hits = append(hits, Hit{Title: s.FullName, URL: s.HTMLURL, Snippet: plain(meta+" — "+s.Description, 240)})
		}
	case "hn":
		var v struct {
			Hits []struct {
				Title    string `json:"title"`
				URL      string `json:"url"`
				ObjectID string `json:"objectID"`
				Points   int    `json:"points"`
				Comments int    `json:"num_comments"`
			} `json:"hits"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Hits {
			discussion := "https://news.ycombinator.com/item?id=" + s.ObjectID
			u := s.URL
			if u == "" {
				u = discussion
			}
			hits = append(hits, Hit{Title: s.Title, URL: u, Snippet: fmt.Sprintf("%d points, %d comments: %s", s.Points, s.Comments, discussion)})
		}
	case "stackoverflow":
		var v struct {
			Items []struct {
				Title    string   `json:"title"`
				Link     string   `json:"link"`
				Score    int      `json:"score"`
				Answered bool     `json:"is_answered"`
				Tags     []string `json:"tags"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Items {
			state := "unanswered"
			if s.Answered {
				state = "answered"
			}
			hits = append(hits, Hit{Title: plain(s.Title, 200), URL: s.Link, Snippet: fmt.Sprintf("score %d, %s · %s", s.Score, state, strings.Join(s.Tags, ", "))})
		}
	case "papers":
		var v struct {
			Message struct {
				Items []struct {
					Title     []string `json:"title"`
					URL       string   `json:"URL"`
					Container []string `json:"container-title"`
					Issued    struct {
						Parts [][]int `json:"date-parts"`
					} `json:"issued"`
					Author []struct{ Family string } `json:"author"`
				} `json:"items"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Message.Items {
			if len(s.Title) == 0 {
				continue
			}
			var meta []string
			if len(s.Author) > 0 {
				a := s.Author[0].Family
				if len(s.Author) > 1 {
					a += " et al."
				}
				meta = append(meta, a)
			}
			if len(s.Issued.Parts) > 0 && len(s.Issued.Parts[0]) > 0 {
				meta = append(meta, strconv.Itoa(s.Issued.Parts[0][0]))
			}
			if len(s.Container) > 0 {
				meta = append(meta, s.Container[0])
			}
			hits = append(hits, Hit{Title: plain(s.Title[0], 200), URL: s.URL, Snippet: strings.Join(meta, " · ")})
		}
	case "npm":
		var v struct {
			Objects []struct {
				Package struct {
					Name        string `json:"name"`
					Description string `json:"description"`
					Version     string `json:"version"`
					Links       struct {
						Npm        string `json:"npm"`
						Repository string `json:"repository"`
					} `json:"links"`
				} `json:"package"`
			} `json:"objects"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Objects {
			p := s.Package
			u := p.Links.Repository
			if u == "" {
				u = p.Links.Npm
			}
			hits = append(hits, Hit{Title: p.Name + " " + p.Version, URL: u, Snippet: plain(p.Description, 240)})
		}
	case "crates":
		var v struct {
			Crates []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Repository  string `json:"repository"`
				Downloads   int    `json:"downloads"`
				MaxVersion  string `json:"max_version"`
			} `json:"crates"`
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			return nil, err
		}
		for _, s := range v.Crates {
			u := s.Repository
			if u == "" {
				u = "https://crates.io/crates/" + s.Name
			}
			hits = append(hits, Hit{Title: s.Name + " " + s.MaxVersion, URL: u, Snippet: plain(fmt.Sprintf("%d downloads — %s", s.Downloads, s.Description), 240)})
		}
	default:
		return nil, fmt.Errorf("unknown index %q", in)
	}
	for i := range hits {
		hits[i].In = in
	}
	return hits, nil
}

// searchIndex runs one query on one index.
func searchIndex(ctx context.Context, rd Reader, env *tools.Env, in, q string, n int) ([]Hit, error) {
	if in == "web" {
		rs, ok, err := rd.Search(ctx, env, q, n)
		if !ok {
			return nil, fmt.Errorf("no web search engine is configured (BRAVE_API_KEY, TAVILY_API_KEY or SEARXNG_URL); the keyless indexes still work")
		}
		if err != nil {
			return nil, err
		}
		var hits []Hit
		for _, r := range rs {
			hits = append(hits, Hit{In: "web", Title: r.Title, URL: r.URL, Snippet: r.Snippet})
		}
		return hits, nil
	}
	u := indexURL(in, q, n)
	if u == "" {
		return nil, fmt.Errorf("unknown index %q", in)
	}
	page, err := rd.Read(ctx, env, u)
	if err != nil {
		return nil, err
	}
	return parseIndex(in, page.Text)
}

// knownIndex reports whether id names an index.
func knownIndex(id string) bool {
	for _, x := range Indexes {
		if x.ID == id {
			return true
		}
	}
	return false
}
