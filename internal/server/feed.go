package server

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/shiroha-a/mk/internal/model"
	"github.com/shiroha-a/mk/internal/repository"
)

// feedNoteLimit mirrors upstream FeedService (take: 20).
const feedNoteLimit = 20

// feedEntry is one note rendered into a feed-agnostic shape.
type feedEntry struct {
	Title    string
	Link     string
	Date     time.Time
	Summary  string // note.cw
	Content  string // MFM -> HTML
	ImageURL string
}

// feedData carries everything the three serialisations need.
type feedData struct {
	ID          string
	Title       string
	Description string
	Link        string
	Updated     time.Time
	ImageURL    string
	AuthorName  string
	AuthorLink  string
	Entries     []feedEntry
}

// --- RSS 2.0 ---

type rssRoot struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	LastBuild   string    `xml:"lastBuildDate,omitempty"`
	Generator   string    `xml:"generator"`
	Items       []rssItem `xml:"item"`
	Image       *rssImage `xml:"image,omitempty"`
}

type rssImage struct {
	URL   string `xml:"url"`
	Title string `xml:"title"`
	Link  string `xml:"link"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description,omitempty"`
}

func (f *feedData) rss2() ([]byte, error) {
	ch := rssChannel{
		Title:       f.Title,
		Link:        f.Link,
		Description: f.Description,
		Generator:   "Misskey",
	}
	if !f.Updated.IsZero() {
		ch.LastBuild = f.Updated.UTC().Format(time.RFC1123Z)
	}
	if f.ImageURL != "" {
		ch.Image = &rssImage{URL: f.ImageURL, Title: f.Title, Link: f.Link}
	}
	for _, e := range f.Entries {
		body := e.Content
		if e.Summary != "" {
			body = e.Summary
		}
		ch.Items = append(ch.Items, rssItem{
			Title:       e.Title,
			Link:        e.Link,
			GUID:        e.Link,
			PubDate:     e.Date.UTC().Format(time.RFC1123Z),
			Description: body,
		})
	}
	out, err := xml.Marshal(rssRoot{Version: "2.0", Channel: ch})
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// --- Atom 1.0 ---

type atomFeed struct {
	XMLName  xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID       string      `xml:"id"`
	Title    string      `xml:"title"`
	Updated  string      `xml:"updated"`
	Subtitle string      `xml:"subtitle,omitempty"`
	Logo     string      `xml:"logo,omitempty"`
	Links    []atomLink  `xml:"link"`
	Author   *atomAuthor `xml:"author,omitempty"`
	Entries  []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomAuthor struct {
	Name string `xml:"name"`
	URI  string `xml:"uri,omitempty"`
}

type atomEntry struct {
	ID      string       `xml:"id"`
	Title   string       `xml:"title"`
	Updated string       `xml:"updated"`
	Link    atomLink     `xml:"link"`
	Summary string       `xml:"summary,omitempty"`
	Content *atomContent `xml:"content,omitempty"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Body string `xml:",chardata"`
}

func (f *feedData) atom1() ([]byte, error) {
	updated := f.Updated
	if updated.IsZero() {
		updated = time.Unix(0, 0).UTC()
	}
	af := atomFeed{
		ID:       f.ID,
		Title:    f.Title,
		Updated:  updated.UTC().Format(time.RFC3339),
		Subtitle: f.Description,
		Logo:     f.ImageURL,
		Links: []atomLink{
			{Href: f.Link, Rel: "alternate"},
			{Href: f.Link + ".atom", Rel: "self", Type: "application/atom+xml"},
		},
		Author: &atomAuthor{Name: f.AuthorName, URI: f.AuthorLink},
	}
	for _, e := range f.Entries {
		ae := atomEntry{
			ID:      e.Link,
			Title:   e.Title,
			Updated: e.Date.UTC().Format(time.RFC3339),
			Link:    atomLink{Href: e.Link, Rel: "alternate"},
			Summary: e.Summary,
		}
		if e.Content != "" {
			ae.Content = &atomContent{Type: "html", Body: e.Content}
		}
		af.Entries = append(af.Entries, ae)
	}
	out, err := xml.Marshal(af)
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}

// --- JSON Feed 1.1 ---

func (f *feedData) jsonFeed() ([]byte, error) {
	items := make([]map[string]any, 0, len(f.Entries))
	for _, e := range f.Entries {
		item := map[string]any{
			"id":             e.Link,
			"url":            e.Link,
			"title":          e.Title,
			"date_published": e.Date.UTC().Format(time.RFC3339),
		}
		if e.Content != "" {
			item["content_html"] = e.Content
		}
		if e.Summary != "" {
			item["summary"] = e.Summary
		}
		if e.ImageURL != "" {
			item["image"] = e.ImageURL
		}
		items = append(items, item)
	}
	doc := map[string]any{
		"version":       "https://jsonfeed.org/version/1.1",
		"title":         f.Title,
		"home_page_url": f.Link,
		"feed_url":      f.Link + ".json",
		"description":   f.Description,
		"authors":       []map[string]any{{"name": f.AuthorName, "url": f.AuthorLink}},
		"items":         items,
	}
	if f.ImageURL != "" {
		doc["icon"] = f.ImageURL
	}
	return json.Marshal(doc)
}

// --- handler ---

// FeedNoteLister returns the notes a user's feed should contain.
type FeedNoteLister interface {
	ListPublicNotesForFeed(userID string, limit int) ([]*model.Note, error)
}

// FeedUserResolver resolves a local user by its username.
type FeedUserResolver interface {
	FindLocalByUsername(username string) (*model.User, error)
}

// feedHandler serves /@:user.rss / .atom / .json.
type feedHandler struct {
	baseURL string
	host    string
	users   FeedUserResolver
	notes   FeedNoteLister
	// parseTime は note.id から投稿日時を得る。upstream も
	// idService.parse(note.id).date を使っており、note 行に createdAt は無い。
	parseTime func(id string) (time.Time, error)
	profiles  func(userID string) *model.UserProfile
	avatarURL func(u *model.User) string
	toHTML    func(text string) string
}

// serve builds the feed for the requested user and hands it to render.
//
// upstream は存在しないユーザーに 404 を返す。SPA catchall より前に登録して
// あるので、ここで 404 を返せばそのままクライアントに届く。
func (h *feedHandler) serve(c echo.Context, render func(*feedData) ([]byte, error), contentType string) error {
	username := strings.TrimSpace(c.Param("user"))
	if username == "" {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	u, err := h.users.FindLocalByUsername(username)
	if err != nil || u == nil {
		return echo.NewHTTPError(http.StatusNotFound)
	}

	data := h.build(u)
	out, err := render(data)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError)
	}
	return c.Blob(http.StatusOK, contentType, out)
}

func (h *feedHandler) build(u *model.User) *feedData {
	name := u.Username
	if u.Name != nil && *u.Name != "" {
		name = *u.Name
	}
	authorLink := h.baseURL + "/@" + u.Username

	data := &feedData{
		ID:         authorLink,
		Title:      name + " (@" + u.Username + "@" + h.host + ")",
		Link:       authorLink,
		AuthorName: name,
		AuthorLink: authorLink,
	}
	if h.avatarURL != nil {
		data.ImageURL = h.avatarURL(u)
	}
	data.Description = h.describe(u)

	notes, err := h.notes.ListPublicNotesForFeed(u.ID, feedNoteLimit)
	if err != nil {
		notes = nil
	}
	for _, n := range notes {
		e := feedEntry{
			Title: "New note by " + name,
			Link:  h.baseURL + "/notes/" + n.ID,
		}
		if h.parseTime != nil {
			if t, err := h.parseTime(n.ID); err == nil {
				e.Date = t
			}
		}
		if n.CW != nil {
			e.Summary = *n.CW
		}
		if n.Text != nil && *n.Text != "" && h.toHTML != nil {
			e.Content = h.toHTML(*n.Text)
		}
		data.Entries = append(data.Entries, e)
	}
	if len(data.Entries) > 0 {
		data.Updated = data.Entries[0].Date
	}
	return data
}

// describe mirrors upstream の description 組み立て。
// followingVisibility / followersVisibility が public でなければ数を伏せる。
func (h *feedHandler) describe(u *model.User) string {
	followingVis, followersVis, bio := "public", "public", ""
	if h.profiles != nil {
		if p := h.profiles(u.ID); p != nil {
			followingVis, followersVis = string(p.FollowingVisibility), string(p.FollowersVisibility)
			if p.Description != nil {
				bio = *p.Description
			}
		}
	}
	hide := func(vis string, n int) string {
		if vis != "public" {
			return "?"
		}
		return strconv.Itoa(n)
	}
	desc := strconv.Itoa(u.NotesCount) + " Notes, " +
		hide(followingVis, u.FollowingCount) + " Following, " +
		hide(followersVis, u.FollowersCount) + " Followers"
	if bio != "" {
		desc += " · " + bio
	}
	return desc
}

func (h *feedHandler) RSS(c echo.Context) error {
	return h.serve(c, (*feedData).rss2, "application/rss+xml; charset=utf-8")
}

func (h *feedHandler) Atom(c echo.Context) error {
	return h.serve(c, (*feedData).atom1, "application/atom+xml; charset=utf-8")
}

func (h *feedHandler) JSON(c echo.Context) error {
	return h.serve(c, (*feedData).jsonFeed, "application/json; charset=utf-8")
}

// feedUserResolver adapts repository.UserRepository to FeedUserResolver.
//
// フィードはローカルユーザーのみ対象なので host=nil で引く。
type feedUserResolver struct {
	repo repository.UserRepository
}

func (r feedUserResolver) FindLocalByUsername(username string) (*model.User, error) {
	return r.repo.FindByUsernameLower(strings.ToLower(username), nil)
}
