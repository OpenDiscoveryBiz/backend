package website

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OpenDiscoveryBiz/backend/internal/httpx"
)

//go:embed templates
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed all:dist
var distFS embed.FS

type Config struct {
	Resolver string
	Client   *http.Client
}

type Service struct {
	resolver string
	client   *http.Client
	tmpl     *template.Template
	css      string
	js       string
}

type page struct {
	Title  string
	Active string
	CSS    string
	JS     string
	ID     string
	Result string
	Body   template.HTML
}

func New(cfg Config) *Service {
	if cfg.Resolver == "" {
		cfg.Resolver = "https://resolver.opendiscovery.biz"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 5 * time.Second, CheckRedirect: httpx.MaxRedirects(5)}
	}
	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))
	css, js := assetPaths()
	return &Service{resolver: cfg.Resolver, client: cfg.Client, tmpl: tmpl, css: css, js: js}
}

func NewFromEnv() *Service {
	return New(Config{Resolver: httpx.Env("OPENDISCOVERY_RESOLVER", "https://resolver.opendiscovery.biz")})
}

func (s *Service) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, "home", "Welcome", "home", "", "")
	})
	r.Get("/resolver", s.resolverPage)
	r.Post("/resolver", s.resolverPage)
	r.Get("/specifications", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, "specifications", "Specifications", "specifications", "", "")
	})
	r.Get("/investigator", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, "investigator", "Business Investigator", "investigator", "", "")
	})
	r.Get("/publisher", func(w http.ResponseWriter, r *http.Request) {
		s.render(w, "publisher", "Business Publisher", "publisher", "", "")
	})
	staticRoot, err := fs.Sub(staticFS, "static")
	if err == nil {
		if imgs, err := fs.Sub(staticRoot, "imgs"); err == nil {
			r.Handle("/imgs/*", http.StripPrefix("/imgs/", fileServer(imgs)))
		}
		r.Get("/favicon.ico", serveFile(staticRoot, "favicon.ico"))
		r.Get("/robots.txt", serveFile(staticRoot, "robots.txt"))
	}
	if assets, err := fs.Sub(distFS, "dist/assets"); err == nil {
		r.Handle("/assets/*", http.StripPrefix("/assets/", fileServer(assets)))
	}
	return r
}

func (s *Service) resolverPage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(r.FormValue("id"))
	result := ""
	if id != "" {
		body, status, err := s.lookup(r, id)
		if err != nil || status >= 500 {
			http.Error(w, "upstream", http.StatusInternalServerError)
			return
		}
		result = string(body)
	}
	s.render(w, "resolver", "Resolver", "resolver", id, result)
}

func (s *Service) lookup(r *http.Request, id string) ([]byte, int, error) {
	u, err := url.Parse(strings.TrimRight(s.resolver, "/") + "/lookup")
	if err != nil {
		return nil, 0, err
	}
	q := u.Query()
	q.Set("id", id)
	q.Set("pretty", "1")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", "OpenDiscoveryWebsite (+https://www.opendiscovery.biz/)")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

func (s *Service) render(w http.ResponseWriter, content, title, active, id, result string) {
	p := page{Title: title, Active: active, CSS: s.css, JS: s.js, ID: id, Result: result}
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, content, p); err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
		return
	}
	p.Body = template.HTML(buf.String())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "shell", p); err != nil {
		http.Error(w, "template", http.StatusInternalServerError)
	}
}

func assetPaths() (css, js string) {
	for _, name := range []string{"dist/.vite/manifest.json", "dist/manifest.json"} {
		b, err := distFS.ReadFile(name)
		if err != nil {
			continue
		}
		var manifest map[string]struct {
			File    string   `json:"file"`
			IsEntry bool     `json:"isEntry"`
			CSS     []string `json:"css"`
		}
		if json.Unmarshal(b, &manifest) != nil {
			return "", ""
		}
		for _, entry := range manifest {
			if !entry.IsEntry {
				continue
			}
			if strings.HasSuffix(entry.File, ".js") {
				js = "/" + entry.File
			}
			if strings.HasSuffix(entry.File, ".css") {
				css = "/" + entry.File
			}
			if len(entry.CSS) > 0 {
				css = "/" + entry.CSS[0]
			}
		}
		return css, js
	}
	return "", ""
}

func serveFile(fsys fs.FS, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := fsys.Open(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || stat.IsDir() {
			http.NotFound(w, r)
			return
		}
		rs, ok := f.(io.ReadSeeker)
		if !ok {
			http.Error(w, "file", http.StatusInternalServerError)
			return
		}
		http.ServeContent(w, r, name, stat.ModTime(), rs)
	}
}

func fileServer(fsys fs.FS) http.Handler {
	return http.FileServer(noDir{http.FS(fsys)})
}

type noDir struct{ http.FileSystem }

func (n noDir) Open(name string) (http.File, error) {
	f, err := n.FileSystem.Open(name)
	if err != nil {
		return nil, err
	}
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		f.Close()
		if err == nil {
			err = os.ErrNotExist
		}
		return nil, err
	}
	return f, nil
}
