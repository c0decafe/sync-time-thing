package web

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mnm/sync-time-thing/internal/auth"
	"github.com/mnm/sync-time-thing/internal/cronexpr"
	"github.com/mnm/sync-time-thing/internal/domain"
)

//go:embed templates/*.html static/*
var assets embed.FS

type contextKey string

const usernameKey contextKey = "username"

type Store interface {
	GetAdmin(ctx context.Context, username string) (domain.AdminUser, error)
	CreateSession(ctx context.Context, username, tokenHash string, expiresAt time.Time) error
	GetSession(ctx context.Context, tokenHash string, now time.Time) (domain.Session, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	GetSettings(ctx context.Context) (domain.Settings, error)
	SaveSettings(ctx context.Context, settings domain.Settings) error
	ListRules(ctx context.Context) ([]domain.Rule, error)
	GetRule(ctx context.Context, id int64) (domain.Rule, error)
	SaveRule(ctx context.Context, rule domain.Rule) (domain.Rule, error)
	DeleteRule(ctx context.Context, id int64) error
	ListRecentRuns(ctx context.Context, limit int) ([]domain.RuleRun, error)
}

type SyncthingClient interface {
	Ping(ctx context.Context) error
	ListDevices(ctx context.Context) ([]domain.Device, error)
	ListFolders(ctx context.Context) ([]domain.Folder, error)
}

type ClientFactory func(settings domain.Settings) (SyncthingClient, error)

type Options struct {
	CookieName    string
	SessionTTL    time.Duration
	SecureCookies bool
	Now           func() time.Time
	Entropy       io.Reader
}

type Server struct {
	store         Store
	makeClient    ClientFactory
	templates     *template.Template
	cookieName    string
	sessionTTL    time.Duration
	secureCookies bool
	now           func() time.Time
	entropy       io.Reader
}

type pageData struct {
	Title           string
	Username        string
	Notice          string
	Error           string
	Settings        domain.Settings
	Rules           []ruleView
	RuleForm        domain.Rule
	Editing         bool
	Runs            []domain.RuleRun
	Devices         []domain.Device
	Folders         []domain.Folder
	ConnectionError string
}

type ruleView struct {
	Rule    domain.Rule
	Preview []string
}

type targetCatalog struct {
	Devices     []domain.Device
	Folders     []domain.Folder
	deviceNames map[string]string
	folderNames map[string]string
}

func New(store Store, factory ClientFactory, options Options) (*Server, error) {
	if options.CookieName == "" {
		options.CookieName = "syncthing_scheduler_session"
	}
	if options.SessionTTL == 0 {
		options.SessionTTL = 24 * time.Hour
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	templates, err := template.New("base").Funcs(template.FuncMap{
		"eq": func(left, right any) bool { return left == right },
		"formatTime": func(ts time.Time) string {
			if ts.IsZero() {
				return "-"
			}
			return ts.Format("2006-01-02 15:04:05 MST")
		},
	}).ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	return &Server{
		store:         store,
		makeClient:    factory,
		templates:     templates,
		cookieName:    options.CookieName,
		sessionTTL:    options.SessionTTL,
		secureCookies: options.SecureCookies,
		now:           options.Now,
		entropy:       options.Entropy,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	staticFS, err := fs.Sub(assets, "static")
	if err == nil {
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	}
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.HandleFunc("/login", s.handleLogin)
	mux.Handle("/logout", s.requireAuth(http.HandlerFunc(s.handleLogout)))
	mux.Handle("/dashboard", s.requireAuth(http.HandlerFunc(s.handleDashboard)))
	mux.Handle("/settings", s.requireAuth(http.HandlerFunc(s.handleSettings)))
	mux.Handle("/rules", s.requireAuth(http.HandlerFunc(s.handleRules)))
	mux.Handle("/rules/delete", s.requireAuth(http.HandlerFunc(s.handleRuleDelete)))
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, ok := s.authenticatedUsername(r); ok {
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.GetSettings(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.render(w, http.StatusOK, "login", pageData{Title: "Login"})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.render(w, http.StatusBadRequest, "login", pageData{Title: "Login", Error: err.Error()})
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		user, err := s.store.GetAdmin(r.Context(), username)
		if err != nil || auth.ComparePassword(user.PasswordHash, password) != nil {
			s.render(w, http.StatusUnauthorized, "login", pageData{Title: "Login", Error: "Invalid credentials."})
			return
		}
		plainToken, tokenHash, err := auth.NewSessionToken(s.entropy)
		if err != nil {
			s.render(w, http.StatusInternalServerError, "login", pageData{Title: "Login", Error: err.Error()})
			return
		}
		expiresAt := s.now().Add(s.sessionTTL).UTC()
		if err := s.store.CreateSession(r.Context(), username, tokenHash, expiresAt); err != nil {
			s.render(w, http.StatusInternalServerError, "login", pageData{Title: "Login", Error: err.Error()})
			return
		}
		s.setSessionCookie(w, plainToken, expiresAt)
		http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if cookie, err := r.Cookie(s.cookieName); err == nil {
		_ = s.store.DeleteSession(r.Context(), auth.HashToken(cookie.Value))
	}
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	rules, err := s.store.ListRules(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	runs, err := s.store.ListRecentRuns(r.Context(), 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	catalog, connectionError := s.loadCatalog(r.Context(), settings)
	data := pageData{
		Title:           "Dashboard",
		Username:        currentUsername(r.Context()),
		Settings:        settings,
		Rules:           s.previewRules(rules, settings.Timezone),
		Runs:            runs,
		Devices:         catalog.Devices,
		Folders:         catalog.Folders,
		ConnectionError: connectionError,
	}
	s.render(w, http.StatusOK, "dashboard", data)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.store.GetSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		s.render(w, http.StatusOK, "settings", pageData{
			Title:    "Settings",
			Username: currentUsername(r.Context()),
			Settings: settings,
			Notice:   settingsSavedNotice(r, settings),
		})
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			s.render(w, http.StatusBadRequest, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Error: err.Error()})
			return
		}
		settings := domain.Settings{
			SyncthingURL:    strings.TrimSpace(r.FormValue("syncthing_url")),
			SyncthingAPIKey: strings.TrimSpace(r.FormValue("syncthing_api_key")),
			Timezone:        strings.TrimSpace(r.FormValue("timezone")),
		}
		if settings.Timezone == "" {
			s.render(w, http.StatusBadRequest, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Settings: settings, Error: "Timezone is required."})
			return
		}
		if _, err := time.LoadLocation(settings.Timezone); err != nil {
			s.render(w, http.StatusBadRequest, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Settings: settings, Error: err.Error()})
			return
		}
		if (settings.SyncthingURL == "") != (settings.SyncthingAPIKey == "") {
			s.render(w, http.StatusBadRequest, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Settings: settings, Error: "Syncthing URL and API key must both be set or both be blank."})
			return
		}
		if settings.SyncthingURL != "" {
			client, err := s.makeClient(settings)
			if err != nil {
				s.render(w, http.StatusBadRequest, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Settings: settings, Error: friendlySyncthingError(err)})
				return
			}
			if err := client.Ping(r.Context()); err != nil {
				s.render(w, http.StatusBadGateway, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Settings: settings, Error: friendlySyncthingError(err)})
				return
			}
		}
		if err := s.store.SaveSettings(r.Context(), settings); err != nil {
			s.render(w, http.StatusInternalServerError, "settings", pageData{Title: "Settings", Username: currentUsername(r.Context()), Settings: settings, Error: err.Error()})
			return
		}
		http.Redirect(w, r, "/settings?saved=1", http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var form domain.Rule
		editing := false
		if rawID := r.URL.Query().Get("edit"); rawID != "" {
			ruleID, err := strconv.ParseInt(rawID, 10, 64)
			if err != nil {
				s.renderRulesPage(w, r, http.StatusBadRequest, form, false, "Invalid rule id.")
				return
			}
			loaded, err := s.store.GetRule(r.Context(), ruleID)
			if err != nil {
				s.renderRulesPage(w, r, http.StatusNotFound, form, false, err.Error())
				return
			}
			form = loaded
			editing = true
		}
		s.renderRulesPage(w, r, http.StatusOK, form, editing, "")
	case http.MethodPost:
		settings, err := s.store.GetSettings(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		catalog, connectionError := s.loadCatalog(r.Context(), settings)
		if err := r.ParseForm(); err != nil {
			s.renderRulesPage(w, r, http.StatusBadRequest, domain.Rule{}, false, err.Error())
			return
		}
		rule, err := s.ruleFromForm(r, catalog, connectionError)
		if err != nil {
			s.renderRulesPage(w, r, http.StatusBadRequest, rule, rule.ID != 0, err.Error())
			return
		}
		if _, err := cronexpr.Parse(rule.Schedule); err != nil {
			s.renderRulesPage(w, r, http.StatusBadRequest, rule, rule.ID != 0, err.Error())
			return
		}
		if _, err := s.store.SaveRule(r.Context(), rule); err != nil {
			s.renderRulesPage(w, r, http.StatusInternalServerError, rule, rule.ID != 0, err.Error())
			return
		}
		redirectQuery := "created"
		if rule.ID != 0 {
			redirectQuery = "updated"
		}
		http.Redirect(w, r, "/rules?saved="+redirectQuery, http.StatusSeeOther)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.renderRulesPage(w, r, http.StatusBadRequest, domain.Rule{}, false, err.Error())
		return
	}
	ruleID, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		s.renderRulesPage(w, r, http.StatusBadRequest, domain.Rule{}, false, "Invalid rule id.")
		return
	}
	if err := s.store.DeleteRule(r.Context(), ruleID); err != nil {
		s.renderRulesPage(w, r, http.StatusNotFound, domain.Rule{}, false, "That rule could not be deleted because it no longer exists.")
		return
	}
	http.Redirect(w, r, "/rules?saved=deleted", http.StatusSeeOther)
}

func (s *Server) renderRulesPage(w http.ResponseWriter, r *http.Request, status int, form domain.Rule, editing bool, errorMessage string) {
	settings, err := s.store.GetSettings(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if !editing && form.ID == 0 && form.Name == "" && form.Schedule == "" {
		form.Enabled = true
	}
	rules, err := s.store.ListRules(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	catalog, connectionError := s.loadCatalog(r.Context(), settings)
	data := pageData{
		Title:           "Rules",
		Username:        currentUsername(r.Context()),
		Notice:          rulesSavedNotice(r),
		Settings:        settings,
		Rules:           s.previewRules(rules, settings.Timezone),
		RuleForm:        form,
		Editing:         editing,
		Devices:         catalog.Devices,
		Folders:         catalog.Folders,
		ConnectionError: connectionError,
		Error:           errorMessage,
	}
	s.render(w, status, "rules", data)
}

func (s *Server) loadCatalog(ctx context.Context, settings domain.Settings) (targetCatalog, string) {
	if settings.SyncthingURL == "" || settings.SyncthingAPIKey == "" || s.makeClient == nil {
		return targetCatalog{}, ""
	}
	client, err := s.makeClient(settings)
	if err != nil {
		return targetCatalog{}, friendlySyncthingError(err)
	}
	devices, err := client.ListDevices(ctx)
	if err != nil {
		return targetCatalog{}, friendlySyncthingError(err)
	}
	folders, err := client.ListFolders(ctx)
	if err != nil {
		return targetCatalog{}, friendlySyncthingError(err)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].Name < devices[j].Name })
	sort.Slice(folders, func(i, j int) bool { return folders[i].Label < folders[j].Label })
	catalog := targetCatalog{
		Devices:     devices,
		Folders:     folders,
		deviceNames: make(map[string]string, len(devices)),
		folderNames: make(map[string]string, len(folders)),
	}
	for _, device := range devices {
		catalog.deviceNames[device.ID] = device.Name
	}
	for _, folder := range folders {
		catalog.folderNames[folder.ID] = folder.Label
	}
	return catalog, ""
}

func (s *Server) ruleFromForm(r *http.Request, catalog targetCatalog, connectionError string) (domain.Rule, error) {
	var rule domain.Rule
	if rawID := strings.TrimSpace(r.FormValue("id")); rawID != "" {
		parsedID, err := strconv.ParseInt(rawID, 10, 64)
		if err != nil {
			return rule, fmt.Errorf("invalid rule id")
		}
		rule.ID = parsedID
	}
	parsedAction, err := domain.ParseAction(r.FormValue("action"))
	if err != nil {
		return rule, err
	}
	parsedTarget, err := domain.ParseTargetKind(r.FormValue("target_kind"))
	if err != nil {
		return rule, err
	}
	rule = domain.Rule{
		ID:         rule.ID,
		Name:       strings.TrimSpace(r.FormValue("name")),
		Schedule:   strings.TrimSpace(r.FormValue("schedule")),
		Action:     parsedAction,
		TargetKind: parsedTarget,
		TargetID:   strings.TrimSpace(r.FormValue("target_id")),
		Enabled:    r.FormValue("enabled") == "on",
	}
	switch rule.TargetKind {
	case domain.TargetGlobal:
		rule.TargetID = ""
		rule.TargetName = "All devices"
	case domain.TargetDevice:
		if connectionError != "" {
			return rule, fmt.Errorf("cannot validate devices: %s", connectionError)
		}
		name, ok := catalog.deviceNames[rule.TargetID]
		if !ok {
			return rule, fmt.Errorf("unknown device id %q", rule.TargetID)
		}
		rule.TargetName = name
	case domain.TargetFolder:
		if connectionError != "" {
			return rule, fmt.Errorf("cannot validate folders: %s", connectionError)
		}
		name, ok := catalog.folderNames[rule.TargetID]
		if !ok {
			return rule, fmt.Errorf("unknown folder id %q", rule.TargetID)
		}
		rule.TargetName = name
	}
	if err := rule.ValidateBasic(); err != nil {
		return rule, err
	}
	return rule, nil
}

func (s *Server) previewRules(rules []domain.Rule, timezone string) []ruleView {
	locationName := timezone
	if strings.TrimSpace(locationName) == "" {
		locationName = "UTC"
	}
	location, err := time.LoadLocation(locationName)
	if err != nil {
		location = time.UTC
	}
	views := make([]ruleView, 0, len(rules))
	for _, rule := range rules {
		view := ruleView{Rule: rule}
		expression, err := cronexpr.Parse(rule.Schedule)
		if err == nil {
			nextRuns, err := expression.NextN(s.now().In(location), 3)
			if err == nil {
				for _, next := range nextRuns {
					view.Preview = append(view.Preview, next.In(location).Format("2006-01-02 15:04 MST"))
				}
			}
		}
		views = append(views, view)
	}
	return views
}

func settingsSavedNotice(r *http.Request, settings domain.Settings) string {
	if r.URL.Query().Get("saved") != "1" {
		return ""
	}
	if settings.SyncthingURL != "" && settings.SyncthingAPIKey != "" {
		return "Settings saved and Syncthing connection verified."
	}
	return "Settings saved."
}

func rulesSavedNotice(r *http.Request) string {
	switch r.URL.Query().Get("saved") {
	case "created":
		return "Rule created."
	case "updated":
		return "Rule updated."
	case "deleted":
		return "Rule deleted."
	default:
		return ""
	}
}

func friendlySyncthingError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "parse syncthing url"), strings.Contains(message, "invalid url"):
		return "Enter a valid Syncthing URL, for example http://localhost:8384."
	case strings.Contains(message, "401"), strings.Contains(message, "403"), strings.Contains(message, "unauthorized"), strings.Contains(message, "forbidden"):
		return "Syncthing rejected the API key. Check the key in Syncthing and try again."
	case strings.Contains(message, "dial tcp"), strings.Contains(message, "connection refused"), strings.Contains(message, "no such host"), strings.Contains(message, "timeout"):
		return "Could not reach Syncthing at that URL. Check the address, network access, and that Syncthing is running."
	default:
		return "Could not talk to Syncthing. Check the URL, API key, and network access, then try again."
	}
}

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(s.cookieName)
		if err != nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		session, err := s.store.GetSession(r.Context(), auth.HashToken(cookie.Value), s.now())
		if err != nil {
			s.clearSessionCookie(w)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		ctx := context.WithValue(r.Context(), usernameKey, session.Username)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) authenticatedUsername(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		return "", false
	}
	session, err := s.store.GetSession(r.Context(), auth.HashToken(cookie.Value), s.now())
	if err != nil {
		return "", false
	}
	return session.Username, true
}

func currentUsername(ctx context.Context) string {
	username, _ := ctx.Value(usernameKey).(string)
	return username
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.secureCookies,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.secureCookies,
	})
}

func (s *Server) render(w http.ResponseWriter, status int, templateName string, data pageData) {
	w.WriteHeader(status)
	if err := s.templates.ExecuteTemplate(w, templateName, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
