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
func (s *StubService) SaveSite(site *model.StubSite) ([]NginxWarning, error) {
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

// SyncToDisk writes the active page where nginx serves it from. Called after
// every change and from the reconcile job, so a database restored from a backup
// brings its cover page back with it.
//
// If there is no page at all it installs the built-in one first. A server with
// the front-end on and an empty web root answers its own domain with nothing —
// which is worse than any placeholder, because the whole point is to look like
// an ordinary site. The page is created as a normal row, so it shows up in the
// list and can be edited or replaced like any other.
func (s *StubService) SyncToDisk() error {
	site := s.ActiveSite()
	if site == nil {
		var err error
		if site, err = s.installDefaultSite(); err != nil {
			return err
		}
	}
	return nginx.WriteStub(site.Html)
}

// DefaultTemplateKey is the page a server falls back on when nothing else has
// been set up.
const DefaultTemplateKey = "construction"

// installDefaultSite puts the built-in page in the database and makes it
// active. It is a no-op once any page exists.
func (s *StubService) installDefaultSite() (*model.StubSite, error) {
	var templates []StubTemplate
	for _, t := range s.Templates() {
		if t.Key == DefaultTemplateKey {
			templates = append([]StubTemplate{t}, templates...)
			continue
		}
		templates = append(templates, t)
	}
	if len(templates) == 0 {
		return nil, fmt.Errorf("no built-in cover page to fall back on")
	}
	tpl := templates[0]

	site := &model.StubSite{
		Name: tpl.Name, Html: tpl.Html, Size: len(tpl.Html), Active: true,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}
	if err := database.GetDB().Create(site).Error; err != nil {
		return nil, fmt.Errorf("install the default cover page: %w", err)
	}
	logger.Info("stub: no cover page was set up, installed the built-in one")
	return site, nil
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
//
// They travel as codes, like every other warning this feature produces: the
// service runs with no request and no locale, so a sentence written here would
// reach the panel in English whatever language it is set to.
func (s *StubService) warnings(html string) []NginxWarning {
	out := StubWarnings(html)

	// A page shipped with the panel, served unchanged, is a fingerprint: the
	// same bytes on every 3AX-UI server anywhere. The whole point of the cover
	// page is to look like one particular site, so say this out loud.
	for _, t := range s.Templates() {
		if strings.TrimSpace(html) == strings.TrimSpace(t.Html) {
			out = append(out, warn("stockCoverPage"))
			break
		}
	}
	return out
}

// StubWarnings lists the problems that can be seen in the markup alone.
func StubWarnings(html string) []NginxWarning {
	var warnings []NginxWarning

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
		warnings = append(warnings, warn("stubExternal", strings.Join(names, ", ")))
	}
	return warnings
}
