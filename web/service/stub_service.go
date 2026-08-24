package service

import (
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coinman-dev/3ax-ui/v2/database"
	"github.com/coinman-dev/3ax-ui/v2/database/model"
	"github.com/coinman-dev/3ax-ui/v2/logger"
	"github.com/coinman-dev/3ax-ui/v2/nginx"
)

//go:embed stubs/*.html
var stubTemplates embed.FS

// MaxStubSize caps a cover page at half a megabyte. Anything larger is either a
// mistake or a page that drags in a whole site, and it has to fit in the panel's
// backup alongside everything else.
const MaxStubSize = 512 * 1024

// StubTemplate is one of the starter pages shipped with the panel.
type StubTemplate struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Html string `json:"html"`
	Size int    `json:"size"`
}

// StubService owns the cover pages: the list, the one that is active, and
// keeping the file nginx serves in step with the database.
type StubService struct{}

// externalResource matches a reference to something the page would have to
// fetch from elsewhere.
//
// This is a warning, not a refusal. A page that pulls in a font from Google or
// a script from a CDN may simply fail to open on a network where those are
// blocked — and the burst of requests to third parties is itself a pattern
// worth not having. But an operator who knows what they are doing may still
// want it, so the panel says so and saves the page.
var externalResource = regexp.MustCompile(`(?i)(?:src|href)\s*=\s*["']\s*(?://|https?://)([^"'/\s]+)`)

// GetSites returns every page, newest first, without the markup — the list only
// needs names and sizes, and the pages themselves can be large.
func (s *StubService) GetSites() ([]model.StubSite, error) {
	var sites []model.StubSite
	err := database.GetDB().Model(model.StubSite{}).
		Select("id", "name", "active", "size", "created_at", "updated_at").
		Order("updated_at desc").Find(&sites).Error
	return sites, err
}

// GetSite returns one page in full.
func (s *StubService) GetSite(id int) (*model.StubSite, error) {
	site := &model.StubSite{}
	if err := database.GetDB().Model(model.StubSite{}).Where("id = ?", id).First(site).Error; err != nil {
		return nil, err
	}
	return site, nil
}

// SaveSite creates or updates a page and returns whatever is worth warning
// about. A returned error means nothing was saved.
func (s *StubService) SaveSite(site *model.StubSite) ([]string, error) {
	site.Name = strings.TrimSpace(site.Name)
	if site.Name == "" {
		return nil, fmt.Errorf("the page needs a name")
	}
	if strings.TrimSpace(site.Html) == "" {
		return nil, fmt.Errorf("the page is empty")
	}
	if len(site.Html) > MaxStubSize {
		return nil, fmt.Errorf("the page is %d KB, the limit is %d KB",
			len(site.Html)/1024, MaxStubSize/1024)
	}

	warnings := s.warnings(site.Html)
	site.Size = len(site.Html)
	now := time.Now().Unix()
	site.UpdatedAt = now

	db := database.GetDB()
	if site.Id == 0 {
		site.CreatedAt = now
		// The first page anyone saves becomes the active one: having to save it
		// and then remember to activate it is a step nobody would thank us for.
		var count int64
		if err := db.Model(model.StubSite{}).Count(&count).Error; err != nil {
			return nil, err
		}
		site.Active = count == 0
		if err := db.Create(site).Error; err != nil {
			return nil, err
		}
	} else {
		// Active is owned by ActivateSite, not by an edit of the markup.
		err := db.Model(model.StubSite{}).Where("id = ?", site.Id).Updates(map[string]any{
			"name":       site.Name,
			"html":       site.Html,
			"size":       site.Size,
			"updated_at": site.UpdatedAt,
		}).Error
		if err != nil {
			return nil, err
		}
	}

	if err := s.SyncToDisk(); err != nil {
		logger.Warning("stub: could not write the active page:", err)
	}
	return warnings, nil
}

// DeleteSite removes a page. The active one is refused: nginx would be left
// serving a file nothing in the panel knows about.
func (s *StubService) DeleteSite(id int) error {
	site, err := s.GetSite(id)
	if err != nil {
		return err
	}
	if site.Active {
		return fmt.Errorf("«%s» is the active page — make another one active first", site.Name)
	}
	return database.GetDB().Where("id = ?", id).Delete(model.StubSite{}).Error
}

// ActivateSite makes one page the one nginx serves.
func (s *StubService) ActivateSite(id int) error {
	if _, err := s.GetSite(id); err != nil {
		return err
	}
	db := database.GetDB()
	tx := db.Begin()
	if err := tx.Model(model.StubSite{}).Where("active = ?", true).Update("active", false).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Model(model.StubSite{}).Where("id = ?", id).Update("active", true).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit().Error; err != nil {
		return err
	}

	// The database is the source of truth and it has already changed: the page
	// IS active now. If the file cannot be written, the reconcile job will keep
	// trying — reporting the whole activation as failed would tell the operator
	// the opposite of what happened.
	if err := s.SyncToDisk(); err != nil {
		logger.Warning("stub: page activated but not written to disk:", err)
	}
	return nil
}

// ActiveSite returns the page nginx should be serving, or nil when there is none.
func (s *StubService) ActiveSite() *model.StubSite {
	site := &model.StubSite{}
	if err := database.GetDB().Model(model.StubSite{}).Where("active = ?", true).First(site).Error; err != nil {
		return nil
	}
	return site
}

// SyncToDisk writes the active page where nginx serves it from, or clears the
// file when no page is active. Called after every change and from the reconcile
// job, so a database restored from a backup brings its cover page back with it.
func (s *StubService) SyncToDisk() error {
	if site := s.ActiveSite(); site != nil {
		return nginx.WriteStub(site.Html)
	}
	return nginx.RemoveStub()
}

// Templates returns the starter pages shipped with the panel.
func (s *StubService) Templates() []StubTemplate {
	defs := []struct{ key, name, file string }{
		{"construction", "Site under construction", "stubs/construction.html"},
		{"personal", "Personal page", "stubs/personal.html"},
		{"studio", "Small studio", "stubs/studio.html"},
	}
	out := make([]StubTemplate, 0, len(defs))
	for _, d := range defs {
		body, err := stubTemplates.ReadFile(d.file)
		if err != nil {
			logger.Warning("stub: missing built-in template", d.file, err)
			continue
		}
		out = append(out, StubTemplate{Key: d.key, Name: d.name, Html: string(body), Size: len(body)})
	}
	return out
}

// warnings lists what is worth telling the operator about a page without
// refusing to save it.
func (s *StubService) warnings(html string) []string {
	out := StubWarnings(html)

	// A page shipped with the panel, served unchanged, is a fingerprint: the
	// same bytes on every 3AX-UI server anywhere. The whole point of the cover
	// page is to look like one particular site, so say this out loud.
	for _, t := range s.Templates() {
		if strings.TrimSpace(html) == strings.TrimSpace(t.Html) {
			out = append(out, "this is the stock page that ships with the panel, unchanged — "+
				"the same page on every server is a give-away of its own, so edit the text and the name")
			break
		}
	}
	return out
}

// StubWarnings lists the problems that can be seen in the markup alone.
func StubWarnings(html string) []string {
	var warnings []string

	hosts := map[string]bool{}
	for _, m := range externalResource.FindAllStringSubmatch(html, -1) {
		host := strings.ToLower(m[1])
		if host != "" {
			hosts[host] = true
		}
	}
	if len(hosts) > 0 {
		names := make([]string, 0, len(hosts))
		for host := range hosts {
			names = append(names, host)
		}
		sort.Strings(names)
		warnings = append(warnings, fmt.Sprintf(
			"the page loads files from %s — it may not open where those are blocked, and the requests are a pattern of their own",
			strings.Join(names, ", ")))
	}
	return warnings
}
