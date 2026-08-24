package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

func newStubTestService(t *testing.T) *StubService {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	nginx.WebRoot = t.TempDir()
	t.Cleanup(func() { nginx.WebRoot = "/usr/local/x-ui/www" })
	return &StubService{}
}

// TestFirstSiteBecomesActive: saving a page and then having to remember to
// activate it is a step nobody would thank us for, and a web root with no
// index.html means nginx answers the domain with a 403.
func TestFirstSiteBecomesActive(t *testing.T) {
	s := newStubTestService(t)

	first := &model.StubSite{Name: "one", Html: "<!doctype html><title>one</title><p>one"}
	if _, err := s.SaveSite(first); err != nil {
		t.Fatalf("save: %v", err)
	}
	if !first.Active {
		t.Error("the first page saved should become the active one")
	}
	if got := nginx.StubOnDisk(); got != first.Html {
		t.Errorf("the active page was not written to disk: %q", got)
	}

	second := &model.StubSite{Name: "two", Html: "<!doctype html><title>two</title><p>two"}
	if _, err := s.SaveSite(second); err != nil {
		t.Fatalf("save second: %v", err)
	}
	if second.Active {
		t.Error("a later page must not take over on its own")
	}
	if got := nginx.StubOnDisk(); got != first.Html {
		t.Error("saving another page changed what nginx serves")
	}

	if err := s.ActivateSite(second.Id); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := nginx.StubOnDisk(); got != second.Html {
		t.Errorf("disk still has the old page after activating another: %q", got)
	}
	if active := s.ActiveSite(); active == nil || active.Id != second.Id {
		t.Error("the database still points at the old page")
	}

	// Exactly one page may be active, or the next sync picks an arbitrary one.
	var count int64
	if err := database.GetDB().Model(model.StubSite{}).Where("active = ?", true).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d pages are active, want exactly 1", count)
	}
}

// TestEditingActiveSiteUpdatesDisk — the page is edited far more often than it
// is switched, and an edit that does not reach nginx looks like the panel
// ignoring the operator.
func TestEditingActiveSiteUpdatesDisk(t *testing.T) {
	s := newStubTestService(t)
	site := &model.StubSite{Name: "one", Html: "<p>before"}
	if _, err := s.SaveSite(site); err != nil {
		t.Fatal(err)
	}
	site.Html = "<p>after"
	if _, err := s.SaveSite(site); err != nil {
		t.Fatal(err)
	}
	if got := nginx.StubOnDisk(); got != "<p>after" {
		t.Errorf("disk has %q, want the edited page", got)
	}
	stored, err := s.GetSite(site.Id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Size != len("<p>after") {
		t.Errorf("stored size = %d, want %d", stored.Size, len("<p>after"))
	}
	if !stored.Active {
		t.Error("editing the markup cleared the active flag")
	}
}

func TestDeleteRefusesTheActiveSite(t *testing.T) {
	s := newStubTestService(t)
	site := &model.StubSite{Name: "only", Html: "<p>x"}
	if _, err := s.SaveSite(site); err != nil {
		t.Fatal(err)
	}
	err := s.DeleteSite(site.Id)
	if err == nil {
		t.Fatal("the active page was deleted; nginx would serve a file the panel no longer knows about")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestSaveValidation(t *testing.T) {
	s := newStubTestService(t)
	cases := []struct {
		name string
		site model.StubSite
		want string
	}{
		{"no name", model.StubSite{Name: "  ", Html: "<p>x"}, "needs a name"},
		{"empty page", model.StubSite{Name: "x", Html: "   "}, "is empty"},
		{"too large", model.StubSite{Name: "x", Html: strings.Repeat("a", MaxStubSize+1)}, "the limit is"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.SaveSite(&tc.site)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestWarningsAreAdviceNotRefusal: a page that pulls in outside files still
// saves — the operator may know exactly what they are doing — but they are told.
func TestWarningsAreAdviceNotRefusal(t *testing.T) {
	s := newStubTestService(t)
	site := &model.StubSite{
		Name: "external",
		Html: `<link href="https://fonts.googleapis.com/css?family=X" rel="stylesheet">` +
			`<script src="//cdn.example.net/app.js"></script><p>hi`,
	}
	warnings, err := s.SaveSite(site)
	if err != nil {
		t.Fatalf("a page with external files must still save: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("nothing was said about the external files")
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "fonts.googleapis.com") || !strings.Contains(joined, "cdn.example.net") {
		t.Errorf("the warning does not name the hosts: %q", joined)
	}
	if stored, err := s.GetSite(site.Id); err != nil || stored.Name != "external" {
		t.Error("the page was not saved despite the warning")
	}
}

// TestStockTemplateIsFlagged guards the point of the whole feature: the same
// bytes served by every 3AX-UI install would be a fingerprint of the panel.
func TestStockTemplateIsFlagged(t *testing.T) {
	s := newStubTestService(t)
	templates := s.Templates()
	if len(templates) < 3 {
		t.Fatalf("got %d built-in pages, want 3", len(templates))
	}

	site := &model.StubSite{Name: "stock", Html: templates[0].Html}
	warnings, err := s.SaveSite(site)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "give-away") {
		t.Errorf("an unchanged stock page was not flagged: %v", warnings)
	}

	// A page the operator actually edited must not be nagged about.
	edited := &model.StubSite{Name: "edited", Html: templates[0].Html + "\n<!-- ours -->"}
	warnings, err = s.SaveSite(edited)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range warnings {
		if strings.Contains(w, "give-away") {
			t.Errorf("an edited page was still called stock: %q", w)
		}
	}
}

// TestBuiltInTemplatesAreSelfContained: a cover page that fetches a font from
// Google fails to open exactly where it matters most, and announces itself by
// the requests it makes. The pages we ship must not do that.
func TestBuiltInTemplatesAreSelfContained(t *testing.T) {
	s := newStubTestService(t)
	for _, tpl := range s.Templates() {
		t.Run(tpl.Key, func(t *testing.T) {
			if warnings := StubWarnings(tpl.Html); len(warnings) > 0 {
				t.Errorf("built-in page %q is not self-contained: %v", tpl.Key, warnings)
			}
			if !strings.Contains(strings.ToLower(tpl.Html), "<!doctype html>") {
				t.Error("not a complete document")
			}
			if tpl.Size == 0 || tpl.Size != len(tpl.Html) {
				t.Errorf("size = %d, want %d", tpl.Size, len(tpl.Html))
			}
		})
	}
}

// TestSyncInstallsTheBuiltInPageWhenThereIsNone: a server with the front-end
// on and an empty web root answers its own domain with nothing, which is worse
// than any placeholder — the whole point is to look like an ordinary site.
func TestSyncInstallsTheBuiltInPageWhenThereIsNone(t *testing.T) {
	s := newStubTestService(t)

	if sites, err := s.GetSites(); err != nil {
		t.Fatal(err)
	} else if len(sites) != 0 {
		t.Fatalf("expected an empty database, found %d pages", len(sites))
	}

	if err := s.SyncToDisk(); err != nil {
		t.Fatalf("sync: %v", err)
	}

	on := nginx.StubOnDisk()
	if on == "" {
		t.Fatal("nothing was written; the domain would answer with an empty site")
	}
	active := s.ActiveSite()
	if active == nil {
		t.Fatal("the page was written but not recorded, so the panel cannot show or edit it")
	}
	if active.Html != on {
		t.Error("what is on disk is not what the database says is active")
	}

	// It has to be the "under construction" page, and it has to be a normal
	// row: visible in the list, editable, replaceable.
	var construction string
	for _, tpl := range s.Templates() {
		if tpl.Key == DefaultTemplateKey {
			construction = tpl.Html
		}
	}
	if active.Html != construction {
		t.Error("the page installed is not the built-in default")
	}
	if sites, err := s.GetSites(); err != nil || len(sites) != 1 {
		t.Errorf("the default page did not turn up in the list: %v (%d)", err, len(sites))
	}

	// Running again must not pile up copies.
	if err := s.SyncToDisk(); err != nil {
		t.Fatal(err)
	}
	if sites, _ := s.GetSites(); len(sites) != 1 {
		t.Errorf("a second sync created %d pages, want 1", len(sites))
	}

	// And a page the operator made stays the active one.
	mine := &model.StubSite{Name: "mine", Html: "<!doctype html><title>mine</title><p>mine"}
	if _, err := s.SaveSite(mine); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateSite(mine.Id); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncToDisk(); err != nil {
		t.Fatal(err)
	}
	if got := nginx.StubOnDisk(); got != mine.Html {
		t.Errorf("the sync replaced the operator's page with the default: %q", got)
	}
}

// TestActivateSucceedsWhenTheDiskDoesNot: the database is the source of truth
// and it has already changed, so the page is active whatever happens to the
// file. Reporting a failure here would tell the operator the opposite of what
// happened — and the reconcile job writes the file on its next pass anyway.
func TestActivateSucceedsWhenTheDiskDoesNot(t *testing.T) {
	s := newStubTestService(t)
	first := &model.StubSite{Name: "one", Html: "<p>one"}
	second := &model.StubSite{Name: "two", Html: "<p>two"}
	for _, site := range []*model.StubSite{first, second} {
		if _, err := s.SaveSite(site); err != nil {
			t.Fatal(err)
		}
	}

	// A web root that cannot be written to: a file where the directory should be.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}
	nginx.WebRoot = blocked

	if err := s.ActivateSite(second.Id); err != nil {
		t.Fatalf("activation reported a failure although the page did become active: %v", err)
	}
	if active := s.ActiveSite(); active == nil || active.Id != second.Id {
		t.Error("the page is not actually active")
	}
}
