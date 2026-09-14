package directory

import (
	"goblog/blog"
	gplugin "goblog/plugin"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestPlugin_Identity(t *testing.T) {
	p := New()
	if p.Name() != "directory" || p.DisplayName() == "" || p.Version() == "" {
		t.Errorf("identity: %q %q %q", p.Name(), p.DisplayName(), p.Version())
	}
	defaults := map[string]string{}
	for _, s := range p.Settings() {
		defaults[s.Key] = s.DefaultValue
	}
	if defaults["enabled"] != "false" {
		t.Errorf("must be disabled by default, got %q", defaults["enabled"])
	}
	if defaults["index_url"] != "https://goblogplatform.github.io/plugins/index.json" {
		t.Errorf("index_url default: %q", defaults["index_url"])
	}
	if defaults["refresh_minutes"] != "15" {
		t.Errorf("refresh_minutes default: %q", defaults["refresh_minutes"])
	}
	pages := p.Pages()
	if len(pages) != 1 || pages[0].PageType != PageType || pages[0].Slug != "plugins" {
		t.Errorf("pages: %+v", pages)
	}
	var _ gplugin.Plugin = p
}

func TestSettingsHelpers(t *testing.T) {
	if got := indexURL(map[string]string{}); got != "https://goblogplatform.github.io/plugins/index.json" {
		t.Errorf("indexURL default: %q", got)
	}
	if got := indexURL(map[string]string{"index_url": " https://example.test/i.json "}); got != "https://example.test/i.json" {
		t.Errorf("indexURL trims: %q", got)
	}
	cases := map[string]time.Duration{"": 15 * time.Minute, "abc": 15 * time.Minute, "0": 15 * time.Minute, "-3": 15 * time.Minute, "5": 5 * time.Minute}
	for in, want := range cases {
		if got := refreshInterval(map[string]string{"refresh_minutes": in}); got != want {
			t.Errorf("refreshInterval(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestOnInit_CreatesPageOnce(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"))
	db.AutoMigrate(&blog.Page{})
	p := New()
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	if err := p.OnInit(db); err != nil {
		t.Fatal(err)
	}
	var pages []blog.Page
	db.Where("page_type = ?", PageType).Find(&pages)
	if len(pages) != 1 {
		t.Fatalf("expected exactly one directory page, got %d", len(pages))
	}
	pg := pages[0]
	if pg.Slug != "plugins" || pg.Title != "Plugins" || !pg.ShowInNav || pg.NavOrder != 30 || !pg.Enabled {
		t.Errorf("page: %+v", pg)
	}
}

func TestScheduledJob_RefreshesWhenStale(t *testing.T) {
	srv := newFixtureServer(t)
	p := New()
	p.fetcher = NewFetcher(srv.Client())
	jobs := p.ScheduledJobs()
	if len(jobs) != 1 || jobs[0].Interval != time.Minute {
		t.Fatalf("jobs: %+v", jobs)
	}
	settings := map[string]string{"index_url": srv.URL + "/index.json", "refresh_minutes": "15"}

	// Empty cache: the job fetches.
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := p.fetcher.Index(); !ok {
		t.Fatal("job should have fetched the index")
	}
	// Fresh cache: the job is a no-op.
	before := srv.hits.Load()
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() != before {
		t.Error("job should not refetch a fresh index")
	}
	// Stale cache: the job fetches again.
	p.fetcher.mu.Lock()
	p.fetcher.fetchedAt = time.Now().Add(-16 * time.Minute)
	p.fetcher.mu.Unlock()
	if err := jobs[0].Run(nil, settings); err != nil {
		t.Fatal(err)
	}
	if srv.hits.Load() == before {
		t.Error("job should refetch a stale index")
	}
}
